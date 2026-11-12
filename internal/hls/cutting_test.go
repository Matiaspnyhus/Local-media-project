package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// How a converted picture's stream is cut into chunks, tested without
// ffmpeg. In ffmpeg's place the manager is given this test program itself:
// started the way the manager starts ffmpeg, it writes out a stream the
// test made by hand and, if told to, then stays alive without writing any
// more. That gives a test what the real ffmpeg cannot be made to give: a
// key frame in the wrong place, or a converter that has sent one chunk and
// is still busy with the next.

// fakeStream names the file the stand-in writes out, and fakeStays, when
// set, makes it stay alive afterwards. They are environment variables,
// which is how a test reaches a program the manager starts.
const (
	fakeStream = "LM_HLS_FAKE_CONVERTER"
	fakeStays  = "LM_HLS_FAKE_CONVERTER_STAYS"
)

// This runs before anything else when the test program starts. The first
// thing the manager says to ffmpeg is always "-hide_banner", which nobody
// says to a test program: only then, and only with a stream named, does it
// play the converter. Otherwise it is the test program it always was.
func init() {
	path := os.Getenv(fakeStream)
	if path == "" || len(os.Args) < 2 || os.Args[1] != "-hide_banner" {
		return
	}
	stream, err := os.ReadFile(path)
	if err != nil {
		os.Exit(3)
	}
	os.Stdout.Write(stream)
	for os.Getenv(fakeStays) != "" {
		time.Sleep(time.Hour) // until the manager stops it
	}
	os.Exit(0)
}

// standIn returns a manager whose "ffmpeg" writes out the stream given, and
// what the manager logs.
func standIn(t *testing.T, stays bool, stream ...[]byte) (*Manager, func() string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "stream.mp4")
	if err := os.WriteFile(file, bytes.Join(stream, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeStream, file)
	t.Setenv(fakeStays, map[bool]string{true: "yes"}[stays])
	m, err := New(exe, filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var logged strings.Builder
	m.Log = func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&logged, format+"\n", a...)
	}
	t.Cleanup(m.Close)
	return m, func() string { mu.Lock(); defer mu.Unlock(); return logged.String() }
}

// The stand-in's stream is of a film at 23.976 frames a second, whose
// picture ffmpeg counts in 24,000 ticks a second.
const standInScale = 24000

// standInHeader is a header with a sound track and a picture track.
func standInHeader() []byte {
	zeros := func(n int) []byte { return make([]byte, n) }
	return bytes.Join([][]byte{
		box("ftyp", []byte("isom"), u32(512), []byte("isomiso6mp41")),
		box("moov",
			box("mvhd", zeros(100)),
			box("trak",
				box("tkhd", u32(0, 0, 0, 2), zeros(68)),
				box("mdia", box("mdhd", u32(0, 0, 0, 48000, 0)), box("hdlr", u32(0, 0), []byte("soun"), zeros(13)))),
			box("trak",
				box("tkhd", u32(0, 0, 0, 1), zeros(68)),
				box("mdia", box("mdhd", u32(0, 0, 0, standInScale, 0)), box("hdlr", u32(0, 0), []byte("vide"), zeros(13))))),
	}, nil)
}

// standInFragment is a fragment whose picture begins at ms on the film's
// clock, with something to tell it by as its data.
func standInFragment(ms float64) []byte {
	ticks := uint32(math.Round((ms + clockShiftMs) * standInScale / 1000))
	return append(
		box("moof",
			box("mfhd", u32(0, 1)),
			box("traf", box("tfhd", u32(0x020000, 1)), box("tfdt", u32(0x01000000, 0, ticks)), box("trun", u32(0x000201, 1, 0, 300))),
			box("traf", box("tfhd", u32(0x020000, 2)), box("tfdt", u32(0x01000000, 0, ticks*2)), box("trun", u32(0x000201, 1, 0, 300)))),
		box("mdat", []byte(fmt.Sprintf("the picture from %.3f ms", ms)))...)
}

// standInSpec is the film the stand-in's streams are of: nine chunks, cut
// at 0, 3003, 6006, ... 24024 ms.
func standInSpec(t *testing.T) Spec {
	t.Helper()
	g, c := Lattice(0, 24983, 25025, 24000, 1001)
	c.W, c.H, c.MaxRateK, c.LeadMs = 320, 180, 2500, 2000
	if err := c.fits(g); err != nil || len(g.Cuts) != 9 {
		t.Fatalf("the stand-in's film: %v %v", err, g.Cuts)
	}
	return Spec{Path: "film.mkv", Grid: g, AudioTrack: 0, Convert: &c}
}

// A viewer who is waiting for a chunk gets it as soon as it is whole. Here
// the converter has sent two chunks and is still busy with the third, as
// the real one is for some seconds after a jump: the second chunk must be
// served all the same, and not held until a third arrives to follow it.
func TestAConvertedChunkIsServedAsSoonAsItIsWhole(t *testing.T) {
	first, second := standInFragment(0), standInFragment(3003)
	m, _ := standIn(t, true, standInHeader(), first, second)
	token := m.Open(standInSpec(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if b, err := m.Init(ctx, token); err != nil || !bytes.Equal(b, standInHeader()) {
		t.Fatalf("the header: %v", err)
	}
	for k, want := range [][]byte{first, second} {
		b, err := m.Segment(ctx, token, k)
		if err != nil || !bytes.Equal(b, want) {
			t.Fatalf("chunk %d while the converter is busy with the one after: %v, %d bytes of %d", k, err, len(b), len(want))
		}
	}
	if m.Running() != 1 {
		t.Errorf("%d converters running, want the one that is still busy", m.Running())
	}
	// The same stream beside an untouched picture is cut the old way, by
	// the times alone: there a chunk may be several fragments, so it is
	// whole only when something that belongs to a later chunk arrives.
	plain := standInSpec(t)
	plain.Convert = nil
	token = m.Open(plain)
	if b, err := m.Segment(ctx, token, 0); err != nil || !bytes.Equal(b, first) {
		t.Fatalf("an untouched picture's first chunk: %v", err)
	}
	short, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	if _, err := m.Segment(short, token, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an untouched picture's second chunk, with nothing after it yet: %v", err)
	}
}

// The converter is told to put a key frame at every cut and nowhere else.
// When one comes somewhere else, the chunks cannot hold what the list says
// they do: the run is ended, and whoever is waiting is told why.
func TestAConverterThatLeavesTheLatticeIsStopped(t *testing.T) {
	// The third key frame is a frame late.
	m, logged := standIn(t, false, standInHeader(), standInFragment(0), standInFragment(3003), standInFragment(6048), standInFragment(9009))
	token := m.Open(standInSpec(t))
	ctx := context.Background()
	for k := 0; k < 2; k++ {
		if b, err := m.Segment(ctx, token, k); err != nil || len(b) == 0 {
			t.Fatalf("chunk %d, which begins where it should: %v", k, err)
		}
	}
	_, err := m.Segment(ctx, token, 2)
	if !errors.Is(err, ErrLeftLattice) || !strings.Contains(fmt.Sprint(err), "left the lattice: chunk 2 begins at 6.048 s, where the list says 6.006 s") {
		t.Errorf("the chunk that begins a frame late: %v", err)
	}
	m.mu.Lock()
	s := m.sessions[token]
	starts, why := s.starts, s.run.why
	m.mu.Unlock()
	// The request started the converter once more, at the chunk itself,
	// before it gave up.
	if starts != 2 || !errors.Is(why, ErrLeftLattice) || m.Running() != 0 {
		t.Errorf("the converter was started %d times, %d are running, and the run ended with: %v", starts, m.Running(), why)
	}
	if text := logged(); !strings.Contains(text, "the converter stopped") || !strings.Contains(text, "left the lattice: chunk 2 begins at 6.048 s") {
		t.Errorf("the log does not say why the converter was stopped:\n%s", text)
	}
	// A viewer who jumps past the bad place gets the film from there: a
	// run for the next chunk begins before it and drops what it makes
	// there, the misplaced key frame with it.
	if b, err := m.Segment(ctx, token, 3); err != nil || !bytes.Equal(b, standInFragment(9009)) {
		t.Errorf("the chunk after it, jumped to: %v", err)
	}
	// The same stream is fine for a picture that is passed on untouched,
	// whose chunks are cut by the times and need not begin on the dot.
	plain := standInSpec(t)
	plain.Convert = nil
	token = m.Open(plain)
	for k := 0; k < 4; k++ {
		if _, err := m.Segment(ctx, token, k); err != nil {
			t.Errorf("chunk %d of an untouched picture: %v", k, err)
		}
	}
}

// A run that is started for a chunk in the middle of the film begins a
// little before it. What it makes there is not part of any chunk.
func TestWhatAConverterMakesBeforeItsChunkIsNotKept(t *testing.T) {
	before, third, fourth := standInFragment(8884), standInFragment(9009), standInFragment(12012)
	m, _ := standIn(t, false, standInHeader(), before, third, fourth, standInFragment(15015))
	token := m.Open(standInSpec(t))
	ctx := context.Background()
	if b, err := m.Segment(ctx, token, 3); err != nil || !bytes.Equal(b, third) {
		t.Fatalf("chunk 3 from a run that began before it: %v, %q", err, b)
	}
	if b, err := m.Segment(ctx, token, 4); err != nil || !bytes.Equal(b, fourth) {
		t.Fatalf("chunk 4: %v, %q", err, b)
	}
	m.mu.Lock()
	starts, low := m.sessions[token].starts, m.sessions[token].run.low
	m.mu.Unlock()
	if starts != 1 || low != 3 {
		t.Errorf("the converter was started %d times and its chunks begin at %d, want once and 3", starts, low)
	}
	// A run whose first fragment begins after its chunk has left the
	// lattice before it started.
	m, _ = standIn(t, false, standInHeader(), standInFragment(9051), standInFragment(12012))
	if _, err := m.Segment(ctx, m.Open(standInSpec(t)), 3); !errors.Is(err, ErrLeftLattice) {
		t.Errorf("a run for chunk 3 that begins after the cut: %v", err)
	}
}

// After the last cut there is nothing left to be out of step with. Whatever
// the converter sends from there on belongs to the last chunk: a key frame
// it added because the picture ran on longer than was thought, say.
func TestEverythingAfterTheLastCutIsTheLastChunk(t *testing.T) {
	eighth, last, more, evenMore := standInFragment(21021), standInFragment(24024), standInFragment(24500), standInFragment(31000)
	m, _ := standIn(t, false, standInHeader(), eighth, last, more, evenMore)
	token := m.Open(standInSpec(t))
	ctx := context.Background()
	if b, err := m.Segment(ctx, token, 7); err != nil || !bytes.Equal(b, eighth) {
		t.Fatalf("the last chunk but one: %v, %q", err, b)
	}
	if b, err := m.Segment(ctx, token, 8); err != nil || !bytes.Equal(b, bytes.Join([][]byte{last, more, evenMore}, nil)) {
		t.Fatalf("the last chunk: %v, %q", err, b)
	}
	if _, err := m.Segment(ctx, token, 9); err != ErrPastEnd {
		t.Errorf("a chunk past the end: %v", err)
	}
}

// Every fragment of a converted picture has the picture in it, or it could
// not be told where it belongs. One without is not guessed at.
func TestAConvertedFragmentWithoutThePictureEndsTheRun(t *testing.T) {
	soundOnly := append(
		box("moof", box("mfhd", u32(0, 1)), box("traf", box("tfhd", u32(0x020000, 2)), box("tfdt", u32(0x01000000, 0, 4003*48)), box("trun", u32(0x000201, 1, 0, 300)))),
		box("mdat", []byte("sound alone"))...)
	m, logged := standIn(t, false, standInHeader(), standInFragment(0), soundOnly, standInFragment(3003))
	token := m.Open(standInSpec(t))
	ctx := context.Background()
	if _, err := m.Segment(ctx, token, 0); err != nil {
		t.Fatalf("the first chunk: %v", err)
	}
	if _, err := m.Segment(ctx, token, 1); err == nil || errors.Is(err, ErrLeftLattice) {
		t.Errorf("the chunk after a fragment without the picture: %v", err)
	}
	if text := logged(); !strings.Contains(text, "not in the expected form") {
		t.Errorf("the log does not say what was wrong:\n%s", text)
	}
}
