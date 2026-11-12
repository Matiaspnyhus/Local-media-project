package hls

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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

// These tests run the real ffmpeg on small films made for the purpose:
//
//	plain:  40 seconds, a key frame every 2 seconds, Dolby (AC-3) sound.
//	rough:  60 seconds, key frames at irregular moments up to 7 seconds apart,
//	        frames stored out of order (B-frames), as most real films are.
//	open:   60 seconds as the x265 encoder makes them when left alone: each
//	        key frame is followed by a few frames that are shown before it
//	        ("open GOP"), as on most discs and in most HEVC films.
//	uneven: 60 seconds with every fifth frame missing, so the time from one
//	        frame to the next varies (a "variable frame rate").
//	recording: the rough film as a TV recording keeps it: MPEG-TS, which has
//	        no list of key frames, with a clock that starts ten hours in.
//	capture: 20 seconds of MPEG-TS with AAC sound, as programs that record
//	        the screen or a web stream write it.
//	still:  90 seconds with one key frame, at the very start.

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
	if name == "recording" || name == "capture" {
		p = filepath.Join(filmDir, name+".ts")
	}
	picture := func(seconds string) []string {
		return []string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=" + seconds, "-f", "lavfi", "-i", "sine=frequency=440:duration=" + seconds}
	}
	// Each film has one or more recipes; the first this ffmpeg can follow
	// is used.
	var recipes [][]string
	switch name {
	case "plain":
		recipes = [][]string{append(picture("40"),
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", p)}
	case "rough":
		recipes = [][]string{append(picture("60"),
			"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-g", "168", "-sc_threshold", "0",
			"-force_key_frames", "3.5,5,11.25,18,19,26.5,33,40.75,47,54.5",
			"-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", p)}
	case "open":
		recipes = [][]string{
			append(picture("60"), "-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=error",
				"-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", p),
			// Without x265, the same shape from x264.
			append(picture("60"), "-c:v", "libx264", "-preset", "veryfast", "-x264-params", "open-gop=1:keyint=96:bframes=3",
				"-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", p),
		}
	case "uneven":
		recipes = [][]string{append(picture("60"),
			"-vf", `select=not(eq(mod(n\,5)\,3))`, "-fps_mode", "vfr",
			"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-g", "40",
			"-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", p)}
	case "recording":
		recipes = [][]string{append(picture("60"),
			"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-g", "168", "-sc_threshold", "0",
			"-force_key_frames", "3.5,5,11.25,18,19,26.5,33,40.75,47,54.5",
			"-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", "-output_ts_offset", "36000", "-f", "mpegts", p)}
	case "capture":
		recipes = [][]string{append(picture("20"),
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k", "-f", "mpegts", p)}
	case "still":
		recipes = [][]string{append(picture("90"),
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "9999", "-sc_threshold", "0",
			"-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", p)}
	}
	var out []byte
	var err error
	for _, recipe := range recipes {
		a := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, recipe...)
		if out, err = exec.Command(ffmpeg, a...).CombinedOutput(); err == nil {
			break
		}
	}
	if err != nil || len(recipes) == 0 {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not make the test film %q: %v %s", name, err, out)
		}
		t.Skipf("this ffmpeg cannot make the test film %q: %v %s", name, err, out)
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

// realKeyFrames returns a film's key frames, in milliseconds, read by the
// test itself the slow and certain way.
func realKeyFrames(t *testing.T, ffprobe, src string) map[int64]bool {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", src).Output()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[int64]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.Split(line, ","); len(p) >= 2 && strings.HasPrefix(p[1], "K") {
			keys[int64(f(p[0])*1000+0.5)] = true
		}
	}
	return keys
}

// AAC sound is stored one way in MPEG-TS and another in the chunks. ffmpeg
// has to be told to rewrap it; left alone it stops at the first sound.
func TestAACSoundFromMPEGTSIsPassedThrough(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "capture")
	g := grid(t, ffprobe, src, 20000)
	if len(g.Cuts) < 3 {
		t.Fatalf("a 20-second film was cut into %d chunks", len(g.Cuts))
	}
	m := manager(t, ffmpeg)
	spec := Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: 0, AudioCopy: true, AudioCodec: "aac"}
	joined := fetch(t, m, m.Open(spec), 0, len(g.Cuts)-1)
	got, want := inspect(t, ffprobe, joined), inspect(t, ffprobe, src)
	if got["video"].Codec != "h264" || got["audio"].Codec != "aac" {
		t.Fatalf("codecs %s + %s, want h264 + aac passed through", got["video"].Codec, got["audio"].Codec)
	}
	if got["video"].Packets != want["video"].Packets || got["audio"].Packets != want["audio"].Packets {
		t.Errorf("frames were lost or added: picture %s of %s, sound %s of %s", got["video"].Packets, want["video"].Packets, got["audio"].Packets, want["audio"].Packets)
	}
	decodes(t, ffmpeg, joined)
	// The same when ffmpeg is started partway through.
	last := len(g.Cuts) - 1
	if tail := inspect(t, ffprobe, fetch(t, m, m.Open(spec), last, last)); tail["audio"].Codec != "aac" || f(tail["audio"].Packets) < 10 {
		t.Errorf("jumping to the last chunk gave %+v", tail)
	}
	// AAC from a file that already stores it the right way (MKV, MP4) is
	// asked to be rewrapped all the same, and must come through unharmed.
	mkv := filepath.Join(t.TempDir(), "capture.mkv")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", src, "-c", "copy", mkv).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	spec.Path, spec.Grid = mkv, grid(t, ffprobe, mkv, 20000)
	joined = fetch(t, m, m.Open(spec), 0, len(spec.Grid.Cuts)-1)
	got, want = inspect(t, ffprobe, joined), inspect(t, ffprobe, mkv)
	if got["audio"].Codec != "aac" || got["video"].Packets != want["video"].Packets || got["audio"].Packets != want["audio"].Packets {
		t.Errorf("from MKV: sound %s, picture %s of %s frames, sound %s of %s", got["audio"].Codec, got["video"].Packets, want["video"].Packets, got["audio"].Packets, want["audio"].Packets)
	}
	decodes(t, ffmpeg, joined)
}

func TestCutsAreFoundAtRealKeyFrames(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "rough")
	g := grid(t, ffprobe, src, 60000)
	keys := realKeyFrames(t, ffprobe, src)
	// An MKV file's own index gives the same key frames with one read.
	index, err := matroskaKeyFrames(src)
	if err != nil || len(index) != len(keys) {
		t.Fatalf("the file's own index lists %d key frames (%v), the film has %d", len(index), err, len(keys))
	}
	for _, k := range index {
		if !keys[k] {
			t.Errorf("the index lists %d ms, which is not a key frame", k)
		}
	}
	if g.From != "index" {
		t.Errorf("an MKV file with a good index was cut by %q", g.From)
	}
	// The index may sit at the end of the file (the usual place) or at the
	// front; both are found.
	front := filepath.Join(t.TempDir(), "front.mkv")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", src, "-c", "copy", "-cues_to_front", "1", front).CombinedOutput(); err == nil {
		if again, err := matroskaKeyFrames(front); err != nil || len(again) != len(keys) {
			t.Errorf("with the index at the front: %d key frames (%v), want %d", len(again), err, len(keys))
		}
	} else {
		t.Logf("this ffmpeg cannot put the index at the front: %s", out)
	}
	// A file that is not MKV, or has no usable index, is visited by ffprobe
	// instead, and the cuts are still real key frames.
	mp4 := filepath.Join(t.TempDir(), "rough.mp4")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", src, "-c", "copy", mp4).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	gp := grid(t, ffprobe, mp4, 60000)
	if gp.From != "probe" || len(gp.Cuts) < 6 {
		t.Errorf("the MP4 copy was cut by %q into %d chunks", gp.From, len(gp.Cuts))
	}
	// An index that lists only one key frame in many is not used.
	if !denseEnough([]int64{0, 8000, 16000, 24000, 32000}, 0, 40000) || !denseEnough([]int64{0, 5000, 12000, 30000, 36000}, 0, 40000) {
		t.Error("a good index was judged too sparse")
	}
	if denseEnough([]int64{0, 5000, 26000, 31000, 36000}, 0, 40000) || denseEnough([]int64{0, 5000}, 0, 60000) {
		t.Error("an index with a gap of over 20 seconds was judged good")
	}
	for _, junk := range [][]byte{nil, {0x1A}, {0x1A, 0x45, 0xDF, 0xA3}, {0x1A, 0x45, 0xDF, 0xA3, 0x80, 0x18, 0x53, 0x80, 0x67, 0xFF}, bytes.Repeat([]byte{0xFF}, 64), bytes.Repeat([]byte{0x00}, 64)} {
		p := filepath.Join(t.TempDir(), "junk.mkv")
		os.WriteFile(p, junk, 0o600)
		if k, err := matroskaKeyFrames(p); err == nil {
			t.Errorf("junk %x was read as an index: %v", junk, k)
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

// A TV recording has no list of key frames and jumping in it does not land
// on one, so it is read through for them; and its clock starts hours in.
func TestCutsAreFoundInATVRecording(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "recording")
	g := grid(t, ffprobe, src, 60000)
	keys := realKeyFrames(t, ffprobe, src)
	if g.From != "read" {
		t.Errorf("the recording was cut by %q", g.From)
	}
	// ffmpeg starts such a file about a second and a half after the time
	// it was told to.
	if g.StartMs < 36000000 || g.StartMs > 36003000 || g.EndMs != g.StartMs+60000 {
		t.Errorf("the recording's clock runs from %d to %d ms, want from just after 36000000 for a minute", g.StartMs, g.EndMs)
	}
	if len(g.Cuts) < 6 || len(g.Cuts) > 10 {
		t.Errorf("a 60-second recording was cut into %d chunks: %v", len(g.Cuts), g.Cuts)
	}
	for i, c := range g.Cuts[1:] {
		if !keys[c] {
			t.Errorf("cut %d at %d ms is not a key frame (key frames: %v)", i+1, c, keys)
		}
		if c-g.Cuts[i] < SegmentSeconds*1000 {
			t.Errorf("chunk %d is only %d ms long", i, c-g.Cuts[i])
		}
	}
	// A run for a chunk of such a file starts one chunk early, so that it
	// has passed a key frame by the time the chunk begins.
	a := strings.Join(args(Spec{Path: src, Grid: g, VideoCodec: "h264", AudioTrack: -1}, 3, 8, 60), " ")
	at := g.Cuts[2] - g.StartMs - 100
	if want := fmt.Sprintf("-ss %d.%03d ", at/1000, at%1000); !strings.Contains(a, want) {
		t.Errorf("a run for chunk 3 should start with %q: %s", want, a)
	}
}

// A film with one key frame in a minute and a half cannot be cut into
// chunks. That must be said, and not answered with a list of one chunk as
// long as the film, which no player could ever be given in time.
func TestAFilmThatCannotBeCutIsNotGivenAListAsIfItCould(t *testing.T) {
	if (Grid{EndMs: 90000, Cuts: []int64{0}}).Valid() || (Grid{EndMs: 200000, Cuts: []int64{0, 6000, 70000, 140000}}).Valid() ||
		(Grid{StartMs: 5000, EndMs: 1205000, Cuts: []int64{5000}}).Valid() {
		t.Error("a grid with a chunk of over a minute was accepted")
	}
	if !(Grid{EndMs: 60000, Cuts: []int64{0}}).Valid() || !(Grid{EndMs: 200000, Cuts: []int64{0, 60000, 120000, 180000}}).Valid() {
		t.Error("a grid with chunks of a minute was refused")
	}
	if u := uniform(36000000, 90000); !u.Valid() || len(u.Cuts) != 15 || u.StartMs != 36000000 || u.Cuts[14] != 36084000 || u.EndMs != 36090000 {
		t.Errorf("an even grid on a clock that starts ten hours in: %+v", u)
	}
	ffmpeg, ffprobe := tools(t)
	src := source(t, ffmpeg, "still")
	g, err := FindCuts(context.Background(), ffprobe, src, 90000)
	if !errors.Is(err, ErrKeyFramesTooFar) {
		t.Fatalf("a film with one key frame: %v %+v", err, g)
	}
	// What comes back with the error is the grid to fall back on.
	if !g.Valid() || g.From != "uniform" || len(g.Cuts) != 15 {
		t.Errorf("the grid to fall back on: %+v", g)
	}
	// A list like that kept from an earlier version is not played from
	// either: the film is cut evenly instead, on the same clock.
	m := manager(t, ffmpeg)
	token := m.Open(Spec{Path: src, Grid: Grid{StartMs: 36000000, EndMs: 36090000, Cuts: []int64{36000000}, From: "probe"}, VideoCodec: "h264", AudioTrack: -1})
	list, _ := m.Playlist(token)
	m.mu.Lock()
	used := m.sessions[token].spec.Grid
	m.mu.Unlock()
	if strings.Count(string(list), "#EXTINF") != 15 || used.StartMs != 36000000 || !used.Valid() {
		t.Errorf("a one-chunk list for 90 seconds was used as it came: %+v\n%s", used, list)
	}
}

// The heart of it: wherever ffmpeg is started, each chunk begins exactly
// where the list said it would, and holds exactly the same frames.
func TestEveryChunkBeginsWhereTheListSays(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	for _, film := range []string{"rough", "open", "uneven", "recording"} {
		src := source(t, ffmpeg, film)
		t.Run(film, func(t *testing.T) {
			t.Parallel() // the films are checked side by side to save time
			g := grid(t, ffprobe, src, 60000)
			codec := inspect(t, ffprobe, src)["video"].Codec
			for _, copySound := range []bool{true, false} {
				everyChunk(t, ffmpeg, ffprobe, Spec{Path: src, Grid: g, VideoCodec: codec, AudioTrack: 0, AudioCopy: copySound}, film != "rough")
			}
		})
	}
}

// firstFrame returns when the first frame stored in a file is shown and
// when it is decoded, in seconds. A chunk's first stored frame is the key
// frame it opens with.
func firstFrame(t *testing.T, ffprobe, path string) (shown, decoded float64) {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-read_intervals", "%+#1",
		"-show_entries", "packet=pts_time,dts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	p := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(p) < 2 {
		t.Fatalf("ffprobe said %q about the first frame of %s", out, path)
	}
	return f(p[0]), f(p[1])
}

// everyChunk fetches each chunk of a film twice, once played through from
// the start and once by a fresh session that asks for it first, and checks
// both against the list.
//
// byKeyFrame is for films in which the time between a key frame being
// decoded and being shown varies. There a chunk is judged by the key frame
// it opens with: frames shown just before a key frame may be stored after
// it and travel with it, and a chunk's sound begins when its key frame is
// decoded, which can be an eighth of a second before it is shown.
func everyChunk(t *testing.T, ffmpeg, ffprobe string, spec Spec, byKeyFrame bool) {
	t.Helper()
	g, copySound := spec.Grid, spec.AudioCopy
	m := manager(t, ffmpeg)
	defer m.Close()
	straight := m.Open(spec)
	whole := fetch(t, m, straight, 0, len(g.Cuts)-1)
	got, want := inspect(t, ffprobe, whole), inspect(t, ffprobe, spec.Path)
	if got["video"].Packets != want["video"].Packets {
		t.Errorf("played straight through: %s frames of %s", got["video"].Packets, want["video"].Packets)
	}
	decodes(t, ffmpeg, whole)
	for k := range g.Cuts {
		through := fetch(t, m, straight, k, k)
		// A fresh session asked for chunk k first: ffmpeg starts there.
		jumped := m.Open(spec)
		direct := fetch(t, m, jumped, k, k)
		m.Stop(jumped)
		a, b := inspect(t, ffprobe, through), inspect(t, ffprobe, direct)
		wantStart := float64(g.Cuts[k])/1000 + shift
		for _, c := range []struct {
			name, path string
			x          map[string]streamInfo
		}{{"played through", through, a}, {"jumped to", direct, b}} {
			start, sound := f(c.x["video"].StartTime), f(c.x["audio"].StartTime)
			early, late := 0.1, 0.1 // how far the sound may begin before and after the picture
			if byKeyFrame {
				var decoded float64
				start, decoded = firstFrame(t, ffprobe, c.path)
				early, late = start-decoded+0.05, 0.05
			}
			if math.Abs(start-wantStart) > 0.002 {
				t.Errorf("copy sound %v: chunk %d %s begins at %.3f s, the list says %.3f", copySound, k, c.name, start, wantStart)
			}
			if off := start - sound; off > early || off < -late {
				t.Errorf("copy sound %v: chunk %d %s: picture and sound start %.3f s apart", copySound, k, c.name, off)
			}
		}
		if a["video"].Packets != b["video"].Packets || copySound && a["audio"].Packets != b["audio"].Packets {
			t.Errorf("copy sound %v: chunk %d differs: played through %s/%s frames, jumped to %s/%s", copySound, k,
				a["video"].Packets, a["audio"].Packets, b["video"].Packets, b["audio"].Packets)
		}
	}
}

// box builds one MP4 box from its type and contents.
func box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, typ...), body...)
}

func u32(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.BigEndian.AppendUint32(b, x)
	}
	return b
}

// The same thing without ffmpeg, on a header and a fragment written out by
// hand with the numbers ffmpeg gave for a film made by x265: the fragment
// is stamped 10.375 s (when its key frame is decoded), and its key frame
// is shown at 10.417 s, which is where the cut is.
func TestAFragmentIsFiledByWhenItsKeyFrameIsShown(t *testing.T) {
	zeros := func(n int) []byte { return make([]byte, n) }
	moov := bytes.Join([][]byte{
		box("mvhd", zeros(100)),
		box("trak", // the sound, listed first to be skipped
			box("tkhd", u32(0, 0, 0, 2), zeros(68)),
			box("mdia", box("mdhd", u32(0, 0, 0, 44100, 0)), box("hdlr", u32(0, 0), []byte("soun"), zeros(13)))),
		box("trak",
			box("tkhd", u32(0, 0, 0, 1), zeros(68)),
			box("edts", box("elst", u32(0, 2, 500, 0xffffffff, 0x10000, 0, 1328, 0x10000))),
			box("mdia", box("mdhd", u32(0, 0, 0, 16000, 0)), box("hdlr", u32(0, 0), []byte("vide"), zeros(13)))),
	}, nil)
	v := findVideo(moov)
	if v.id != 1 || v.scale != 16000 || v.edit != 1328 {
		t.Fatalf("the picture track was read as %+v", v)
	}
	const stamped = 182000 // 10.375 s, plus the second every time is moved by
	moof := bytes.Join([][]byte{
		box("mfhd", u32(0, 2)),
		box("traf",
			box("tfhd", u32(0x020000, 1)),
			box("tfdt", u32(0x01000000, 0, stamped)),
			// Two frames: flags say each has a length, a size, flags of its own
			// and how long after decoding it is shown; the list has a data
			// offset and flags for its first frame.
			box("trun", u32(0x000f05, 2, 0, 0, 666, 5000, 0, 2000, 666, 400, 0, 0))),
		box("traf",
			box("tfhd", u32(0x020000, 2)),
			box("tfdt", u32(0x01000000, 0, 500000)),
			box("trun", u32(0x000201, 1, 0, 300))),
	}, nil)
	ms, ok := fragmentStart(moof, v)
	if !ok || math.Abs(ms-clockShiftMs-10417) > 0.5 {
		t.Fatalf("the fragment's key frame is shown at %.1f ms (%v), want 10417", ms-clockShiftMs, ok)
	}
	g := Grid{EndMs: 60000, Cuts: []int64{0, 10417, 20833}}
	if k := g.index(ms - clockShiftMs); k != 1 {
		t.Errorf("the fragment was filed in chunk %d, want 1", k)
	}
	// A header with no edit list and a fragment whose frames are all shown
	// when they are decoded: the stamp is the time.
	plain := findVideo(box("trak", box("tkhd", u32(0, 0, 0, 7), zeros(68)),
		box("mdia", box("mdhd", u32(0, 0, 0, 1000, 0)), box("hdlr", u32(0, 0), []byte("vide"), zeros(13)))))
	ms, ok = fragmentStart(box("traf", box("tfhd", u32(0, 7)), box("tfdt", u32(0, 4500)), box("trun", u32(0x000201, 1, 0, 300))), plain)
	if !ok || ms != 4500 {
		t.Errorf("a plain fragment: %.1f ms (%v), want 4500", ms, ok)
	}
	// Cut-off or senseless boxes are read as "no information", never a crash.
	for _, junk := range [][]byte{nil, {0}, u32(0x000800), u32(0x000800, 1), u32(0x000f05, 1, 0, 0)} {
		if d := firstShownAfter(junk); d != 0 {
			t.Errorf("junk %x was read as %d", junk, d)
		}
		if d := editStart(junk); d != 0 {
			t.Errorf("junk %x was read as an edit at %d", junk, d)
		}
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
	// AAC that is passed through is rewrapped. Nothing else is: ffmpeg
	// refuses the rewrapping for any other codec, and converted sound is
	// written in the right form to begin with.
	for _, c := range []struct {
		spec Spec
		want bool
	}{
		{Spec{Path: "x", Grid: g, AudioTrack: 0, AudioCopy: true, AudioCodec: "aac"}, true},
		{Spec{Path: "x", Grid: g, AudioTrack: 0, AudioCopy: true, AudioCodec: "ac3"}, false},
		{Spec{Path: "x", Grid: g, AudioTrack: 0, AudioCopy: true}, false},
		{Spec{Path: "x", Grid: g, AudioTrack: 0, AudioCopy: false, AudioCodec: "aac"}, false},
		{Spec{Path: "x", Grid: g, AudioTrack: -1, AudioCopy: true, AudioCodec: "aac"}, false},
	} {
		if got := strings.Contains(strings.Join(args(c.spec, 0, 8, 60), "\x00"), "-bsf:a\x00aac_adtstoasc"); got != c.want {
			t.Errorf("rewrapping of AAC asked for: %v, want %v, for %+v", got, c.want, c.spec)
		}
	}
}
