package subs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ownNames is every name this package may give a file or folder.
var ownNames = regexp.MustCompile(`^(index\.json|fonts|f[0-9]+|[st][0-9]+\.(ass|vtt)|p[0-9]+\.(sup|mks))$`)

// onlyOwnNames fails the test if the folder holds anything not named by
// this package, or anything left over from the making.
func onlyOwnNames(t *testing.T, dir string) (names []string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		names = append(names, filepath.ToSlash(rel))
		if !ownNames.MatchString(d.Name()) {
			t.Errorf("the folder holds %q, which is not a name this package gives", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// One reading of a film with a track of every sort gives one file for
// each, named by its place and its sort, and an index that says what each
// is. Reading the index back gives the same.
func TestOnePassWritesEveryTrack(t *testing.T) {
	tl := tools(t)
	dir, res, percents := extracted(t, tl, madeFilm(t, tl, "five"))
	want := []Track{
		{N: 0, Kind: Styled, Codec: "ass", Language: "eng", Title: "Full", Default: true, File: "s0.ass", FirstMs: 1000, Done: true},
		{N: 1, Kind: Text, Codec: "subrip", Language: "nor", Forced: true, File: "t1.vtt", FirstMs: 500, Done: true},
		{N: 2, Kind: Picture, Codec: "hdmv_pgs_subtitle", Language: "eng", File: "p2.sup", FirstMs: 2000, Width: 1920, Height: 1080, Done: true},
		{N: 3, Kind: Picture, Codec: "dvd_subtitle", Language: "ger", File: "p3.mks", FirstMs: 2000, Width: 1920, Height: 1080, Done: true},
		{N: 4, Kind: Picture, Codec: "dvb_subtitle", File: "p4.mks", FirstMs: 2000, Width: 1920, Height: 1080, Done: true},
	}
	if len(res.Tracks) != len(want) {
		t.Fatalf("%d tracks, want %d: %+v", len(res.Tracks), len(want), res.Tracks)
	}
	for i, got := range res.Tracks {
		st, err := os.Stat(filepath.Join(dir, got.File))
		if err != nil || st.Size() == 0 || st.Size() != got.Size {
			t.Errorf("track %d: its file %q: %v, recorded as %d bytes", i, got.File, err, got.Size)
		}
		got.Size = 0
		if got != want[i] {
			t.Errorf("track %d:\n got %+v\nwant %+v", i, got, want[i])
		}
	}
	if res.DurationMs < 7900 || res.DurationMs > 8200 || res.StartMs < -100 || res.StartMs > 100 {
		t.Errorf("the film is given as %d ms long, starting at %d", res.DurationMs, res.StartMs)
	}
	if len(res.Fonts) != 0 {
		t.Errorf("fonts in a film that has none: %+v", res.Fonts)
	}
	if why := rising(percents); why != "" {
		t.Error(why)
	}
	names := onlyOwnNames(t, dir)
	if len(names) != 7 { // five tracks, the index, and the folder for fonts
		t.Errorf("the folder holds %v", names)
	}
	back, err := Load(dir)
	if err != nil || !reflect.DeepEqual(back, res) {
		t.Errorf("read back: %v\n got %+v\nwant %+v", err, back, res)
	}

	// What is in the files. The styled track is the film's own script: its
	// lines, instructions and all.
	ass, _ := os.ReadFile(filepath.Join(dir, "s0.ass"))
	for _, line := range []string{`Dialogue: 0,0:00:01.00,0:00:02.50,Default,,0,0,0,,Hello, {\i1}world{\i0}`, `{\p1}m 0 0 l 100 0 100 100 0 100{\p0}`, "Style: Sign,Test Font"} {
		if !bytes.Contains(ass, []byte(line)) {
			t.Errorf("the styled track lacks %q:\n%s", line, ass)
		}
	}
	// The text track is in the form browsers show.
	vtt, _ := os.ReadFile(filepath.Join(dir, "t1.vtt"))
	if text := strings.ReplaceAll(string(vtt), "\r", ""); !strings.HasPrefix(text, "WEBVTT\n") ||
		!strings.Contains(text, "00:00.500 --> 00:01.500\nFirst plain line\n") || !strings.Contains(text, "00:04.000 --> 00:05.000\n<i>Second</i> plain line\n") {
		t.Errorf("the text track:\n%s", vtt)
	}
	// The disc's pictures are the film's, piece for piece, at the film's
	// times.
	sup, _ := os.ReadFile(filepath.Join(dir, "p2.sup"))
	if got, want := supPieces(t, sup), supPieces(t, pgs(t, 1920, 1080, twoCaptions)); !reflect.DeepEqual(got, want) {
		t.Errorf("the disc's picture track is not what went into the film: %d pieces, want %d", len(got), len(want))
	}
	// And ffprobe finds in each picture file one track of the right sort
	// that begins where the index says.
	for _, tr := range res.Tracks[2:] {
		got := asked(t, tl, "-show_entries", "packet=pts_time:stream=codec_name", "-read_intervals", "%+#1", "-of", "csv=p=0", filepath.Join(dir, tr.File))
		if len(got) != 2 || got[0] != "2.000000" || got[1] != tr.Codec {
			t.Errorf("ffprobe on %s says %v, want a first caption at 2.000000 in a %s track", tr.File, got, tr.Codec)
		}
	}
}

// A styled track comes with its fonts. Every attachment that is a font is
// written, by its place among the attachments, whatever the film calls it
// and whether or not the film says it is one; what is not a font is not
// kept. The film's own names for things are kept as words and are nowhere
// used as names.
//
// The film has four attachments, and the second is a cover picture. ffmpeg
// does not count a picture among the attachments (it shows it as a still
// "video"), so the fonts are numbers 0 and 1, and number 2 is the one that
// turns out to be nothing.
func TestAStyledTrackComesWithItsFonts(t *testing.T) {
	tl := tools(t)
	dir, res, _ := extracted(t, tl, madeFilm(t, tl, "styled"))
	if len(res.Tracks) != 1 || res.Tracks[0].Kind != Styled || res.Tracks[0].File != "s0.ass" || !res.Tracks[0].Done ||
		res.Tracks[0].Title != hostileTitle || res.Tracks[0].Language != "eng" || !res.Tracks[0].Default || res.Tracks[0].FirstMs != 1000 {
		t.Errorf("the styled track: %+v", res.Tracks)
	}
	want := []Font{
		{K: 0, File: "fonts/f0", Size: int64(len(fontA)), Name: hostileName, Mime: "font/ttf"},
		{K: 1, File: "fonts/f1", Size: int64(len(fontB)), Name: "second", Mime: "application/octet-stream"},
	}
	if !reflect.DeepEqual(res.Fonts, want) {
		t.Errorf("fonts:\n got %+v\nwant %+v", res.Fonts, want)
	}
	for file, content := range map[string][]byte{"fonts/f0": fontA, "fonts/f1": fontB} {
		if got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file))); err != nil || !bytes.Equal(got, content) {
			t.Errorf("%s is not the font the film carries: %v, %d bytes", file, err, len(got))
		}
	}
	names := onlyOwnNames(t, dir)
	if got := strings.Join(names, " "); got != "fonts fonts/f0 fonts/f1 index.json s0.ass" {
		t.Errorf("the folder holds: %s", got)
	}
	if back, err := Load(dir); err != nil || !reflect.DeepEqual(back, res) {
		t.Errorf("read back: %v\n got %+v\nwant %+v", err, back, res)
	}
}

// A track whose first caption comes late says when, for each sort of
// picture track, and that is the time ffprobe finds in the file. The size
// each was made for is read from where that sort keeps it.
func TestALateFirstCaptionIsRecorded(t *testing.T) {
	tl := tools(t)
	dir, res, percents := extracted(t, tl, madeFilm(t, tl, "late"))
	if len(res.Tracks) != 3 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	for i, file := range []string{"p0.sup", "p1.mks", "p2.mks"} {
		tr := res.Tracks[i]
		if tr.File != file || tr.Kind != Picture || !tr.Done || tr.FirstMs != 20000 || tr.Width != 720 || tr.Height != 576 {
			t.Errorf("track %d: %+v, want %s with a first caption at 20000 ms, made for 720 by 576", i, tr, file)
		}
		got := asked(t, tl, "-show_entries", "packet=pts_time", "-read_intervals", "%+#1", "-of", "csv=p=0", filepath.Join(dir, file))
		if len(got) != 1 || got[0] != "20.000000" {
			t.Errorf("ffprobe finds the first caption of %s at %v", file, got)
		}
		// ffprobe knows the size for two of the three sorts; where it
		// does, it agrees.
		size := asked(t, tl, "-show_entries", "stream=width,height", "-of", "csv=p=0", filepath.Join(dir, file))
		if len(size) == 1 && size[0] != "720,576" && size[0] != "N/A,N/A" && size[0] != "," && size[0] != "0,0" {
			t.Errorf("ffprobe gives the size of %s as %v", file, size)
		}
		facts, err := ReadPicture(filepath.Join(dir, file))
		if err != nil || facts != (PictureFacts{Width: 720, Height: 576, FirstMs: 20000}) {
			t.Errorf("ReadPicture(%s) = %+v, %v", file, facts, err)
		}
	}
	// A minute of film is long enough for the reading to be seen on its
	// way: the sound's clock passes every hundredth of it.
	if why := rising(percents); why != "" || len(percents) < 50 || percents[0] > 2 {
		t.Errorf("progress: %s %v", why, percents)
	}
}

// What a later step relies on when it has ffmpeg begin reading a picture
// track somewhere in the middle: where ffmpeg counts from. In a ".sup"
// file that is the first caption, in a ".mks" file zero, and JumpBaseMs
// says which.
func TestAJumpIntoAPictureTrackCountsFromItsBase(t *testing.T) {
	tl := tools(t)
	dir, res, _ := extracted(t, tl, madeFilm(t, tl, "late"))
	const wantMs = 45000 // captions are at 20, 24, 28 ... 56 seconds, each 2 seconds long
	for _, tr := range res.Tracks {
		if base, sup := tr.JumpBaseMs(), strings.HasSuffix(tr.File, ".sup"); base != map[bool]int64{true: 20000, false: 0}[sup] {
			t.Errorf("%s: jumps are said to count from %d ms", tr.File, base)
		}
		at := wantMs - tr.JumpBaseMs()
		// Where ffmpeg lands: the time of the first piece it passes on.
		cmd := exec.Command(tl.FFmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-ss", strconv.FormatInt(at/1000, 10), "-copyts", "-i", filepath.Join(dir, tr.File),
			"-map", "0", "-c", "copy", "-f", "mkvtimestamp_v2", "pipe:1")
		out, err := cmd.Output()
		lines := strings.Fields(strings.ReplaceAll(string(out), "\r", ""))
		if err != nil || len(lines) < 5 {
			t.Errorf("%s: reading from %d s: %v %q", tr.File, at/1000, err, out)
			continue
		}
		// It lands on a caption a little before the moment asked for,
		// never after it and never back at the start.
		if landed, _ := strconv.ParseInt(lines[4], 10, 64); landed > wantMs || landed < wantMs-6000 {
			t.Errorf("%s: asked for %d ms of the film by way of -ss %d, ffmpeg began at %d", tr.File, wantMs, at/1000, landed)
		}
	}
}

// A TV recording's clock starts hours in. The files keep the film's own
// times, the index says where the clock starts, and progress is counted
// from there, not from zero.
func TestTimesAreOnTheFilmsOwnClock(t *testing.T) {
	tl := tools(t)
	path := madeFilm(t, tl, "recording")
	dir, res, percents := extracted(t, tl, path)
	if res.StartMs < 36000000 || res.StartMs > 36003000 || res.DurationMs < 63000 || res.DurationMs > 66000 {
		t.Errorf("the recording is given as %d ms long, starting at %d", res.DurationMs, res.StartMs)
	}
	if len(res.Tracks) != 1 || res.Tracks[0].File != "p0.mks" || !res.Tracks[0].Done || res.Tracks[0].Width != 720 || res.Tracks[0].Height != 576 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	// The first caption is where the recording itself has it.
	inFilm := asked(t, tl, "-select_streams", "s:0", "-show_entries", "packet=pts_time", "-read_intervals", "%+#1", "-of", "csv=p=0", path)
	inFile := asked(t, tl, "-show_entries", "packet=pts_time", "-read_intervals", "%+#1", "-of", "csv=p=0", filepath.Join(dir, "p0.mks"))
	if len(inFilm) != 1 || len(inFile) != 1 || inFilm[0] != inFile[0] {
		t.Fatalf("the first caption is at %v in the recording and at %v in the file taken from it", inFilm, inFile)
	}
	sec, _ := strconv.ParseFloat(inFilm[0], 64)
	if first := res.Tracks[0].FirstMs; first < int64(sec*1000)-1 || first > int64(sec*1000)+1 || first-res.StartMs < 19000 || first-res.StartMs > 21000 {
		t.Errorf("the first caption is recorded at %d ms; the recording has it at %s s, and starts at %d ms", first, inFilm[0], res.StartMs)
	}
	if why := rising(percents); why != "" || len(percents) < 50 || percents[0] > 2 {
		t.Errorf("progress: %s %v", why, percents)
	}
}

// A script may go on after its captions and stop without the end of a
// line. One converter writes that out as it finds it, the other adds the
// line end; either way the track is whole, and what comes after the
// captions is not taken for one.
func TestAScriptThatGoesOnAfterItsCaptions(t *testing.T) {
	tl := tools(t)
	dir, res, _ := extracted(t, tl, madeFilm(t, tl, "tail"))
	if len(res.Tracks) != 1 || res.Tracks[0].File != "s0.ass" || !res.Tracks[0].Done || res.Tracks[0].FirstMs != 1000 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "s0.ass"))
	if err != nil || !bytes.Contains(raw, []byte("[Aegisub Extradata]")) || !bytes.Contains(raw, []byte("keeps for itself")) {
		t.Fatalf("the track as written: %v\n%s", err, raw)
	}
	t.Logf("the track ends with the end of a line: %v", bytes.HasSuffix(raw, []byte("\n")))
	want := []Cue{{StartMs: 1000, EndMs: 2000, Text: "One"}, {StartMs: 3000, EndMs: 4000, Text: "Two"}}
	if got := Flatten(raw); !reflect.DeepEqual(got, want) {
		t.Errorf("flattened: %+v", got)
	}
}

// Films have tracks with nothing in them (a "forced" track for a film in
// which nobody speaks a foreign language). Such a track is written like
// any other and says that it has no first caption.
func TestTracksWithoutCaptions(t *testing.T) {
	tl := tools(t)
	dir, res, _ := extracted(t, tl, madeFilm(t, tl, "hollow"))
	if len(res.Tracks) != 5 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	for i, file := range []string{"s0.ass", "t1.vtt", "p2.sup", "p3.mks", "p4.mks"} {
		if tr := res.Tracks[i]; tr.File != file || !tr.Done || tr.FirstMs != -1 {
			t.Errorf("track %d: %+v, want %s, done, without a first caption", i, tr, file)
		}
	}
	// A DVD's track says its size in its heading, captions or none.
	if tr := res.Tracks[3]; tr.Width != 1920 || tr.Height != 1080 {
		t.Errorf("the DVD track's size: %d by %d", tr.Width, tr.Height)
	}
	if back, err := Load(dir); err != nil || !reflect.DeepEqual(back, res) {
		t.Errorf("read back: %v\n got %+v\nwant %+v", err, back, res)
	}
	if got := Flatten(mustRead(t, filepath.Join(dir, "s0.ass"))); len(got) != 0 {
		t.Errorf("captions from a track that has none: %+v", got)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The sort of text track MP4 files have comes out like any other.
func TestATextTrackFromAnMP4(t *testing.T) {
	tl := tools(t)
	dir, res, _ := extracted(t, tl, madeFilm(t, tl, "phone"))
	if len(res.Tracks) != 1 || res.Tracks[0].Kind != Text || res.Tracks[0].Codec != "mov_text" || res.Tracks[0].File != "t0.vtt" ||
		res.Tracks[0].Language != "fra" || res.Tracks[0].FirstMs != 500 || !res.Tracks[0].Done {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	vtt, _ := os.ReadFile(filepath.Join(dir, "t0.vtt"))
	if text := strings.ReplaceAll(string(vtt), "\r", ""); !strings.Contains(text, "00:00.500 --> 00:01.500\nFirst plain line\n") {
		t.Errorf("the text track:\n%s", vtt)
	}
}

// A film without sound is followed by its picture. (Its two text tracks
// are in different formats, and come out in the same one.)
func TestAFilmWithoutSoundIsFollowedByItsPicture(t *testing.T) {
	tl := tools(t)
	dir, res, percents := extracted(t, tl, madeFilm(t, tl, "silent"))
	if len(res.Tracks) != 2 || res.Tracks[0].Codec != "subrip" || res.Tracks[1].Codec != "webvtt" {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	for i, file := range []string{"t0.vtt", "t1.vtt"} {
		if tr := res.Tracks[i]; tr.File != file || tr.Kind != Text || !tr.Done || tr.FirstMs != 500 {
			t.Errorf("track %d: %+v", i, tr)
		}
		if text := strings.ReplaceAll(string(mustRead(t, filepath.Join(dir, file))), "\r", ""); !strings.HasPrefix(text, "WEBVTT\n") ||
			!strings.Contains(text, "00:00.500 --> 00:01.500\nFirst plain line\n") || !strings.Contains(text, "00:04.000 --> 00:05.000\n") {
			t.Errorf("%s:\n%s", file, text)
		}
	}
	if why := rising(percents); why != "" || len(percents) < 50 {
		t.Errorf("progress: %s %v", why, percents)
	}
}

// A file that holds nothing but subtitles has nothing to follow the
// reading by; it is read all the same, and says so when it is done.
func TestAFileOfSubtitlesAlone(t *testing.T) {
	tl := tools(t)
	whole := pgs(t, 1920, 1080, twoCaptions)
	dir, res, percents := extracted(t, tl, written(t, "alone.sup", whole))
	if len(res.Tracks) != 1 || res.Tracks[0].File != "p0.sup" || !res.Tracks[0].Done || res.Tracks[0].FirstMs != 2000 || res.Tracks[0].Width != 1920 || res.Tracks[0].Height != 1080 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	if !reflect.DeepEqual(supPieces(t, mustRead(t, filepath.Join(dir, "p0.sup"))), supPieces(t, whole)) {
		t.Error("the track that came out is not the one that went in")
	}
	if !reflect.DeepEqual(percents, []int{100}) {
		t.Errorf("progress: %v", percents)
	}
}

// A film with no subtitles and no fonts is not read at all: the converter
// named here does not exist, and is never started. Its folder still gets
// an index, so that nobody asks again.
func TestAFilmWithNothingToTakeIsNotRead(t *testing.T) {
	tl := tools(t)
	tl.FFmpeg = filepath.Join(t.TempDir(), "no-such-converter")
	dir, res, percents := extracted(t, tl, madeFilm(t, tools(t), "bare"))
	if len(res.Tracks) != 0 || len(res.Fonts) != 0 || res.DurationMs < 5900 {
		t.Errorf("result: %+v", res)
	}
	if !reflect.DeepEqual(percents, []int{100}) {
		t.Errorf("progress: %v", percents)
	}
	if back, err := Load(dir); err != nil || !reflect.DeepEqual(back, res) {
		t.Errorf("read back: %+v, %v", back, err)
	}
}

// A film with fonts and no subtitles gives up its fonts without being
// read through.
func TestFontsAloneNeedNoReading(t *testing.T) {
	tl := tools(t)
	dir, res, percents := extracted(t, tl, madeFilm(t, tl, "fonts"))
	if len(res.Tracks) != 0 || len(res.Fonts) != 1 || res.Fonts[0] != (Font{K: 0, File: "fonts/f0", Size: int64(len(fontA)), Name: "Only.ttf", Mime: "font/ttf"}) {
		t.Errorf("result: %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "fonts", "f0")); !bytes.Equal(got, fontA) {
		t.Errorf("the font is not the film's: %d bytes", len(got))
	}
	if !reflect.DeepEqual(percents, []int{100}) {
		t.Errorf("progress: %v", percents)
	}
}

// The twin of the test of the same name in internal/hls: every argument
// but the film's path is made of numbers and of words from this package,
// whatever the film says about itself.
func TestCommandLineIsBuiltOnlyFromTypedValues(t *testing.T) {
	path := `C:\Films\A "quoted"; rm -rf, [in] 'x' -map 0.mkv`
	plain := regexp.MustCompile(`^[A-Za-z0-9_.:/-]+$`)
	hostile := `ass -f null; shutdown "now" /etc/passwd`
	seen := inventory{durationMs: 7200000, clock: clockSound}
	for n, codec := range []string{"ass", "subrip", "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "mov_text", "ssa", "webvtt", "dvb_teletext", "eia_608", hostile, ""} {
		seen.tracks = append(seen.tracks, Track{N: n, Codec: codec, Kind: KindOf(codec), Language: hostile, Title: hostile})
	}
	seen.attachments = []attachment{{name: hostile + ".ttf", mime: "font/ttf"}, {name: "cover.jpg", mime: "image/jpeg"}, {name: "x", mime: hostile}, {name: `..\..\evil.otf`, mime: hostile}, {}}
	for _, c := range []clock{clockSound, clockPicture, clockNone} {
		seen.clock = c
		a := plan(seen).args(path)
		paths := 0
		for i, x := range a {
			switch {
			case x == path:
				// One argument, exactly as given, right after -i.
				if paths++; a[i-1] != "-i" {
					t.Errorf("the path follows %q", a[i-1])
				}
			case !plain.MatchString(x):
				t.Errorf("the argument %q is not made of plain words and numbers", x)
			case strings.Contains(x, "Films") || strings.Contains(x, "passwd") || strings.Contains(x, "shutdown") || strings.Contains(x, "null") || strings.Contains(x, "evil") || strings.Contains(x, "cover"):
				t.Errorf("the argument %q holds text from outside", x)
			}
		}
		if paths != 1 {
			t.Errorf("the path is given %d times: %q", paths, a)
		}
		joined := "\x00" + strings.Join(a, "\x00") + "\x00"
		for _, want := range []string{
			"\x00-copyts\x00-i\x00", "\x00-nostdin\x00", "\x00-y\x00",
			"-dump_attachment:t:0\x00fonts/f0\x00", "-dump_attachment:t:3\x00fonts/f3\x00", "-dump_attachment:t:4\x00fonts/f4\x00",
			"-map\x000:s:0\x00-c\x00copy\x00-f\x00ass\x00s0.ass\x00",
			"-map\x000:s:1\x00-c\x00webvtt\x00-f\x00webvtt\x00t1.vtt\x00",
			"-map\x000:s:2\x00-c\x00copy\x00-f\x00sup\x00p2.sup\x00",
			"-map\x000:s:3\x00-c\x00copy\x00-map_metadata\x00-1\x00-map_chapters\x00-1\x00-f\x00matroska\x00p3.mks\x00",
			"\x00p4.mks\x00", "\x00t5.vtt\x00", "\x00s6.ass\x00", "\x00t7.vtt\x00",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing %q in %q", strings.ReplaceAll(want, "\x00", " "), a)
			}
		}
		// What is not a font by its own account is not written, and a sort
		// of track that cannot be used is not asked for.
		for _, not := range []string{"fonts/f1\x00", "fonts/f2\x00", "0:s:8\x00", "0:s:9\x00", "0:s:10\x00", "0:s:11\x00", "\x00-t\x00"} {
			if strings.Contains(joined, not) {
				t.Errorf("%q should not be in %q", strings.ReplaceAll(not, "\x00", " "), a)
			}
		}
		clockArgs := map[clock]string{clockSound: "-map\x000:a:0\x00-c\x00copy\x00-f\x00mkvtimestamp_v2\x00-flush_packets\x001\x00pipe:1\x00",
			clockPicture: "-map\x000:V:0\x00-c\x00copy\x00-f\x00mkvtimestamp_v2\x00-flush_packets\x001\x00pipe:1\x00"}[c]
		if got := strings.Contains(joined, "pipe:1"); got != (c != clockNone) || !strings.HasSuffix(joined, clockArgs) {
			t.Errorf("the clock for %v: %q", c, a)
		}
	}
	// With only fonts to fetch, none of the film is read.
	fontsOnly := inventory{clock: clockSound, attachments: []attachment{{name: "a.ttf"}}}
	if a := "\x00" + strings.Join(plan(fontsOnly).args(path), "\x00"); !strings.Contains(a, "\x00-t\x000\x00-f\x00mkvtimestamp_v2") {
		t.Errorf("fonts only: %q", a)
	}
	// With nothing to fetch, or fonts in a file with nothing to keep time
	// by, ffmpeg is not run.
	for _, f := range []inventory{{clock: clockSound}, {clock: clockNone, attachments: []attachment{{name: "a.ttf"}}}, {clock: clockSound, tracks: []Track{{Codec: "dvb_teletext"}}}} {
		if plan(f).runs() {
			t.Errorf("ffmpeg would be run for %+v", f)
		}
	}
}

// Windows takes a command line of at most 32,767 characters. However many
// tracks and fonts a file claims to have, the command stays well inside
// that, with a file name as long as Windows ordinarily allows.
func TestTheLongestCommandLineFitsOnWindows(t *testing.T) {
	path := `C:\` + strings.Repeat("a", 259-len(`C:\.mkv`)) + ".mkv"
	var seen inventory
	seen.clock = clockSound
	for n := 0; n < 5000; n++ {
		seen.tracks = append(seen.tracks, Track{N: n, Codec: "dvd_subtitle"})
		seen.attachments = append(seen.attachments, attachment{name: "f.ttf"})
	}
	j := plan(seen)
	a := j.args(path)
	length := len(a) // a space after each, and room for quotes
	for _, x := range a {
		length += len(x) + 2
	}
	if j.outputs() != maxTracks || len(j.fonts) != maxFonts || length > 30000 {
		t.Errorf("%d tracks and %d fonts are asked for in a command of %d characters", j.outputs(), len(j.fonts), length)
	}
	// The tracks that were left are said to be not done.
	if last := j.forms[len(j.forms)-1]; last != formNone {
		t.Errorf("track 4999 would be written as %v", last)
	}
}

// What follows uses the stand-in, and so runs everywhere.

// A good run: the reports follow the stand-in's clock (0, 10, 50 and 99.9
// seconds of a film of 100), and 100 comes only when all is in place.
func TestProgressFollowsTheClock(t *testing.T) {
	tl := standInTools(t, "ok")
	dir, res, percents := extracted(t, tl, "any.mkv")
	if !reflect.DeepEqual(percents, []int{0, 10, 50, 99, 100}) {
		t.Errorf("progress: %v", percents)
	}
	if len(res.Tracks) != 3 || !res.Tracks[0].Done || !res.Tracks[1].Done || !res.Tracks[2].Done || len(res.Fonts) != 1 ||
		res.Tracks[0].FirstMs != 1000 || res.Tracks[1].FirstMs != 500 || res.Tracks[2].FirstMs != 2000 || res.Tracks[2].Width != 1920 {
		t.Errorf("result: %+v", res)
	}
	if got := strings.Join(onlyOwnNames(t, dir), " "); got != "fonts fonts/f0 index.json p2.sup s0.ass t1.vtt" {
		t.Errorf("the folder holds: %s", got)
	}
}

// nothingKept fails the test unless the folder is as a run that did not
// finish must leave it: no index, nothing of the making, and Load saying
// so.
func nothingKept(t *testing.T, dir string) {
	t.Helper()
	if _, err := Load(dir); !errors.Is(err, ErrNotExtracted) {
		t.Errorf("Load after a run that did not finish: %v", err)
	}
	for _, name := range []string{indexName, indexName + ".tmp", workName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there: %v", name, err)
		}
	}
}

// When the caller's context ends, ffmpeg is stopped (the stand-in would
// otherwise never end) and nothing is kept. While it runs, a second run
// for the same folder is refused.
func TestStoppingEndsTheConverterAndKeepsNothing(t *testing.T) {
	tl := standInTools(t, "hang")
	dir := filepath.Join(t.TempDir(), "subs")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	running := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		first := true
		_, err := Extract(ctx, tl, "any.mkv", dir, func(p int) {
			if first {
				first = false
				close(running)
			}
		})
		done <- err
	}()
	select {
	case <-running:
	case err := <-done:
		t.Fatalf("the run ended before it reported anything: %v", err)
	case <-time.After(time.Minute):
		t.Fatal("the stand-in never reported")
	}
	// (Were it not refused it would hang like the first, so it is given an
	// end of its own.)
	second, enough := context.WithTimeout(context.Background(), 20*time.Second)
	if _, err := Extract(second, tl, "any.mkv", dir, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("a second run for the same folder: %v", err)
	}
	enough()
	if _, err := os.Stat(filepath.Join(dir, workName, "p2.sup")); err != nil {
		t.Errorf("the stand-in should have written into the working folder by now: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("stopped: %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("Extract did not return after its context ended")
	}
	nothingKept(t, dir)
	// A context that has already ended starts nothing.
	if _, err := Extract(ctx, tl, "any.mkv", dir, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("with a context that had ended: %v", err)
	}
	nothingKept(t, dir)
}

// When ffmpeg fails, nothing is kept, although files were written; an
// earlier result in the same folder is not left looking good; and the
// error names the film without the folder it is in.
func TestAFailedRunLeavesNoResult(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subs")
	if _, err := Extract(context.Background(), standInTools(t, "ok"), "any.mkv", dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(t.TempDir(), "Household Videos")
	_, err := Extract(context.Background(), standInTools(t, "fail"), filepath.Join(library, "Test Film (2020).mkv"), dir, nil)
	if err == nil || !strings.Contains(err.Error(), "Test Film (2020).mkv") || strings.Contains(err.Error(), "Household") || strings.Contains(err.Error(), filepath.Dir(library)) {
		t.Errorf("the error should name the film and not its folder: %v", err)
	}
	nothingKept(t, dir)
}

// ffmpeg ending well is not enough: every file has to be there and
// complete. A file cut short, as a full disk leaves it, makes the whole
// run count for nothing.
func TestAFileCutShortIsNotAResult(t *testing.T) {
	for _, how := range []string{"cut-sup", "cut-ass", "cut-vtt", "no-sup"} {
		t.Run(how, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "subs")
			var percents []int
			_, err := Extract(context.Background(), standInTools(t, how), "any.mkv", dir, func(p int) { percents = append(percents, p) })
			if err == nil || !strings.Contains(err.Error(), "was not written whole") {
				t.Errorf("error: %v", err)
			}
			if len(percents) > 0 && percents[len(percents)-1] == 100 {
				t.Errorf("a run that failed reported 100: %v", percents)
			}
			nothingKept(t, dir)
		})
	}
	// An attachment with nothing in it is no font, and no reason to fail.
	dir := filepath.Join(t.TempDir(), "subs")
	res, err := Extract(context.Background(), standInTools(t, "cut-font"), "any.mkv", dir, nil)
	if err != nil || len(res.Fonts) != 0 || len(res.Tracks) != 3 {
		t.Errorf("with an empty attachment: %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fonts", "f0")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the empty attachment was kept: %v", err)
	}
}

// The real thing: ffmpeg writing to a disk that is full. Linux has a file
// that behaves like one ("/dev/full": every write to it fails for lack of
// space), and a link to it is put where ffmpeg is about to write.
func TestAFullDiskLeavesNoResult(t *testing.T) {
	tl := tools(t)
	if runtime.GOOS != "linux" {
		t.Skip("needs /dev/full, which only Linux has")
	}
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("this machine has no /dev/full")
	}
	path := madeFilm(t, tl, "five")
	for _, name := range []string{"s0.ass", "t1.vtt", "p2.sup", "p3.mks"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "subs")
			e := &extraction{tools: tl, film: path, dir: dir, prepared: func(work string) {
				if err := os.Symlink("/dev/full", filepath.Join(work, name)); err != nil {
					t.Error(err)
				}
			}}
			_, err := e.run(context.Background())
			if err == nil {
				t.Error("a run whose disk was full for " + name + " counted")
			} else if strings.Contains(err.Error(), filepath.Dir(path)) {
				t.Errorf("the error names the film's folder: %v", err)
			}
			nothingKept(t, dir)
		})
	}
	// The same for a font, in the film that has them.
	dir := filepath.Join(t.TempDir(), "subs")
	e := &extraction{tools: tl, film: madeFilm(t, tl, "styled"), dir: dir, prepared: func(work string) {
		if err := os.Symlink("/dev/full", filepath.Join(work, "fonts", "f0")); err != nil {
			t.Error(err)
		}
	}}
	if _, err := e.run(context.Background()); err == nil {
		t.Error("a run whose disk was full for a font counted")
	}
	nothingKept(t, dir)
}

// Load trusts a folder only as far as it can check: the index must be this
// version's, every file it lists must be there at its length, and every
// name in it must be one this package gives.
func TestLoadRefusesAFolderThatIsNotWhole(t *testing.T) {
	fresh := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "subs")
		if _, err := Extract(context.Background(), standInTools(t, "ok"), "any.mkv", dir, nil); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	rewrite := func(t *testing.T, dir string, change func(idx map[string]any)) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, indexName))
		if err != nil {
			t.Fatal(err)
		}
		var idx map[string]any
		if err := json.Unmarshal(b, &idx); err != nil {
			t.Fatal(err)
		}
		change(idx)
		b, _ = json.Marshal(idx)
		if err := os.WriteFile(filepath.Join(dir, indexName), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	track := func(idx map[string]any, n int) map[string]any { return idx["tracks"].([]any)[n].(map[string]any) }
	for name, spoil := range map[string]func(t *testing.T, dir string){
		"a track's file is gone":  func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, "p2.sup")) },
		"a track's file is short": func(t *testing.T, dir string) { os.Truncate(filepath.Join(dir, "s0.ass"), 100) },
		"a font is gone":          func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, "fonts", "f0")) },
		"the index is gone":       func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, indexName)) },
		"the index is cut short":  func(t *testing.T, dir string) { os.Truncate(filepath.Join(dir, indexName), 40) },
		"another version's index": func(t *testing.T, dir string) {
			rewrite(t, dir, func(idx map[string]any) { idx["version"] = indexVersion + 1 })
		},
		"a name from somewhere": func(t *testing.T, dir string) {
			rewrite(t, dir, func(idx map[string]any) { track(idx, 0)["file"] = "../s0.ass" })
		},
		"another track's file": func(t *testing.T, dir string) {
			rewrite(t, dir, func(idx map[string]any) { track(idx, 0)["file"] = "t1.vtt" })
		},
		"a font from somewhere": func(t *testing.T, dir string) {
			rewrite(t, dir, func(idx map[string]any) { idx["fonts"].([]any)[0].(map[string]any)["file"] = "index.json" })
		},
		"tracks in another order": func(t *testing.T, dir string) { rewrite(t, dir, func(idx map[string]any) { track(idx, 1)["n"] = 5 }) },
		"a file said to be undone": func(t *testing.T, dir string) {
			rewrite(t, dir, func(idx map[string]any) { track(idx, 2)["done"] = false })
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := fresh(t)
			if _, err := Load(dir); err != nil {
				t.Fatal(err)
			}
			spoil(t, dir)
			if res, err := Load(dir); !errors.Is(err, ErrNotExtracted) {
				t.Errorf("Load: %+v, %v", res, err)
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "never made")); !errors.Is(err, ErrNotExtracted) {
		t.Errorf("Load of a folder that is not there: %v", err)
	}
}

// What ffprobe says about a film is read with care: tracks are numbered in
// the file's order whatever sort they are, a cover picture is not the
// film's picture, and what the film says about itself is cut to a length
// worth keeping.
func TestReadingWhatAFilmHolds(t *testing.T) {
	long := strings.Repeat("x", 5000)
	f, err := parseInspection([]byte(`{"format": {"start_time": "-0.023000", "duration": "5400.500000"}, "streams": [
	 {"codec_type": "video", "codec_name": "mjpeg", "disposition": {"attached_pic": 1}},
	 {"codec_type": "subtitle", "codec_name": "dvb_teletext", "tags": {"language": "und"}},
	 {"codec_type": "subtitle", "codec_name": "ass", "tags": {"LANGUAGE": "ENG", "TITLE": "` + long + `"}, "disposition": {"default": 1, "hearing_impaired": 1}},
	 {"codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle", "tags": {"title": "Director's Commentary"}, "disposition": {"forced": 1}},
	 {"codec_type": "attachment", "codec_name": "ttf", "tags": {"filename": "A.TTF", "mimetype": "application/x-truetype-font"}},
	 {"codec_type": "data", "codec_name": "bin_data"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.durationMs != 5400500 || f.startMs != -23 || f.clock != clockNone || len(f.tracks) != 3 || len(f.attachments) != 1 {
		t.Fatalf("read as %+v", f)
	}
	want := []Track{
		{N: 0, Kind: Other, Codec: "dvb_teletext", FirstMs: -1},
		{N: 1, Kind: Styled, Codec: "ass", Language: "eng", Title: long[:200], Default: true, HearingImpaired: true, FirstMs: -1},
		{N: 2, Kind: Picture, Codec: "hdmv_pgs_subtitle", Title: "Director's Commentary", Forced: true, Commentary: true, FirstMs: -1},
	}
	if !reflect.DeepEqual(f.tracks, want) {
		t.Errorf("tracks:\n got %+v\nwant %+v", f.tracks, want)
	}
	if _, err := parseInspection([]byte("not what ffprobe writes")); err == nil {
		t.Error("nonsense was read as a film")
	}
	for codec, want := range map[string]Kind{"ass": Styled, "ssa": Styled, "subrip": Text, "mov_text": Text, "webvtt": Text, "text": Text,
		"hdmv_pgs_subtitle": Picture, "dvd_subtitle": Picture, "dvb_subtitle": Picture, "dvb_teletext": Other, "eia_608": Other, "": Other, "h264": Other} {
		if got := KindOf(codec); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", codec, got, want)
		}
	}
	// Which attachments are worth writing out to look at.
	for a, want := range map[attachment]bool{
		{name: "Arial.TTF", mime: "application/octet-stream"}: true, {name: "x", mime: "font/otf"}: true, {name: "x", mime: "application/vnd.ms-opentype"}: true,
		{name: "x", mime: "application/x-truetype-font"}: true, {name: "", mime: ""}: true, {name: "x.bin", mime: "application/octet-stream"}: true,
		{name: "cover.jpg", mime: "image/jpeg"}: false, {name: "notes.txt", mime: "text/plain"}: false, {name: "x", mime: "application/pdf"}: false,
	} {
		if got := a.mayBeFont(); got != want {
			t.Errorf("%+v worth looking at: %v, want %v", a, got, want)
		}
	}
}
