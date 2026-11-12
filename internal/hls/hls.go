// Package hls repackages a file into short chunks as it is watched, and
// when asked converts its sound on the way. It runs ffmpeg, a separate
// program, and never more of it than the viewer needs:
//
//   - The viewer's player is given the whole film's list of chunks at once,
//     so it can show the full timeline and jump anywhere.
//   - ffmpeg is started at the chunk the player asks for and runs a little
//     ahead of it. A jump to somewhere it has not reached restarts it there.
//   - ffmpeg writes one continuous stream. This package cuts it into the
//     chunks the list promised, at times it decided beforehand (see Grid),
//     so a chunk holds the same part of the film however ffmpeg got there.
//   - When nobody has asked for anything for a while, ffmpeg is stopped and
//     its chunks deleted. Asking again starts it again.
//
// Every ffmpeg command line is built here from typed values. Nothing a
// client sends is ever part of one.
package hls

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SegmentSeconds is how long each chunk is meant to be. Chunks are cut at
// the picture's own key frames, so real ones run a little over or under.
const SegmentSeconds = 6

// clockShiftMs is added to every time ffmpeg writes. Sound often starts a
// few hundredths of a second "before zero", which the chunk format cannot
// express; shifting everything by one second keeps all times positive.
// Players only look at the differences, so nobody sees it.
const clockShiftMs = 1000

// maxPerOwner is how many films one sign-in may hold open at once.
const maxPerOwner = 6

// Spec says what to produce. It is filled in by the server from its own
// database; none of it comes from a client.
type Spec struct {
	Path       string // the file, as the server itself found it
	Grid       Grid   // where the chunks are cut
	VideoCodec string // as ffprobe names it; "hevc" needs a tag Apple devices insist on
	AudioTrack int    // which sound track, counted from 0; -1 for none
	AudioCopy  bool   // pass the sound through untouched, or convert it to stereo AAC
	Owner      string // the sign-in this session belongs to
}

// Manager owns every running conversion.
type Manager struct {
	ffmpeg string
	root   string
	// IdleStop is how long after the last request ffmpeg is stopped.
	// Forget is how long after it a session is forgotten altogether.
	IdleStop, Forget time.Duration
	// Ahead is how many chunks past the last one asked for ffmpeg may get
	// before it is made to wait, and Behind how many already-watched chunks
	// are kept. Together they bound the disk space one viewer uses.
	Ahead, Behind int
	// ReadRate is how many times faster than playback ffmpeg may read, and
	// Burst how many seconds it may read at full speed first.
	ReadRate, Burst int
	Log             func(format string, args ...any)

	mu       sync.Mutex
	sessions map[string]*session
	stop     chan struct{}
	closing  sync.Once
	done     sync.WaitGroup
}

type session struct {
	spec    Spec
	dir     string
	gen     int // each ffmpeg run writes into its own folder
	run     *run
	lastReq time.Time
	lastSeg int
	starts  int
}

// run is one ffmpeg process and the chunks cut from its output.
type run struct {
	cmd    *exec.Cmd
	dir    string
	first  int           // the chunk this run was started for
	low    int           // the lowest chunk still on disk
	next   int           // chunks low..next-1 are complete
	header bool          // the header is complete
	ended  bool          // ffmpeg reached the end of the film
	stop   chan struct{} // closed to make the reader give up
	exited chan struct{} // closed when ffmpeg is gone and its output read
	stderr *tail
}

func (r *run) alive() bool {
	select {
	case <-r.exited:
		return false
	default:
		return true
	}
}

func (r *run) file(n int) string {
	if n < 0 {
		return filepath.Join(r.dir, "init.mp4")
	}
	return filepath.Join(r.dir, "seg-"+strconv.Itoa(n)+".m4s")
}

// New prepares a manager that keeps its chunks under root. Whatever an
// earlier run of the program left there is removed.
func New(ffmpeg, root string) (*Manager, error) {
	if ffmpeg == "" {
		return nil, errors.New("ffmpeg is not available")
	}
	if err := os.RemoveAll(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{ffmpeg: ffmpeg, root: root, IdleStop: 75 * time.Second, Forget: 12 * time.Hour,
		Ahead: 50, Behind: 20, ReadRate: 8, Burst: 60,
		Log: func(string, ...any) {}, sessions: map[string]*session{}, stop: make(chan struct{})}
	m.done.Add(1)
	go m.janitor()
	return m, nil
}

// Close stops every conversion and removes every chunk.
func (m *Manager) Close() {
	m.closing.Do(func() {
		close(m.stop)
		m.done.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, s := range m.sessions {
			m.halt(s)
		}
		m.sessions = map[string]*session{}
		os.RemoveAll(m.root)
	})
}

// Open registers a session and returns its token: a random value that is
// the only way to reach the session's chunks.
func (m *Manager) Open(spec Spec) string {
	if !spec.Grid.Valid() {
		spec.Grid = Uniform(spec.Grid.EndMs - spec.Grid.StartMs)
	}
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	m.mu.Lock()
	defer m.mu.Unlock()
	// One sign-in watches one film at a time, give or take a tab or two.
	// Beyond a handful, the one left alone longest is closed.
	for {
		var oldest string
		n := 0
		for t, s := range m.sessions {
			if s.spec.Owner == spec.Owner {
				n++
				if oldest == "" || s.lastReq.Before(m.sessions[oldest].lastReq) {
					oldest = t
				}
			}
		}
		if n < maxPerOwner {
			break
		}
		s := m.sessions[oldest]
		m.halt(s)
		os.RemoveAll(s.dir)
		delete(m.sessions, oldest)
	}
	m.sessions[token] = &session{spec: spec, dir: filepath.Join(m.root, token), lastReq: time.Now()}
	return token
}

// Owner returns who a session belongs to, or "" if there is none.
func (m *Manager) Owner(token string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[token]; s != nil {
		return s.spec.Owner
	}
	return ""
}

// Running reports how many ffmpeg processes are alive.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sessions {
		if s.run != nil && s.run.alive() {
			n++
		}
	}
	return n
}

// Sessions reports how many films are open for conversion, whether or not
// ffmpeg is running for them at this moment.
func (m *Manager) Sessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Drop ends a session at once: used when its owner signs out.
func (m *Manager) Drop(owner string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for token, s := range m.sessions {
		if s.spec.Owner == owner {
			m.halt(s)
			os.RemoveAll(s.dir)
			delete(m.sessions, token)
		}
	}
}

// Stop ends one session at once: used when its viewer closes the player.
func (m *Manager) Stop(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[token]; s != nil {
		m.halt(s)
		os.RemoveAll(s.dir)
		delete(m.sessions, token)
	}
}

// ErrNoSession means the token is unknown or has been forgotten.
var ErrNoSession = errors.New("no such session")

// ErrPastEnd means a chunk beyond the end of the film was asked for.
var ErrPastEnd = errors.New("past the end")

// ErrSuperseded means that while this request waited, a newer one moved the
// converter somewhere else in the film. The player has moved on too.
var ErrSuperseded = errors.New("a newer request moved the converter elsewhere")

// Playlist returns the list of every chunk of the film.
func (m *Manager) Playlist(token string) ([]byte, error) {
	m.mu.Lock()
	s := m.sessions[token]
	if s != nil {
		s.lastReq = time.Now()
	}
	m.mu.Unlock()
	if s == nil {
		return nil, ErrNoSession
	}
	g := s.spec.Grid
	longest := 0.0
	for k := range g.Cuts {
		longest = max(longest, g.seconds(k))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-MAP:URI=\"init.mp4\"\n", int(longest+0.999))
	for k := range g.Cuts {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg-%d.m4s\n", g.seconds(k), k)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return []byte(b.String()), nil
}

// Init returns the header every chunk needs.
func (m *Manager) Init(ctx context.Context, token string) ([]byte, error) {
	return m.wait(ctx, token, -1)
}

// Segment returns chunk n, starting or restarting ffmpeg if that is what
// it takes, and waiting until the chunk is complete.
func (m *Manager) Segment(ctx context.Context, token string, n int) ([]byte, error) {
	if n < 0 {
		return nil, ErrPastEnd
	}
	return m.wait(ctx, token, n)
}

// reach is how many chunks past the one being written it is quicker to
// wait for than to restart ffmpeg at: about two seconds' worth of its work.
func (m *Manager) reach() int {
	return max(1, 2*m.ReadRate/SegmentSeconds)
}

func (m *Manager) wait(ctx context.Context, token string, n int) ([]byte, error) {
	deadline := time.Now().Add(30 * time.Second)
	launched := false
	for {
		m.mu.Lock()
		s := m.sessions[token]
		if s == nil {
			m.mu.Unlock()
			return nil, ErrNoSession
		}
		s.lastReq = time.Now()
		if n >= len(s.spec.Grid.Cuts) {
			m.mu.Unlock()
			return nil, ErrPastEnd
		}
		if n >= 0 {
			s.lastSeg = n
		}
		r := s.run
		alive := r != nil && r.alive()
		if r != nil && (n < 0 && r.header || n >= r.low && n < r.next) {
			p := r.file(n)
			m.mu.Unlock()
			// Read outside the lock. If a restart removed the file in the
			// meantime, go round again.
			if b, err := os.ReadFile(p); err == nil {
				return b, nil
			}
			m.mu.Lock()
		}
		startAt := -1
		switch {
		case n < 0:
			// The header was asked for and nothing is running: start at the
			// top, or where the stopped run began.
			if r == nil {
				startAt = 0
			} else if !alive && !r.header {
				startAt = r.first
			}
		case r == nil || n < r.low || n >= r.next+m.reach() || !alive && !r.ended:
			// Not started, behind this run, too far ahead of it, or the run
			// was stopped: start ffmpeg at this chunk.
			startAt = n
		case !alive && n >= r.next:
			// The run finished and this chunk never appeared: the film is
			// shorter than its stated length.
			m.mu.Unlock()
			return nil, ErrPastEnd
		}
		if startAt >= 0 {
			if launched {
				// This request already started ffmpeg once. Either it failed,
				// or another request has since moved it.
				var err error = ErrSuperseded
				if r != nil && !alive && r.first == startAt {
					err = fmt.Errorf("the converter stopped: %s", r.stderr.String())
				}
				m.mu.Unlock()
				return nil, err
			}
			launched = true
			if err := m.launch(s, startAt); err != nil {
				m.mu.Unlock()
				return nil, err
			}
		}
		var exited chan struct{}
		if s.run != nil {
			exited = s.run.exited
		}
		m.mu.Unlock()
		if time.Now().After(deadline) {
			return nil, errors.New("the converter did not produce this part in time")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-exited:
			time.Sleep(20 * time.Millisecond)
		case <-time.After(30 * time.Millisecond):
		}
	}
}

// args builds the ffmpeg command line for one run.
func args(spec Spec, startSeg int, readRate, burst int) []string {
	a := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if startSeg > 0 && startSeg < len(spec.Grid.Cuts) {
		// Start a little before the chunk, which lands ffmpeg one key frame
		// early: the sound around the cut is then complete, and what comes
		// before the chunk is thrown away as it is read.
		if at := spec.Grid.Cuts[startSeg] - spec.Grid.StartMs - 100; at > 0 {
			a = append(a, "-ss", fmt.Sprintf("%d.%03d", at/1000, at%1000), "-noaccurate_seek")
		}
	}
	// Read a little faster than it plays, after a quick first burst, so
	// that playback starts at once and the disk is not filled with a film
	// nobody may finish.
	a = append(a, "-readrate", strconv.Itoa(readRate), "-readrate_initial_burst", strconv.Itoa(burst), "-i", spec.Path, "-map", "0:v:0", "-c:v", "copy")
	if spec.VideoCodec == "hevc" {
		a = append(a, "-tag:v", "hvc1")
	}
	if spec.AudioTrack >= 0 {
		a = append(a, "-map", "0:a:"+strconv.Itoa(spec.AudioTrack))
		if spec.AudioCopy {
			a = append(a, "-c:a", "copy")
		} else {
			a = append(a, "-c:a", "aac", "-ac", "2", "-b:a", "192k")
		}
	}
	// Keep the file's own times (so every run agrees on them), write a new
	// fragment at every key frame, and send it all to this program.
	return append(a,
		"-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1",
		"-copyts", "-avoid_negative_ts", "disabled", "-output_ts_offset", fmt.Sprintf("%d.%03d", clockShiftMs/1000, clockShiftMs%1000),
		"-max_muxing_queue_size", "2048",
		"-f", "mp4", "-movflags", "+frag_keyframe+delay_moov+default_base_moof+frag_discont", "pipe:1")
}

// launch starts a fresh run at startSeg. The caller holds the lock.
func (m *Manager) launch(s *session, startSeg int) error {
	m.halt(s)
	os.RemoveAll(s.dir) // one run's chunks are never mixed with another's
	s.gen++
	r := &run{dir: filepath.Join(s.dir, "run-"+strconv.Itoa(s.gen)), first: startSeg, low: startSeg, next: startSeg,
		stop: make(chan struct{}), exited: make(chan struct{}), stderr: &tail{}}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	cmd := exec.Command(m.ffmpeg, args(s.spec, startSeg, m.ReadRate, m.Burst)...)
	hideWindow(cmd)
	cmd.Stderr = r.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("the converter could not be started: %w", err)
	}
	r.cmd = cmd
	s.run = r
	s.starts++
	go m.pump(s, r, out)
	return nil
}

// pump reads one run's output to its end and cuts it into chunks.
func (m *Manager) pump(s *session, r *run, out io.Reader) {
	err := m.slice(s, r, out)
	if err != nil {
		r.cmd.Process.Kill()
		io.Copy(io.Discard, out)
	}
	werr := r.cmd.Wait()
	m.mu.Lock()
	r.ended = err == nil && werr == nil
	stopped := s.run != r
	m.mu.Unlock()
	if !r.ended && !stopped {
		m.Log("the converter stopped: %v %v %s", err, werr, r.stderr.String())
	}
	close(r.exited)
}

var errStopped = errors.New("stopped")

// slice does the cutting. ffmpeg's output is a header followed by
// fragments, each a short index (moof) and its data (mdat), one per key
// frame. A fragment goes into the chunk that the moment its key frame is
// shown falls in.
func (m *Manager) slice(s *session, r *run, out io.Reader) error {
	br := bufio.NewReaderSize(out, 256<<10)
	grid := s.spec.Grid
	var header []byte
	var video videoTrack
	var f *os.File
	cur, skip := -1, false
	finish := func() error {
		if f == nil {
			return nil
		}
		err := f.Close()
		f = nil
		if err == nil {
			err = os.Rename(r.file(cur)+".tmp", r.file(cur))
		}
		m.mu.Lock()
		r.next = cur + 1
		// Chunks well behind the viewer are not kept.
		for r.low < s.lastSeg-m.Behind && r.low < cur {
			os.Remove(r.file(r.low))
			r.low++
		}
		m.mu.Unlock()
		return err
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for {
		typ, raw, payload, err := boxHeader(br)
		if err == io.EOF {
			return finish()
		}
		if err != nil {
			return err
		}
		switch typ {
		case "ftyp", "moov":
			if payload > 16<<20 {
				return errors.New("the converter's header is implausibly large")
			}
			body := make([]byte, payload)
			if _, err := io.ReadFull(br, body); err != nil {
				return err
			}
			header = append(append(header, raw...), body...)
			if typ == "moov" {
				video = findVideo(body)
				if err := os.WriteFile(r.file(-1)+".tmp", header, 0o600); err != nil {
					return err
				}
				if err := os.Rename(r.file(-1)+".tmp", r.file(-1)); err != nil {
					return err
				}
				m.mu.Lock()
				r.header = true
				m.mu.Unlock()
			}
		case "moof":
			if payload > 64<<20 {
				return errors.New("the converter's output is not in the expected form")
			}
			body := make([]byte, payload)
			if _, err := io.ReadFull(br, body); err != nil {
				return err
			}
			seg := max(cur, r.first)
			if ms, ok := fragmentStart(body, video); ok {
				seg = grid.index(ms - clockShiftMs)
			}
			if cur < 0 && seg < r.first {
				skip = true // the lead-in before the chunk that was asked for
				continue
			}
			skip = false
			// Chunk numbers never go back and never skip, whatever the
			// times say, so every chunk the list promises exists.
			switch {
			case cur < 0:
				seg = r.first
			case seg > cur+1:
				seg = cur + 1
			case seg < cur:
				seg = cur
			}
			if seg != cur {
				if err := finish(); err != nil {
					return err
				}
				if err := m.hold(s, r, seg); err != nil {
					return err
				}
				cur = seg
				if f, err = os.Create(r.file(cur) + ".tmp"); err != nil {
					return err
				}
			}
			if _, err := f.Write(append(raw, body...)); err != nil {
				return err
			}
		case "mdat":
			if skip || f == nil {
				_, err = io.CopyN(io.Discard, br, payload)
			} else {
				if _, err = f.Write(raw); err == nil {
					_, err = io.CopyN(f, br, payload)
				}
			}
			if err != nil {
				return err
			}
		default:
			if _, err := io.CopyN(io.Discard, br, payload); err != nil {
				return err
			}
		}
	}
}

// hold makes the reader wait while it is far enough ahead of the viewer.
// ffmpeg, finding nobody reading, waits too.
func (m *Manager) hold(s *session, r *run, seg int) error {
	for {
		m.mu.Lock()
		ok := seg <= s.lastSeg+m.Ahead
		m.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-r.stop:
			return errStopped
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// halt stops the session's ffmpeg, if any, and waits for it to be gone.
// The caller holds the lock; it is released while waiting.
func (m *Manager) halt(s *session) {
	r := s.run
	if r == nil {
		return
	}
	s.run = nil
	close(r.stop)
	r.cmd.Process.Kill()
	m.mu.Unlock()
	<-r.exited
	m.mu.Lock()
}

func (m *Manager) janitor() {
	defer m.done.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		m.mu.Lock()
		now := time.Now()
		for token, s := range m.sessions {
			idle := now.Sub(s.lastReq)
			switch {
			case idle > m.Forget:
				m.halt(s)
				os.RemoveAll(s.dir)
				delete(m.sessions, token)
			case idle > m.IdleStop && s.run != nil:
				// Nobody is watching: stop, and give the disk space back.
				m.halt(s)
				os.RemoveAll(s.dir)
			}
		}
		m.mu.Unlock()
	}
}

// tail keeps the last part of what ffmpeg wrote to its error output.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}
