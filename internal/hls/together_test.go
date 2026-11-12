package hls

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests are about requests that arrive on top of each other: a jump
// while another jump is still restarting ffmpeg, a player closed in the
// middle of a jump, and many of everything at once. They use the "long"
// film: two minutes, twenty chunks.

// midRestart waits for the moment a request has taken the session's ffmpeg
// away to stop it and is waiting, without the lock, for it to be gone.
func midRestart(t *testing.T, m *Manager, s *session) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		stopping := s.run == nil
		m.mu.Unlock()
		if stopping {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the jump never stopped the running converter")
		}
	}
}

// converters lists the ffmpeg processes this test program has started and
// that are still alive, where the system offers a way to ask (Linux).
func converters() (pids []int, known bool) {
	if runtime.GOOS != "linux" {
		return nil, false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// "pid (name) state parent ...": the name may hold spaces.
		text := string(b)
		open, shut := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
		if open < 0 || shut < open {
			continue
		}
		rest := strings.Fields(text[shut+1:])
		if len(rest) < 2 || rest[0] == "Z" || !strings.Contains(text[open:shut], "ffmpeg") {
			continue
		}
		if parent, _ := strconv.Atoi(rest[1]); parent == os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids, true
}

// settled waits a moment for ok to become true: a process that has been
// told to stop takes a few thousandths of a second to be gone.
func settled(ok func() bool) bool {
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return ok()
}

func longSpec(t *testing.T, ffmpeg, ffprobe, owner string) Spec {
	t.Helper()
	src := source(t, ffmpeg, "long")
	g := grid(t, ffprobe, src, 120000)
	if len(g.Cuts) != 20 {
		t.Fatalf("the long film was cut into %d chunks, want 20", len(g.Cuts))
	}
	return Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: 0, AudioCopy: true, Owner: owner}
}

// A viewer drags the timeline: the player asks for one place, gives that up
// and asks for another a moment later. The second request arrives while the
// first is still stopping the old ffmpeg. The second is the one the player
// is waiting for, so it is the one that must be answered.
func TestASecondJumpDuringARestartIsAnswered(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longSpec(t, ffmpeg, ffprobe, "ann")
	m := manager(t, ffmpeg)
	m.Ahead = 2 // the converter stays near the viewer, so every jump is a restart
	bg := context.Background()
	for round := 0; round < 12; round++ {
		token := m.Open(spec)
		if _, err := m.Segment(bg, token, 0); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		s := m.sessions[token]
		m.mu.Unlock()
		given, giveUp := context.WithCancel(bg)
		first := make(chan error, 1)
		go func() {
			_, err := m.Segment(given, token, 8)
			first <- err
		}()
		midRestart(t, m, s)
		giveUp()
		if _, err := m.Segment(bg, token, 14); err != nil {
			t.Fatalf("round %d: the jump the player is waiting for: %v", round, err)
		}
		<-first
		m.mu.Lock()
		at, starts := -1, s.starts
		if s.run != nil {
			at = s.run.first
		}
		m.mu.Unlock()
		if at != 14 {
			t.Errorf("round %d: the converter ended up at chunk %d, the player is at 14", round, at)
		}
		if starts > 3 {
			t.Errorf("round %d: ffmpeg was started %d times for one start and two jumps", round, starts)
		}
		if n := m.Running(); n != 1 {
			t.Errorf("round %d: %d converters running for one viewer", round, n)
		}
		m.Stop(token)
		if pids, known := converters(); known && !settled(func() bool { pids, _ = converters(); return len(pids) == 0 }) {
			t.Fatalf("round %d: ffmpeg still running after the player was closed: %v", round, pids)
		}
	}
}

// The idle check stops a converter nobody has asked anything of for a
// while and deletes its chunks. A viewer who presses play at that very
// moment gets the film: what is started for them is not swept away.
func TestPressingPlayWhileTheIdleCheckIsStopping(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longSpec(t, ffmpeg, ffprobe, "ann")
	m := manager(t, ffmpeg)
	m.Log = func(format string, args ...any) { t.Errorf("logged as a failure: "+format, args...) }
	m.Ahead, m.IdleStop = 2, 50*time.Millisecond
	bg := context.Background()
	token := m.Open(spec)
	m.mu.Lock()
	s := m.sessions[token]
	m.mu.Unlock()
	for round := 0; round < 3; round++ {
		if _, err := m.Segment(bg, token, 0); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		midRestart(t, m, s) // within a second or so: the idle check
		if b, err := m.Segment(bg, token, 1); err != nil || len(b) == 0 {
			t.Fatalf("round %d: pressing play as the idle check stopped the converter: %v", round, err)
		}
	}
}

// A request that its player has already given up must not start ffmpeg or
// move it: the converter is where the player's next request needs it.
func TestARequestThatWasGivenUpDoesNotMoveTheConverter(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	m := manager(t, ffmpeg)
	m.Ahead = 2
	token := m.Open(longSpec(t, ffmpeg, ffprobe, "ann"))
	bg := context.Background()
	givenUp, giveUp := context.WithCancel(bg)
	giveUp()
	state := func() (starts, at int, running bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		s := m.sessions[token]
		if s.run == nil {
			return s.starts, -1, false
		}
		return s.starts, s.run.first, s.run.alive()
	}
	if _, err := m.Segment(givenUp, token, 5); !errors.Is(err, context.Canceled) {
		t.Errorf("a request that was given up before it began: %v", err)
	}
	if starts, _, _ := state(); starts != 0 {
		t.Errorf("ffmpeg was started %d times for a request nobody was waiting for", starts)
	}
	if _, err := m.Segment(bg, token, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Segment(givenUp, token, 15); !errors.Is(err, context.Canceled) {
		t.Errorf("a jump that was given up: %v", err)
	}
	if starts, at, running := state(); starts != 1 || at != 0 || !running {
		t.Errorf("after a jump that was given up: started %d times, at chunk %d, running %v; want once, 0, true", starts, at, running)
	}
	// A chunk that is ready is handed over whoever asks.
	if b, err := m.Segment(givenUp, token, 0); err != nil || len(b) == 0 {
		t.Errorf("a chunk that was ready: %v", err)
	}
}

// Everything at once for a few seconds: jumps all over the film, most of
// them given up at once, players closed and opened again, sign-outs, and
// the idle check stopping whatever is left alone. Run with -race, this also
// checks that no two of them touch the same thing at the same moment.
// Afterwards nothing may be left: no ffmpeg, no goroutine, no file.
func TestHammeredFromAllSidesNothingIsLeftBehind(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longSpec(t, ffmpeg, ffprobe, "")
	goroutines := runtime.NumGoroutine()
	root := filepath.Join(t.TempDir(), "chunks")
	m, err := New(ffmpeg, root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.Ahead, m.IdleStop = 3, 30*time.Millisecond
	owners := []string{"ann", "bob", "cy"}
	var mu sync.Mutex
	tokens := make([]string, 6) // two players for each sign-in
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
	bg := context.Background()
	until := time.Now().Add(5 * time.Second)
	var wg sync.WaitGroup
	var served atomic.Int64
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for time.Now().Before(until) {
				i := rng.Intn(len(tokens))
				mu.Lock()
				token := tokens[i]
				mu.Unlock()
				ctx, cancel := context.WithTimeout(bg, time.Duration(rng.Intn(150))*time.Millisecond)
				var b []byte
				var err error
				switch p := rng.Intn(100); {
				case p < 60:
					b, err = m.Segment(ctx, token, rng.Intn(21)) // one past the end now and then
				case p < 70:
					b, err = m.Init(ctx, token)
				case p < 76:
					b, err = m.Playlist(token)
				case p < 86:
					m.Stop(token)
					open(i)
				case p < 90:
					m.Drop(owners[rng.Intn(len(owners))])
				default:
					open(i)
				}
				cancel()
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
	// Whatever is running now belongs to a session that is still open: a
	// run that no session holds is one that nothing could ever stop. (One
	// that the idle check is stopping at this moment is gone in an instant.)
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
	if served.Load() == 0 {
		t.Error("in all that, not one request was answered")
	}
	m.Close()
	m.mu.Lock()
	left := len(m.live)
	m.mu.Unlock()
	if left != 0 {
		t.Errorf("%d converters outlived the closing of the manager", left)
	}
	if pids, known := converters(); known && len(pids) != 0 {
		t.Errorf("ffmpeg still running after the manager was closed: %v", pids)
	}
	if _, err := os.Stat(root); err == nil {
		t.Error("closing left the chunk folder behind")
	}
	if !settled(func() bool { return runtime.NumGoroutine() <= goroutines }) {
		t.Errorf("%d goroutines before, %d after the manager was closed", goroutines, runtime.NumGoroutine())
	}
	// Closed is closed: nothing can be opened or started afterwards.
	late := m.Open(spec)
	if _, err := m.Segment(bg, late, 0); !errors.Is(err, ErrNoSession) || m.Sessions() != 0 || m.Running() != 0 {
		t.Errorf("after closing: %v, %d sessions, %d running", err, m.Sessions(), m.Running())
	}
	t.Logf("%d requests answered", served.Load())
}

// The player is closed, or its owner signs out, or the program shuts down,
// at the very moment a jump is restarting ffmpeg. Nothing may be started
// for a session that is no longer there: nobody could ever stop it.
func TestClosingDuringAJumpLeavesNothingRunning(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec := longSpec(t, ffmpeg, ffprobe, "ann")
	bg := context.Background()
	for _, closer := range []string{"the player is closed", "its owner signs out", "a seventh film is opened", "the program shuts down"} {
		t.Run(closer, func(t *testing.T) {
			for round := 0; round < 4; round++ {
				m := manager(t, ffmpeg)
				m.Ahead = 2
				token := m.Open(spec)
				if _, err := m.Segment(bg, token, 0); err != nil {
					t.Fatal(err)
				}
				m.mu.Lock()
				s := m.sessions[token]
				m.mu.Unlock()
				jump := make(chan error, 1)
				go func() {
					_, err := m.Segment(bg, token, 10)
					jump <- err
				}()
				midRestart(t, m, s)
				switch closer {
				case "the player is closed":
					m.Stop(token)
				case "its owner signs out":
					m.Drop("ann")
				case "a seventh film is opened":
					for i := 0; i < maxPerOwner; i++ {
						m.Open(spec)
					}
				case "the program shuts down":
					m.Close()
				}
				if err := <-jump; err == nil {
					t.Errorf("the jump was answered though its session was closed")
				}
				left := func() bool {
					m.mu.Lock()
					defer m.mu.Unlock()
					return s.run != nil && s.run.alive()
				}
				if settled(func() bool { return !left() }); left() {
					t.Errorf("a converter was started for a session that had been closed, and nothing can stop it")
				}
				if m.Owner(token) != "" || m.Running() != 0 {
					t.Errorf("after closing: owner %q, %d running", m.Owner(token), m.Running())
				}
				if _, err := os.Stat(s.dir); err == nil {
					t.Errorf("the closed session's chunks are still on disk")
				}
				m.Close()
				if pids, known := converters(); known && !settled(func() bool { pids, _ = converters(); return len(pids) == 0 }) {
					t.Fatalf("ffmpeg still running after the manager was closed: %v", pids)
				}
			}
		})
	}
}
