package hls

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// These tests run the real ffmpeg on two small films made for the purpose:
//
//	plain: 40 seconds, a key frame every 2 seconds, Dolby (AC-3) sound.
//	rough: 60 seconds, key frames at irregular moments up to 7 seconds apart,
//	       frames stored out of order (B-frames), as most real films are.

func tools(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, ffprobe = probe.FindFFmpeg(), probe.Find()
	if ffmpeg == "" || ffprobe == "" {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg and ffprobe were not found, and LM_REQUIRE_FFMPEG says they must be")
		}
		t.Skip("ffmpeg and ffprobe are not installed here")
	}
	return
}

var films = map[string]string{}
var filmDir string

func source(t *testing.T, ffmpeg, name string) string {
	t.Helper()
	if p := films[name]; p != "" {
		return p
	}
	if filmDir == "" {
		dir, err := os.MkdirTemp("", "lm-hls-src-")
		if err != nil {
			t.Fatal(err)
		}
		filmDir = dir
	}
	p := filepath.Join(filmDir, name+".mkv")
	a := []string{"-hide_banner", "-loglevel", "error", "-y"}
	switch name {
	case "plain":
		a = append(a, "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=40", "-f", "lavfi", "-i", "sine=frequency=440:duration=40",
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", p)
	case "rough":
		a = append(a, "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=60", "-f", "lavfi", "-i", "sine=frequency=440:duration=60",
			"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-g", "168", "-sc_threshold", "0",
			"-force_key_frames", "3.5,5,11.25,18,19,26.5,33,40.75,47,54.5",
			"-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", p)
	}
	if out, err := exec.Command(ffmpeg, a...).CombinedOutput(); err != nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not make the test film: %v %s", err, out)
		}
		t.Skipf("this ffmpeg cannot make the test film: %v %s", err, out)
	}
	films[name] = p
	return p
}

func TestMain(m *testing.M) {
	code := m.Run()
	if filmDir != "" {
		os.RemoveAll(filmDir)
	}
	os.Exit(code)
}

func manager(t *testing.T, ffmpeg string) *Manager {
	t.Helper()
	m, err := New(ffmpeg, filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	m.Log = t.Logf
	t.Cleanup(m.Close)
	return m
}

func grid(t *testing.T, ffprobe, path string, durationMs int64) Grid {
	t.Helper()
	g, err := FindCuts(context.Background(), ffprobe, path, durationMs)
	if err != nil || !g.Valid() {
		t.Fatalf("finding the cuts: %v %+v", err, g)
	}
	return g
}

type streamInfo struct {
	Codec     string `json:"codec_name"`
	Type      string `json:"codec_type"`
	Channels  int    `json:"channels"`
	StartTime string `json:"start_time"`
	Packets   string `json:"nb_read_packets"`
}

func inspect(t *testing.T, ffprobe, path string) map[string]streamInfo {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-count_packets", "-show_streams", "-print_format", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var r struct{ Streams []streamInfo }
	json.Unmarshal(out, &r)
	m := map[string]streamInfo{}
	for _, s := range r.Streams {
		m[s.Type] = s
	}
	return m
}

func f(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

// fetch asks for the header and the chunks from..to in order, as a player
// would, and joins them into one file.
func fetch(t *testing.T, m *Manager, token string, from, to int) string {
	t.Helper()
	ctx := context.Background()
	var buf bytes.Buffer
	b, err := m.Init(ctx, token)
	if err != nil || len(b) == 0 {
		t.Fatalf("header: %v", err)
	}
	buf.Write(b)
	for i := from; i <= to; i++ {
		b, err := m.Segment(ctx, token, i)
		if err != nil || len(b) == 0 {
			t.Fatalf("chunk %d: %v", i, err)
		}
		buf.Write(b)
	}
	out := filepath.Join(t.TempDir(), "joined.mp4")
	os.WriteFile(out, buf.Bytes(), 0o600)
	return out
}

// decodes plays the whole file through ffmpeg's decoders and fails on the
// first error: the nearest thing here to a player that is not a browser.
func decodes(t *testing.T, ffmpeg, path string) {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-v", "error", "-xerror", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil || len(bytes.TrimSpace(out)) > 0 {
		t.Fatalf("the stream does not decode cleanly: %v\n%s", err, out)
	}
}

const shift = float64(clockShiftMs) / 1000

func TestRepackagingKeepsPictureAndSoundUntouched(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "plain")
	m := manager(t, ffmpeg)
	token := m.Open(Spec{Path: src, Grid: grid(t, ffprobe, src, 40000), VideoCodec: "h264", AudioTrack: 0, AudioCopy: true, Owner: "u1"})
	list, err := m.Playlist(token)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(list), "#EXTINF"); n != 7 || !strings.Contains(string(list), "#EXT-X-ENDLIST") || !strings.Contains(string(list), `URI="init.mp4"`) {
		t.Fatalf("playlist has %d chunks:\n%s", n, list)
	}
	if !strings.Contains(string(list), "#EXTINF:4.000,\nseg-6.m4s") {
		t.Errorf("the last chunk should be the 4 seconds that are left:\n%s", list)
	}
	joined := fetch(t, m, token, 0, 6)
	got, want := inspect(t, ffprobe, joined), inspect(t, ffprobe, src)
	if got["video"].Codec != "h264" || got["audio"].Codec != "ac3" {
		t.Fatalf("codecs %s + %s, want h264 + ac3 passed through", got["video"].Codec, got["audio"].Codec)
	}
	if got["video"].Packets != want["video"].Packets || got["audio"].Packets != want["audio"].Packets {
		t.Errorf("frames were lost or added: picture %s of %s, sound %s of %s", got["video"].Packets, want["video"].Packets, got["audio"].Packets, want["audio"].Packets)
	}
	if st := f(got["video"].StartTime); math.Abs(st-shift) > 0.001 {
		t.Errorf("the picture starts at %.3f s, want %.3f", st, shift)
	}
	if off := f(got["video"].StartTime) - f(got["audio"].StartTime); math.Abs(off) > 0.05 {
		t.Errorf("picture and sound start %.3f s apart", off)
	}
	decodes(t, ffmpeg, joined)
	if _, err := m.Segment(context.Background(), token, 7); err != ErrPastEnd {
		t.Errorf("a chunk past the end: %v", err)
	}
	if _, err := m.Segment(context.Background(), "nope", 0); err != ErrNoSession {
		t.Errorf("an unknown session: %v", err)
	}
}

func TestSoundIsConvertedAndPictureStillUntouched(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "plain")
	m := manager(t, ffmpeg)
	g := grid(t, ffprobe, src, 40000)
	token := m.Open(Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: 0, AudioCopy: false})
	joined := fetch(t, m, token, 0, 6)
	got, want := inspect(t, ffprobe, joined), inspect(t, ffprobe, src)
	if got["video"].Codec != "h264" || got["video"].Packets != want["video"].Packets {
		t.Errorf("the picture was touched: %s, %s frames of %s", got["video"].Codec, got["video"].Packets, want["video"].Packets)
	}
	if got["audio"].Codec != "aac" || got["audio"].Channels != 2 {
		t.Errorf("sound is %s with %d channels, want stereo AAC", got["audio"].Codec, got["audio"].Channels)
	}
	// 40 seconds of AAC is about 1,723 frames of 1,024 samples at 44.1 kHz.
	if n := f(got["audio"].Packets); n < 1715 || n > 1730 {
		t.Errorf("the sound has %v frames, want about 1723 (40 seconds)", n)
	}
	if off := f(got["video"].StartTime) - f(got["audio"].StartTime); math.Abs(off) > 0.05 {
		t.Errorf("picture and sound start %.3f s apart", off)
	}
	decodes(t, ffmpeg, joined)
	// A file with no sound track is repackaged without one.
	silent := m.Open(Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: -1})
	if got := inspect(t, ffprobe, fetch(t, m, silent, 0, 1)); got["audio"].Codec != "" || got["video"].Codec != "h264" {
		t.Errorf("silent session produced %+v", got)
	}
}

func TestCutsAreFoundAtRealKeyFrames(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "rough")
	g := grid(t, ffprobe, src, 60000)
	// The film's key frames, read the slow and certain way.
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", src).Output()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[int64]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.Split(line, ","); len(p) == 2 && strings.HasPrefix(p[1], "K") {
			keys[int64(f(p[0])*1000+0.5)] = true
		}
	}
	if len(g.Cuts) < 6 || len(g.Cuts) > 10 {
		t.Errorf("a 60-second film was cut into %d chunks: %v", len(g.Cuts), g.Cuts)
	}
	for i, c := range g.Cuts[1:] {
		if !keys[c] {
			t.Errorf("cut %d at %d ms is not a key frame (key frames: %v)", i+1, c, keys)
		}
		if c-g.Cuts[i] < 2000 {
			t.Errorf("chunk %d is only %d ms long", i, c-g.Cuts[i])
		}
	}
	// A grid survives being stored and read back.
	b, _ := json.Marshal(g)
	var back Grid
	if json.Unmarshal(b, &back); !back.Valid() || len(back.Cuts) != len(g.Cuts) {
		t.Errorf("stored and read back: %+v", back)
	}
	if (Grid{}).Valid() || (Grid{StartMs: 0, EndMs: 10, Cuts: []int64{0, 5, 5}}).Valid() {
		t.Error("a broken grid was accepted")
	}
	if u := Uniform(40000); !u.Valid() || len(u.Cuts) != 7 {
		t.Errorf("uniform grid: %+v", u)
	}
}

// The heart of it: wherever ffmpeg is started, each chunk begins exactly
// where the list said it would, and holds exactly the same frames.
func TestEveryChunkBeginsWhereTheListSays(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "rough")
	g := grid(t, ffprobe, src, 60000)
	for _, copySound := range []bool{true, false} {
		m := manager(t, ffmpeg)
		straight := m.Open(Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: 0, AudioCopy: copySound})
		whole := fetch(t, m, straight, 0, len(g.Cuts)-1)
		got, want := inspect(t, ffprobe, whole), inspect(t, ffprobe, src)
		if got["video"].Packets != want["video"].Packets {
			t.Errorf("played straight through: %s frames of %s", got["video"].Packets, want["video"].Packets)
		}
		decodes(t, ffmpeg, whole)
		for k := range g.Cuts {
			a := inspect(t, ffprobe, fetch(t, m, straight, k, k))
			// A fresh session asked for chunk k first: ffmpeg starts there.
			jumped := m.Open(Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: 0, AudioCopy: copySound})
			b := inspect(t, ffprobe, fetch(t, m, jumped, k, k))
			wantStart := float64(g.Cuts[k])/1000 + shift
			for name, x := range map[string]map[string]streamInfo{"played through": a, "jumped to": b} {
				if st := f(x["video"].StartTime); math.Abs(st-wantStart) > 0.002 {
					t.Errorf("copy sound %v: chunk %d %s begins at %.3f s, the list says %.3f", copySound, k, name, st, wantStart)
				}
				if off := f(x["video"].StartTime) - f(x["audio"].StartTime); math.Abs(off) > 0.1 {
					t.Errorf("copy sound %v: chunk %d %s: picture and sound start %.3f s apart", copySound, k, name, off)
				}
			}
			if a["video"].Packets != b["video"].Packets || copySound && a["audio"].Packets != b["audio"].Packets {
				t.Errorf("copy sound %v: chunk %d differs: played through %s/%s frames, jumped to %s/%s", copySound, k,
					a["video"].Packets, a["audio"].Packets, b["video"].Packets, b["audio"].Packets)
			}
		}
		m.Close()
	}
}

func TestJumpingAheadAndBack(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "plain")
	m := manager(t, ffmpeg)
	m.ReadRate, m.Burst = 1, 6 // as slow as real playback, so a jump truly outruns it
	token := m.Open(Spec{Path: src, Grid: grid(t, ffprobe, src, 40000), VideoCodec: "h264", AudioTrack: 0, AudioCopy: false})
	ctx := context.Background()
	if _, err := m.Segment(ctx, token, 0); err != nil {
		t.Fatal(err)
	}
	header, _ := m.Init(ctx, token)
	start := time.Now()
	if _, err := m.Segment(ctx, token, 5); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("a jump far ahead took %v, over 3 seconds", took)
	}
	// A player keeps the header it fetched first, so every run must write
	// the same one.
	if again, _ := m.Init(ctx, token); len(header) == 0 || !bytes.Equal(header, again) {
		t.Errorf("the header differs between runs (%d and %d bytes)", len(header), len(again))
	}
	// Back to the start: the earlier run's chunks are gone, so it restarts.
	if _, err := m.Segment(ctx, token, 0); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	starts := m.sessions[token].starts
	m.mu.Unlock()
	if starts != 3 {
		t.Errorf("ffmpeg was started %d times, want 3 (start, jump ahead, jump back)", starts)
	}
	if m.Running() != 1 {
		t.Errorf("%d converters running for one viewer", m.Running())
	}
}

func TestNothingIsLeftRunningOrOnDisk(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "plain")
	root := filepath.Join(t.TempDir(), "chunks")
	// Leftovers from a run of the program that was cut short.
	os.MkdirAll(filepath.Join(root, "stale", "run-1"), 0o700)
	os.WriteFile(filepath.Join(root, "stale", "run-1", "seg-0.m4s"), []byte("old"), 0o600)
	m, err := New(ffmpeg, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "stale")); err == nil {
		t.Fatal("leftovers from an earlier run were not cleared at start")
	}
	m.ReadRate, m.Burst = 1, 6
	m.IdleStop = 3 * time.Second
	ctx := context.Background()
	g := grid(t, ffprobe, src, 40000)
	a := m.Open(Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: 0, AudioCopy: true, Owner: "ann"})
	b := m.Open(Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: 0, AudioCopy: true, Owner: "bob"})
	for _, tok := range []string{a, b} {
		if _, err := m.Segment(ctx, tok, 0); err != nil {
			t.Fatal(err)
		}
	}
	if m.Running() != 2 {
		t.Fatalf("%d converters running, want 2", m.Running())
	}
	// Bob signs out: his is stopped at once.
	m.Drop("bob")
	if m.Running() != 1 || m.Owner(b) != "" {
		t.Fatalf("after one viewer signed out: %d running", m.Running())
	}
	// Ann closes the tab: hers stops by itself and its chunks are deleted.
	deadline := time.Now().Add(10 * time.Second)
	for m.Running() > 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if m.Running() != 0 {
		t.Fatal("the converter kept running after the viewer went away")
	}
	time.Sleep(300 * time.Millisecond)
	if left, _ := filepath.Glob(filepath.Join(root, "*", "*", "*")); len(left) != 0 {
		t.Fatalf("chunks left on disk after the viewer went away: %v", left)
	}
	// Ann comes back and presses play: it simply starts again.
	if _, err := m.Segment(ctx, a, 2); err != nil {
		t.Fatalf("resuming after an idle stop: %v", err)
	}
	m.Close()
	if _, err := os.Stat(root); err == nil {
		t.Fatal("closing left the chunk folder behind")
	}
}

func TestTheConverterWaitsWhenFarAheadAndForgetsWhatWasWatched(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "plain")
	m := manager(t, ffmpeg)
	m.Ahead, m.Behind = 1, 1
	ctx := context.Background()
	token := m.Open(Spec{Path: src, Grid: grid(t, ffprobe, src, 40000), VideoCodec: "h264", AudioTrack: 0, AudioCopy: true})
	if _, err := m.Segment(ctx, token, 0); err != nil {
		t.Fatal(err)
	}
	// At full speed the whole film would be done in a moment. It must stop
	// one chunk past the viewer instead.
	time.Sleep(1500 * time.Millisecond)
	state := func() (low, next int) {
		m.mu.Lock()
		defer m.mu.Unlock()
		r := m.sessions[token].run
		return r.low, r.next
	}
	if _, next := state(); next > 2 {
		t.Fatalf("with the viewer at chunk 0 the converter ran on to chunk %d", next)
	}
	if m.Running() != 1 {
		t.Fatal("the converter should be waiting, not gone")
	}
	// The viewer watches on; the converter follows, and old chunks go.
	for i := 1; i <= 6; i++ {
		if _, err := m.Segment(ctx, token, i); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	if low, _ := state(); low < 3 {
		t.Errorf("chunks from %d are still kept with the viewer at chunk 6", low)
	}
	m.mu.Lock()
	starts := m.sessions[token].starts
	m.mu.Unlock()
	if starts != 1 {
		t.Errorf("watching straight through started ffmpeg %d times", starts)
	}
}

func TestAFileThatCannotBeConvertedFailsOnceAndSaysWhy(t *testing.T) {
	ffmpeg, _ := tools(t)
	m := manager(t, ffmpeg)
	m.Log = func(string, ...any) {}
	bad := filepath.Join(t.TempDir(), "not a film.mkv")
	os.WriteFile(bad, []byte("this is not a film"), 0o600)
	token := m.Open(Spec{Path: bad, Grid: Uniform(40000), VideoCodec: "h264", AudioTrack: 0})
	start := time.Now()
	_, err := m.Segment(context.Background(), token, 0)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("a broken file: %v after %v", err, time.Since(start))
	}
	m.mu.Lock()
	starts := m.sessions[token].starts
	m.mu.Unlock()
	if starts != 1 {
		t.Errorf("ffmpeg was started %d times for one request on a broken file", starts)
	}
}

func TestOneSignInCannotHoldEndlessFilmsOpen(t *testing.T) {
	ffmpeg, _ := tools(t)
	m := manager(t, ffmpeg)
	var first string
	for i := 0; i < maxPerOwner+3; i++ {
		tok := m.Open(Spec{Path: "x", Grid: Uniform(40000), VideoCodec: "h264", Owner: "ann"})
		if i == 0 {
			first = tok
		}
		time.Sleep(2 * time.Millisecond)
	}
	other := m.Open(Spec{Path: "x", Grid: Uniform(40000), VideoCodec: "h264", Owner: "bob"})
	if n := m.Sessions(); n != maxPerOwner+1 {
		t.Errorf("%d films held open, want %d for one sign-in and 1 for the other", n, maxPerOwner)
	}
	if m.Owner(first) != "" || m.Owner(other) != "bob" {
		t.Error("the film left alone longest should be the one closed, and only its owner's")
	}
}

func TestCommandLineIsBuiltOnlyFromTypedValues(t *testing.T) {
	g := Grid{EndMs: 60000, Cuts: []int64{0, 6000, 12500, 18000}}
	path := `C:\Films\A "quoted"; rm -rf.mkv`
	a := args(Spec{Path: path, Grid: g, VideoCodec: "hevc", AudioTrack: 2, AudioCopy: false}, 2, 8, 60)
	joined := strings.Join(a, "\x00")
	for _, want := range []string{"-ss\x0012.400", "-tag:v\x00hvc1", "-map\x000:a:2", "-c:a\x00aac", "-c:v\x00copy", "-copyts"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", strings.ReplaceAll(want, "\x00", " "), a)
		}
	}
	// The file path is one argument, exactly as given, right after -i.
	for i, x := range a {
		if x == "-i" && a[i+1] != path {
			t.Errorf("path argument is %q", a[i+1])
		}
	}
	if b := args(Spec{Path: "x", Grid: g, VideoCodec: "h264", AudioTrack: -1}, 0, 8, 60); strings.Contains(strings.Join(b, " "), "-ss") || strings.Contains(strings.Join(b, " "), "0:a") {
		t.Errorf("from the start with no sound: %v", b)
	}
}
