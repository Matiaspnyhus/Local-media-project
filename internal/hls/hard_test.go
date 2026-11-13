package hls

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests treat a session whose picture is converted roughly: jumps on
// top of each other, players closed in the middle of a chunk, a converter
// that dies, films of awkward shapes. After each, nothing may be left
// running, nobody may be left waiting, and every chunk that was handed over
// must be whole and be the chunk the list says it is.

// wholeChunk says what is wrong with a chunk of a converted film as it was
// handed over, or "". It must be made of whole fragments (an index and its
// data, nothing cut short), and its first picture must be shown at the
// moment the list gives for chunk k.
func wholeChunk(spec Spec, header, chunk []byte, k int) string {
	var v videoTrack
	children(header, func(typ string, body []byte) {
		if typ == "moov" {
			v = findVideo(body)
		}
	})
	rest, want, n := chunk, "moof", 0
	start, found := 0.0, false
	for len(rest) > 0 {
		if len(rest) < 8 {
			return fmt.Sprintf("%d stray bytes at the end", len(rest))
		}
		size, typ := int(binary.BigEndian.Uint32(rest[:4])), string(rest[4:8])
		if size < 8 || size > len(rest) {
			return fmt.Sprintf("a %s box of %d bytes with only %d left: cut short", typ, size, len(rest))
		}
		if typ != want {
			return fmt.Sprintf("a %s box where a %s belongs", typ, want)
		}
		if typ == "moof" {
			if ms, _, ok := fragmentStart(rest[8:size], v); ok && !found {
				start, found = ms, true
			}
			want = "mdat"
		} else {
			want = "moof"
			n++
		}
		rest = rest[size:]
	}
	if n == 0 || want != "moof" {
		return fmt.Sprintf("%d whole fragments, and then an index without its data", n)
	}
	if !found {
		return "no picture in it"
	}
	if at := float64(spec.Grid.Cuts[k] + spec.Grid.shift()); math.Abs(start-at) > 2 {
		return fmt.Sprintf("its picture begins at %.3f s, the list says %.3f", start/1000, at/1000)
	}
	if k < len(spec.Grid.Cuts)-1 && n != 1 {
		return fmt.Sprintf("%d fragments in a chunk that is not the last", n)
	}
	return ""
}

// longConvertSpec is the two-minute film with its picture converted: forty
// chunks to jump about in.
func longConvertSpec(t *testing.T, ffmpeg, ffprobe string) Spec {
	t.Helper()
	spec, _ := convertSpec(t, ffprobe, source(t, ffmpeg, "long"))
	if len(spec.Grid.Cuts) != 40 {
		t.Fatalf("the long film was cut into %d chunks, want 40: %v", len(spec.Grid.Cuts), spec.Grid.Cuts)
	}
	spec.Owner = "ann"
	return spec
}

// nothingLeft fails the test if any converter outlives the closing of the
// manager, or any goroutine does.
func nothingLeft(t *testing.T, m *Manager, goroutines int) {
	t.Helper()
	m.Close()
	m.mu.Lock()
	left := len(m.live)
	m.mu.Unlock()
	if left != 0 {
		t.Errorf("%d converters outlived the closing of the manager", left)
	}
	if pids, known := converters(); known && !settled(func() bool { pids, _ = converters(); return len(pids) == 0 }) {
		t.Errorf("ffmpeg still running after the manager was closed: %v", pids)
	}
	if _, err := os.Stat(m.root); err == nil {
		t.Error("closing left the chunk folder behind")
	}
	if !settled(func() bool { return runtime.NumGoroutine() <= goroutines }) {
		t.Errorf("%d goroutines before, %d after the manager was closed", goroutines, runtime.NumGoroutine())
	}
}

// A viewer drags the timeline of a converted film back and forth: requests
// all over the film, most of them given up at once. Whatever is handed over
// must be whole and in its place, the converter must end up where the last
// request wants it, and nothing may be left behind.
func TestAStormOfJumpsOnAConvertedFilm(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longConvertSpec(t, ffmpeg, ffprobe)
	goroutines := runtime.NumGoroutine()
	m, err := New(ffmpeg, filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var logMu sync.Mutex
	var logged []string
	m.Log = func(format string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, fmt.Sprintf(format, a...))
	}
	bg := context.Background()
	token := m.Open(spec)
	header, err := m.Init(bg, token)
	if err != nil {
		t.Fatal(err)
	}
	last := len(spec.Grid.Cuts) - 1
	var served, wrong atomic.Int64
	var wg sync.WaitGroup
	// Three players' worth of requests at once. One in three asks for the
	// chunk after the one last asked for, as a player that has settled
	// does; the rest are jumps. Most are given up before a chunk could be
	// made, some are waited for.
	var at atomic.Int64
	until := time.Now().Add(8 * time.Second)
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 100))
			for time.Now().Before(until) {
				k := rng.Intn(last + 1)
				switch rng.Intn(6) {
				case 0:
					k = last - rng.Intn(2) // the end of the film, often
				case 1, 2:
					k = min(int(at.Load())+1, last)
				}
				at.Store(int64(k))
				patience := time.Duration(rng.Intn(300)) * time.Millisecond
				if rng.Intn(3) == 0 {
					patience = 20 * time.Second
				}
				ctx, cancel := context.WithTimeout(bg, patience)
				var b []byte
				var err error
				if rng.Intn(10) == 0 {
					b, err = m.Init(ctx, token)
					if err == nil && !bytes.Equal(b, header) {
						wrong.Add(1)
						t.Errorf("a header that differs from the first one was handed over (%d and %d bytes)", len(b), len(header))
					}
				} else {
					b, err = m.Segment(ctx, token, k)
					if err == nil {
						served.Add(1)
						if what := wholeChunk(spec, header, b, k); what != "" {
							wrong.Add(1)
							t.Errorf("chunk %d was handed over wrong: %s", k, what)
						}
					}
				}
				cancel()
				switch {
				case err == nil, errors.Is(err, ErrSuperseded), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
				default:
					t.Errorf("a request failed in a way no player should see: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	// The storm is over. One last request, which nobody gives up, is
	// answered, and the converter is then where that request wants it.
	for _, k := range []int{17, last, 0} {
		b, err := m.Segment(bg, token, k)
		if err != nil {
			t.Fatalf("chunk %d after the storm: %v", k, err)
		}
		if what := wholeChunk(spec, header, b, k); what != "" {
			t.Errorf("chunk %d after the storm: %s", k, what)
		}
	}
	if n := m.Running(); n > 1 {
		t.Errorf("%d converters running for one viewer", n)
	}
	if pids, known := converters(); known && !settled(func() bool { pids, _ = converters(); return len(pids) <= 1 }) {
		t.Errorf("%d ffmpeg processes are alive for one viewer", len(pids))
	}
	if pace, n := m.Pace(token); math.IsNaN(pace) || math.IsInf(pace, 0) || pace < 0 || n < 0 || n > paceChunks {
		t.Errorf("after the storm the pace is %v over %d chunks", pace, n)
	}
	m.mu.Lock()
	starts := m.sessions[token].starts
	m.mu.Unlock()
	logMu.Lock()
	for _, line := range logged {
		if strings.Contains(line, "left the lattice") || strings.Contains(line, "expected form") {
			t.Errorf("logged in the storm: %s", line)
		}
	}
	logMu.Unlock()
	t.Logf("%d chunks handed over, %d of them wrong; ffmpeg was started %d times", served.Load(), wrong.Load(), starts)
	if served.Load() == 0 {
		t.Error("in all that, not one chunk was handed over")
	}
	nothingLeft(t, m, goroutines)
}

// slowly makes the converter read no faster than the film plays, so that a
// chunk is some seconds in the making and things can be made to happen
// while it is.
func slowly(m *Manager) { m.ReadRate, m.Burst = 1, 1 }

// busy waits until the session's converter is running and has not yet
// finished chunk n, and returns the session.
func busy(t *testing.T, m *Manager, token string, n int) *session {
	t.Helper()
	m.mu.Lock()
	s := m.sessions[token]
	m.mu.Unlock()
	if s == nil {
		t.Fatal("no such session")
	}
	if !eventually(func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return s.run != nil && s.run.alive() && s.run.cmd != nil
	}) {
		t.Fatal("no converter was started")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.run.next > n {
		t.Fatalf("chunk %d was finished before anything could be done while it was being made", n)
	}
	return s
}

// The player is closed, its owner signs out, or the program shuts down
// while a chunk of a converted picture is in the making and a request is
// waiting for it. The request is answered at once (nobody waits for a chunk
// that will never come), and nothing is left running or on the disk.
func TestClosingWhileAPictureIsBeingConverted(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longConvertSpec(t, ffmpeg, ffprobe)
	bg := context.Background()
	for _, closer := range []string{"the player is closed", "its owner signs out", "the program shuts down"} {
		for _, when := range []string{"as the converter starts", "with a chunk handed over and the next in the making"} {
			t.Run(closer+" "+when, func(t *testing.T) {
				goroutines := runtime.NumGoroutine()
				m, err := New(ffmpeg, filepath.Join(t.TempDir(), "chunks"))
				if err != nil {
					t.Fatal(err)
				}
				defer m.Close()
				slowly(m)
				token := m.Open(spec)
				ask := 20 // a jump: the converter starts in mid-film
				if when != "as the converter starts" {
					if _, err := m.Segment(bg, token, ask); err != nil {
						t.Fatal(err)
					}
					ask++
				}
				answer := make(chan error, 1)
				go func() {
					_, err := m.Segment(bg, token, ask)
					answer <- err
				}()
				s := busy(t, m, token, ask)
				time.Sleep(moment) // the request is waiting
				switch closer {
				case "the player is closed":
					m.Stop(token)
				case "its owner signs out":
					m.Drop("ann")
				case "the program shuts down":
					m.Close()
				}
				select {
				case err := <-answer:
					if !errors.Is(err, ErrNoSession) {
						t.Errorf("the request that was waiting was answered: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Errorf("the request that was waiting is waiting still, ten seconds after its session was closed")
				}
				m.mu.Lock()
				left := s.run != nil && s.run.alive() || len(m.live) != 0
				m.mu.Unlock()
				if left {
					t.Errorf("a converter is still running for the closed session")
				}
				if _, err := os.Stat(s.dir); err == nil {
					t.Errorf("the closed session's chunks are still on disk")
				}
				if pace, n := m.Pace(token); pace != 0 || n != 0 {
					t.Errorf("a closed session has a pace of %v over %d chunks", pace, n)
				}
				nothingLeft(t, m, goroutines)
			})
		}
	}
}

// A request for a chunk that is still being converted is given up by its
// player. It is answered at once, the converter carries on where it was,
// and whoever asks next gets the chunk from the same run.
func TestAConvertedRequestGivenUpWhileWaiting(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longConvertSpec(t, ffmpeg, ffprobe)
	goroutines := runtime.NumGoroutine()
	m, err := New(ffmpeg, filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	slowly(m)
	bg := context.Background()
	token := m.Open(spec)
	given, giveUp := context.WithCancel(bg)
	answer := make(chan error, 1)
	go func() {
		_, err := m.Segment(given, token, 20)
		answer <- err
	}()
	s := busy(t, m, token, 20)
	giveUp()
	select {
	case err := <-answer:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the request that was given up: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a request that was given up while it waited is waiting still")
	}
	m.mu.Lock()
	starts, at, alive := s.starts, s.run.first, s.run.alive()
	m.mu.Unlock()
	if starts != 1 || at != 20 || !alive {
		t.Errorf("after the request was given up: started %d times, at chunk %d, running %v; want once, 20, true", starts, at, alive)
	}
	header, err := m.Init(bg, token)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Segment(bg, token, 20)
	if err != nil {
		t.Fatalf("the chunk asked for again: %v", err)
	}
	if what := wholeChunk(spec, header, b, 20); what != "" {
		t.Errorf("the chunk asked for again: %s", what)
	}
	m.mu.Lock()
	starts = s.starts
	m.mu.Unlock()
	if starts != 1 {
		t.Errorf("ffmpeg was started %d times, want once: the chunk was in the making all along", starts)
	}
	nothingLeft(t, m, goroutines)
}

// The converter dies while it is making a chunk somebody is waiting for:
// the PC ran out of memory, or a cleaning tool ended it. It is started once
// more at that chunk and the viewer gets the chunk, whole.
func TestAConverterOfAPictureThatDiesInMidChunk(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longConvertSpec(t, ffmpeg, ffprobe)
	goroutines := runtime.NumGoroutine()
	m, err := New(ffmpeg, filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.Log = func(string, ...any) {}
	slowly(m)
	bg := context.Background()
	token := m.Open(spec)
	header, err := m.Init(bg, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Segment(bg, token, 0); err != nil {
		t.Fatal(err)
	}
	type got struct {
		b   []byte
		err error
	}
	answer := make(chan got, 1)
	go func() {
		b, err := m.Segment(bg, token, 1)
		answer <- got{b, err}
	}()
	s := busy(t, m, token, 1)
	time.Sleep(moment) // the request is waiting
	m.mu.Lock()
	victim := s.run.cmd.Process
	m.mu.Unlock()
	victim.Kill()
	select {
	case g := <-answer:
		if g.err != nil {
			t.Fatalf("the chunk that was being made when the converter died: %v", g.err)
		}
		if what := wholeChunk(spec, header, g.b, 1); what != "" {
			t.Errorf("the chunk that was being made when the converter died: %s", what)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("the request that was waiting when the converter died is waiting still")
	}
	m.mu.Lock()
	starts := s.starts
	m.mu.Unlock()
	if starts != 2 {
		t.Errorf("ffmpeg was started %d times, want twice (the film, and again after it died)", starts)
	}
	// And the film goes on from there.
	for k := 2; k < 4; k++ {
		b, err := m.Segment(bg, token, k)
		if err != nil {
			t.Fatalf("chunk %d after the converter was started again: %v", k, err)
		}
		if what := wholeChunk(spec, header, b, k); what != "" {
			t.Errorf("chunk %d after the converter was started again: %s", k, what)
		}
	}
	nothingLeft(t, m, goroutines)
}

// Films too short to be cut at all: one chunk, which is the whole film.
func TestAFilmShorterThanOneChunkIsConverted(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	dir := t.TempDir()
	for _, film := range []struct {
		name            string
		picture, sound  string
		frames          int
		soundAtLeastSec float64
	}{
		{"two seconds", "2", "2", 48, 1.9},
		{"half a second", "0.5", "0.5", 12, 0.4},
		{"one frame", "0.04", "0.5", 1, 0.4},
		{"one second of picture and eight of sound", "1", "8", 24, 7.9},
		{"three seconds and a bit", "3.4", "3.4", 82, 3.3},
	} {
		src := filepath.Join(dir, strings.ReplaceAll(film.name, " ", "-")+".mkv")
		out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration="+film.picture,
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration="+film.sound,
			"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", src).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: making the film: %v %s", film.name, err, out)
		}
		facts, err := ProbePicture(context.Background(), ffprobe, src)
		if err != nil {
			t.Errorf("%s: the facts about the picture: %v", film.name, err)
			continue
		}
		g, c := facts.Lattice()
		c.W, c.H = facts.Size(1080)
		c.Preset, c.MaxRateK = VeryFast, 10000
		if !g.Valid() || c.fits(g) != nil {
			t.Errorf("%s: no list to convert by: %+v, %v (facts %+v)", film.name, g, c.fits(g), facts)
			continue
		}
		if len(g.Cuts) != 1 {
			t.Errorf("%s: cut into %d chunks: %v", film.name, len(g.Cuts), g.Cuts)
			continue
		}
		for _, sound := range []string{"ac3", ""} {
			spec := Spec{Path: src, Grid: g, AudioTrack: 0, AudioCopy: sound != "", AudioCodec: sound, Convert: &c}
			m := manager(t, ffmpeg)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			token := m.Open(spec)
			list, err := m.Playlist(token)
			if err != nil || strings.Count(string(list), ".m4s") != 1 {
				t.Errorf("%s: the list: %v\n%s", film.name, err, list)
			}
			// The chunk first, as a player that resumes does, then the header.
			b, err := m.Segment(ctx, token, 0)
			if err != nil {
				t.Errorf("%s, sound %q: the only chunk: %v", film.name, sound, err)
				cancel()
				m.Close()
				continue
			}
			header, err := m.Init(ctx, token)
			if err != nil {
				t.Errorf("%s, sound %q: the header: %v", film.name, sound, err)
			}
			if what := wholeChunk(spec, header, b, 0); what != "" {
				t.Errorf("%s, sound %q: the only chunk: %s", film.name, sound, what)
			}
			if _, err := m.Segment(ctx, token, 1); !errors.Is(err, ErrPastEnd) {
				t.Errorf("%s, sound %q: a chunk past the end: %v", film.name, sound, err)
			}
			whole := joined(t, header, b)
			decodes(t, ffmpeg, whole)
			got := inspect(t, ffprobe, whole)
			if got["video"].Packets != fmt.Sprint(film.frames) {
				t.Errorf("%s, sound %q: %s frames of picture, the film has %d", film.name, sound, got["video"].Packets, film.frames)
			}
			// Sound comes in pieces of 32 ms (AC-3) or 21 ms (AAC).
			per := 0.032
			if sound == "" {
				per = 1024.0 / 48000
			}
			if secs := f(got["audio"].Packets) * per; secs < film.soundAtLeastSec {
				t.Errorf("%s, sound %q: %.2f seconds of sound, the film has %s", film.name, sound, secs, film.sound)
			}
			m.mu.Lock()
			starts := m.sessions[token].starts
			m.mu.Unlock()
			if starts != 1 {
				t.Errorf("%s, sound %q: ffmpeg was started %d times", film.name, sound, starts)
			}
			if pace, n := m.Pace(token); pace != 0 || n != 0 {
				t.Errorf("%s, sound %q: a film of one chunk has a pace of %v over %d chunks", film.name, sound, pace, n)
			}
			cancel()
			m.Close()
		}
	}
}

// Two viewers watch the same converted film at the same time, each from
// their own place. Each has a converter of their own and gets the film
// whole, and neither disturbs the other.
func TestTwoViewersOfOneConvertedFilm(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec, _ := convertSpec(t, ffprobe, convertFilm(t, ffmpeg, "film24"))
	goroutines := runtime.NumGoroutine()
	m, err := New(ffmpeg, filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	bg := context.Background()
	n := len(spec.Grid.Cuts)
	var wg sync.WaitGroup
	tokens := make([]string, 2)
	for v := range tokens {
		s := spec // the same film, the same way of converting it
		s.Owner = []string{"ann", "bob"}[v]
		tokens[v] = m.Open(s)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One from the top, the other from the middle and round again.
			// Each asks for a chunk first and the header after, as a player
			// that resumes does.
			var header []byte
			for i := 0; i < n; i++ {
				k := (i + v*n/2) % n
				b, err := m.Segment(bg, tokens[v], k)
				if err != nil {
					t.Errorf("viewer %d: chunk %d: %v", v, k, err)
					return
				}
				if header == nil {
					if header, err = m.Init(bg, tokens[v]); err != nil {
						t.Errorf("viewer %d: the header: %v", v, err)
						return
					}
				}
				if what := wholeChunk(spec, header, b, k); what != "" {
					t.Errorf("viewer %d: chunk %d: %s", v, k, what)
				}
			}
		}()
	}
	wg.Wait()
	m.mu.Lock()
	a, b := m.sessions[tokens[0]], m.sessions[tokens[1]]
	startsA, startsB, apart := a.starts, b.starts, a.dir != b.dir
	m.mu.Unlock()
	// The second began in the middle (one start) and went round to the top
	// (another).
	if startsA != 1 || startsB != 2 || !apart {
		t.Errorf("ffmpeg was started %d times for the first viewer and %d for the second, want 1 and 2; folders apart: %v", startsA, startsB, apart)
	}
	nothingLeft(t, m, goroutines)
}

// The idle check stops a converter nobody has asked anything of for a
// while. A viewer of a converted film who presses play at that very moment
// gets the film: what is started for them is not swept away.
func TestPressingPlayOnAConvertedFilmWhileTheIdleCheckIsStopping(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longConvertSpec(t, ffmpeg, ffprobe)
	goroutines := runtime.NumGoroutine()
	m, err := New(ffmpeg, filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.Log = func(format string, args ...any) { t.Errorf("logged as a failure: "+format, args...) }
	m.ConvertAhead, m.IdleStop = 6, 50*time.Millisecond
	bg := context.Background()
	token := m.Open(spec)
	header, err := m.Init(bg, token)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		if _, err := m.Segment(bg, token, 10*round); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		reached, resume := gap(t, m)
		arrive(t, reached) // within a second or so: the idle check is stopping ffmpeg
		play := make(chan error, 1)
		go func() {
			b, err := m.Segment(bg, token, 10*round+1)
			if err == nil {
				if what := wholeChunk(spec, header, b, 10*round+1); what != "" {
					err = errors.New(what)
				}
			}
			play <- err
		}()
		time.Sleep(moment)
		resume()
		select {
		case err := <-play:
			if err != nil {
				t.Fatalf("round %d: pressing play as the idle check stopped the converter: %v", round, err)
			}
		case <-time.After(40 * time.Second):
			t.Fatalf("round %d: the request made as the idle check stopped the converter is waiting still", round)
		}
	}
	// Left alone, the converter is stopped and its chunks are removed.
	if !eventually(func() bool { return m.Running() == 0 }) {
		t.Error("the converter kept running after the viewer went away")
	}
	m.mu.Lock()
	dir := m.sessions[token].dir
	m.mu.Unlock()
	if !settled(func() bool { left, _ := filepath.Glob(filepath.Join(dir, "*", "*")); return len(left) == 0 }) {
		left, _ := filepath.Glob(filepath.Join(dir, "*", "*"))
		t.Errorf("chunks left on disk after the viewer went away: %d files", len(left))
	}
	nothingLeft(t, m, goroutines)
}

// What ffmpeg says when it cannot convert a film goes into the answer and
// the log. For a converted picture too, only the film's own name may be in
// it, never the folder.
func TestWhatTheConverterOfAPictureSaysNamesNoFolder(t *testing.T) {
	ffmpeg, _ := tools(t)
	g, c := Lattice(0, 24983, 25025, 24000, 1001)
	c.W, c.H, c.MaxRateK, c.LeadMs = 320, 180, 2500, 2000
	folder := filepath.Join(t.TempDir(), "192.168.1.20", "Household Videos")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(folder, "Broken Film (2019).mkv")
	if err := os.WriteFile(broken, []byte("this is not a film"), 0o600); err != nil {
		t.Fatal(err)
	}
	for what, film := range map[string]string{
		"a film that is not there": filepath.Join(folder, "Test Film (2020).mkv"),
		"a file that is no film":   broken,
	} {
		for _, at := range []int{0, 5} {
			m := manager(t, ffmpeg)
			var mu sync.Mutex
			var logged strings.Builder
			m.Log = func(format string, args ...any) {
				mu.Lock()
				defer mu.Unlock()
				fmt.Fprintf(&logged, format+"\n", args...)
			}
			token := m.Open(Spec{Path: film, Grid: g, AudioTrack: 0, Convert: &c})
			_, err := m.Segment(context.Background(), token, at)
			_, err2 := m.Init(context.Background(), token)
			if err == nil || err2 == nil {
				t.Fatalf("%s was converted", what)
			}
			m.Close()
			mu.Lock()
			said := map[string]string{"the answer": err.Error(), "the answer about the header": err2.Error(), "the log": logged.String()}
			mu.Unlock()
			for where, text := range said {
				if strings.Contains(text, "Household Videos") || strings.Contains(text, "192.168.1.20") || strings.Contains(text, filepath.Dir(folder)) || strings.Contains(text, m.root) {
					t.Errorf("%s, from chunk %d: %s names a folder:\n%s", what, at, where, text)
				}
				if !strings.Contains(text, filepath.Base(film)) {
					t.Errorf("%s, from chunk %d: %s should still say which film it is about:\n%s", what, at, where, text)
				}
			}
		}
	}
}

// Everything at once, as in TestHammeredFromAllSidesNothingIsLeftBehind,
// with films whose picture is converted: jumps given up at once, players
// closed and opened again, sign-outs, and the idle check stopping whatever
// is left alone for a moment. Whatever is handed over must be whole and in
// its place, and afterwards nothing may be left.
func TestConvertedFilmsHammeredFromAllSides(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longConvertSpec(t, ffmpeg, ffprobe)
	goroutines := runtime.NumGoroutine()
	root := filepath.Join(t.TempDir(), "chunks")
	m, err := New(ffmpeg, root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var logMu sync.Mutex
	var logged []string
	m.Log = func(format string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, fmt.Sprintf(format, a...))
	}
	m.ConvertAhead, m.ConvertBehind, m.IdleStop = 9, 6, 30*time.Millisecond
	bg := context.Background()
	// Every run of a film writes the same header, whichever session it is
	// for: the one to read the chunks by.
	first := m.Open(spec)
	header, err := m.Init(bg, first)
	if err != nil {
		t.Fatal(err)
	}
	m.Stop(first)
	owners := []string{"ann", "bob"}
	var mu sync.Mutex
	tokens := make([]string, 4) // two players for each sign-in
	open := func(i int) {
		s := spec
		s.Owner = owners[i%len(owners)]
		token := m.Open(s)
		mu.Lock()
		tokens[i] = token
		mu.Unlock()
	}
	for i := range tokens {
		open(i)
	}
	last := len(spec.Grid.Cuts) - 1
	until := time.Now().Add(8 * time.Second)
	var wg sync.WaitGroup
	var served atomic.Int64
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 7))
			at := 0
			for time.Now().Before(until) {
				i := rng.Intn(len(tokens))
				mu.Lock()
				token := tokens[i]
				mu.Unlock()
				patience := time.Duration(rng.Intn(200)) * time.Millisecond
				if rng.Intn(3) == 0 {
					patience = 20 * time.Second
				}
				ctx, cancel := context.WithTimeout(bg, patience)
				var b []byte
				var err error
				k := -1
				switch p := rng.Intn(100); {
				case p < 35:
					k = rng.Intn(last + 2) // one past the end now and then
					b, err = m.Segment(ctx, token, k)
				case p < 65:
					k = min(at+1, last) // watching on
					b, err = m.Segment(ctx, token, k)
				case p < 73:
					b, err = m.Init(ctx, token)
					if err == nil && !bytes.Equal(b, header) {
						t.Errorf("a header that differs from the first one was handed over (%d and %d bytes)", len(b), len(header))
					}
				case p < 78:
					b, err = m.Playlist(token)
				case p < 88:
					m.Stop(token)
					open(i)
				case p < 91:
					m.Drop(owners[rng.Intn(len(owners))])
				default:
					m.Pace(token)
					open(i)
				}
				cancel()
				if k >= 0 && err == nil {
					at = k
					if what := wholeChunk(spec, header, b, k); what != "" {
						t.Errorf("chunk %d was handed over wrong: %s", k, what)
					}
				}
				switch {
				case err == nil && b != nil:
					served.Add(1)
				case err == nil, errors.Is(err, ErrNoSession), errors.Is(err, ErrPastEnd), errors.Is(err, ErrSuperseded),
					errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
				default:
					t.Errorf("a request failed in a way no player should see: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	stray := func() (n int) {
		m.mu.Lock()
		defer m.mu.Unlock()
		held := map[*run]bool{}
		for _, s := range m.sessions {
			held[s.run] = true
		}
		for r := range m.live {
			if !held[r] {
				n++
			}
		}
		return n
	}
	if !settled(func() bool { return stray() == 0 }) {
		t.Errorf("%d converters are running that no open session holds", stray())
	}
	if pids, known := converters(); known && !settled(func() bool { pids, _ = converters(); return len(pids) <= m.Running() }) {
		t.Errorf("%d ffmpeg processes are alive, the manager knows of %d", len(pids), m.Running())
	}
	logMu.Lock()
	for _, line := range logged {
		t.Errorf("logged as a failure: %s", line)
	}
	logMu.Unlock()
	if served.Load() == 0 {
		t.Error("in all that, not one request was answered")
	}
	t.Logf("%d requests answered", served.Load())
	nothingLeft(t, m, goroutines)
}

// A film that has no sound at all, or whose sound the viewer's device is
// not given: the picture is converted alone, and every chunk is the same
// whether it is played up to or jumped to.
func TestAConvertedPictureWithoutSound(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec, facts := convertSpec(t, ffprobe, convertFilm(t, ffmpeg, "film24"))
	spec.AudioTrack = -1
	m := manager(t, ffmpeg)
	ctx := context.Background()
	token := m.Open(spec)
	header, err := m.Init(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	var through [][]byte
	for k := range spec.Grid.Cuts {
		b, err := m.Segment(ctx, token, k)
		if err != nil {
			t.Fatalf("chunk %d played through: %v", k, err)
		}
		if what := wholeChunk(spec, header, b, k); what != "" {
			t.Errorf("chunk %d played through: %s", k, what)
		}
		through = append(through, b)
	}
	whole := joined(t, append([][]byte{header}, through...)...)
	decodes(t, ffmpeg, whole)
	got := inspect(t, ffprobe, whole)
	c := spec.Convert
	_, tick0 := anchor(facts.StartMs, c.Num, c.Den)
	if ticks := divRound(facts.LastMs*c.Num, c.Den*1000) - tick0 + 1; got["video"].Packets != fmt.Sprint(ticks) || got["audio"].Codec != "" {
		t.Errorf("played through: %s frames of picture (want %d) and sound of kind %q (want none)", got["video"].Packets, ticks, got["audio"].Codec)
	}
	for k := range spec.Grid.Cuts {
		fresh := m.Open(spec)
		b, err := m.Segment(ctx, fresh, k)
		if err != nil {
			t.Errorf("chunk %d jumped to: %v", k, err)
			m.Stop(fresh)
			continue
		}
		h2, err := m.Init(ctx, fresh)
		if err != nil || !bytes.Equal(h2, header) {
			t.Errorf("the header written by a run that began at chunk %d differs from the one written from the top (%v)", k, err)
		}
		if what := wholeChunk(spec, header, b, k); what != "" {
			t.Errorf("chunk %d jumped to: %s", k, what)
		}
		a, j := inspect(t, ffprobe, joined(t, header, through[k])), inspect(t, ffprobe, joined(t, header, b))
		if a["video"].Packets != j["video"].Packets {
			t.Errorf("chunk %d: %s frames played through, %s jumped to", k, a["video"].Packets, j["video"].Packets)
		}
		m.mu.Lock()
		starts := m.sessions[fresh].starts
		m.mu.Unlock()
		if starts != 1 {
			t.Errorf("chunk %d jumped to: ffmpeg was started %d times", k, starts)
		}
		m.Stop(fresh)
	}
}
