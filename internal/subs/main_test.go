package subs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
	"github.com/Matiaspnyhus/Local-media-project/internal/subs/substest"
)

// Most of these tests run the real ffmpeg on small films made for the
// purpose, with the converter under test:
//
//	styled: 6 seconds with one styled track and four attachments: a font
//	        that says it is one, a cover picture, a font that does not say
//	        what it is, and something that is neither.
//	five:   8 seconds with five tracks, one of every sort a film can
//	        carry: styled, plain text, and pictures as on Blu-ray discs
//	        (PGS), on DVDs (VobSub) and in TV broadcasts (DVB).
//	late:   64 seconds with three picture tracks whose first caption comes
//	        20 seconds in.
//	recording: the late film as a TV recording keeps it (MPEG-TS), with a
//	        clock that starts ten hours in.
//	phone:  6 seconds as MP4 with the kind of text track MP4 files have.
//	streamed: the same as phone, with the list of what the file holds at
//	        its beginning (as files made for playing over a network have
//	        it), so that it can still be opened when its end is missing.
//	live:   6 seconds with a styled track, as a program that records what
//	        is being broadcast writes it: the file does not say how large
//	        or how long it is.
//	silent: 6 seconds of picture without sound, and two text tracks in the
//	        two text formats MKV files mostly hold.
//	bare:   6 seconds with no subtitles at all.
//	fonts:  6 seconds with a font and no subtitles.
//	hollow: the first four tenths of a second of five: all its tracks, and
//	        not one caption in any of them.
//	tail:   5 seconds with a styled track whose script goes on after its
//	        captions (programs for writing such scripts keep notes of
//	        their own there) and does not end with the end of a line.
//
// The rest use a stand-in for ffmpeg and ffprobe: this test program itself,
// started again with stand-in set, which then behaves as told (see
// standIn). They run on a machine that has neither.

// TestMain is where the stand-in takes over when this program was started
// as one.
func TestMain(m *testing.M) {
	if how := os.Getenv(standInVar); how != "" {
		standIn(how)
		return
	}
	code := m.Run()
	if filmDir != "" {
		os.RemoveAll(filmDir) // this run's own temporary folder
	}
	os.Exit(code)
}

func tools(t *testing.T) Tools {
	t.Helper()
	ffmpeg, ffprobe := probe.FindFFmpeg(), probe.Find()
	if ffmpeg == "" || ffprobe == "" {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg and ffprobe were not found, and LM_REQUIRE_FFMPEG says they must be")
		}
		t.Skip("ffmpeg and ffprobe are not installed here")
	}
	return Tools{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

// What the test films are made of.

// script is a styled track with what real ones have: a spoken line with an
// instruction in the middle and a comma in it, a sign pinned to a place, a
// drawing, and a line that is the maker's note.
const script = `[Script Info]
ScriptType: v4.00+
PlayResX: 384
PlayResY: 288

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Test Font,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,2,2,10,10,10,1
Style: Sign,Test Font,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,2,8,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:02.50,Default,,0,0,0,,Hello, {\i1}world{\i0}
Comment: 0,0:00:02.00,0:00:03.00,Default,,0,0,0,,a note to myself
Dialogue: 0,0:00:03.00,0:00:04.00,Sign,,0,0,0,,{\an8\pos(100,50)}Bakery
Dialogue: 1,0:00:03.00,0:00:04.00,Sign,,0,0,0,,{\p1}m 0 0 l 100 0 100 100 0 100{\p0}
`

// scriptWithTail has a section after its captions, and no line end at its
// very end.
const scriptWithTail = `[Script Info]
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Test Font,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,2,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,One
Dialogue: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,Two

[Aegisub Extradata]
Data: 0,e,a note the program keeps for itself`

const plain = `1
00:00:00,500 --> 00:00:01,500
First plain line

2
00:00:04,000 --> 00:00:05,000
<i>Second</i> plain line
`

// Things a film can say about itself that must never reach a command line
// or become a file name.
const (
	hostileTitle = `Styled, "full"; rm -rf / [x] 'y' -vf movie=/etc/passwd`
	hostileName  = `../../Evil Name.ttf`
)

var (
	// Stand-ins for fonts: what a font file begins with, and filler. No
	// program is asked to draw with them.
	fontA  = append([]byte{0, 1, 0, 0}, bytes.Repeat([]byte("a font that says it is one. "), 40)...)
	fontB  = append([]byte("OTTO"), bytes.Repeat([]byte("a font that does not say what it is. "), 40)...)
	cover  = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("a cover picture. "), 40)...)
	unsure = bytes.Repeat([]byte("neither a font nor anything else. "), 40)
)

// The picture tracks. Two captions on a Blu-ray's size of picture, and ten
// on a DVD's that begin late.
var (
	twoCaptions = []substest.Caption{
		{StartMs: 2000, EndMs: 3500, X: 200, Y: 800, W: 600, H: 100, Colour: color.NRGBA{255, 0, 0, 255}},
		{StartMs: 4000, EndMs: 5000, X: 1000, Y: 100, W: 400, H: 200, Colour: color.NRGBA{0, 200, 50, 255}},
	}
	lateCaptions = func() (list []substest.Caption) {
		for i := int64(0); i < 10; i++ {
			list = append(list, substest.Caption{StartMs: 20000 + i*4000, EndMs: 22000 + i*4000, X: 40 + 10*int(i), Y: 400, W: 200, H: 60, Colour: color.NRGBA{255, 255, 0, 255}})
		}
		return list
	}()
)

var films = map[string]string{}
var filmDir string

// madeFilm returns the path of a test film, making it the first time.
func madeFilm(t *testing.T, tl Tools, name string) string {
	t.Helper()
	if p := films[name]; p != "" {
		return p
	}
	if filmDir == "" {
		dir, err := os.MkdirTemp("", "lm-subs-src-")
		if err != nil {
			t.Fatal(err)
		}
		filmDir = dir
		for file, content := range map[string][]byte{
			"a.ass": []byte(script), "b.srt": []byte(plain), "tail.ass": []byte(scriptWithTail),
			"font-a": fontA, "font-b": fontB, "cover": cover, "unsure": unsure,
			"two.sup": pgs(t, 1920, 1080, twoCaptions), "late.sup": pgs(t, 720, 576, lateCaptions),
		} {
			if err := os.WriteFile(filepath.Join(dir, file), content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	in := func(file string) string { return filepath.Join(filmDir, file) }
	picture := func(seconds string) []string {
		return []string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=" + seconds}
	}
	sound := func(seconds string) []string {
		return []string{"-f", "lavfi", "-i", "sine=frequency=440:duration=" + seconds}
	}
	both := func(seconds string) []string { return append(picture(seconds), sound(seconds)...) }
	h264 := []string{"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p"}
	join := func(parts ...[]string) (all []string) {
		for _, p := range parts {
			all = append(all, p...)
		}
		return all
	}
	// The picture tracks are read with "-copyts", so that their captions
	// keep the times they were made with; and a track that is turned into
	// another sort of picture is read with "-fix_sub_duration", because a
	// disc's captions do not say how long they stay and the other sorts
	// have to.
	p := in(name + ".mkv")
	var recipe []string
	switch name {
	case "styled":
		recipe = join(both("6"), []string{"-i", in("a.ass"), "-map", "0:v", "-map", "1:a", "-map", "2:s"}, h264,
			[]string{"-c:a", "aac", "-b:a", "64k", "-c:s", "ass",
				"-metadata:s:s:0", "language=eng", "-metadata:s:s:0", "title=" + hostileTitle, "-disposition:s:0", "default",
				"-attach", in("font-a"), "-metadata:s:t:0", "mimetype=font/ttf", "-metadata:s:t:0", "filename=" + hostileName,
				"-attach", in("cover"), "-metadata:s:t:1", "mimetype=image/jpeg", "-metadata:s:t:1", "filename=cover.jpg",
				"-attach", in("font-b"), "-metadata:s:t:2", "mimetype=application/octet-stream", "-metadata:s:t:2", "filename=second",
				"-attach", in("unsure"), "-metadata:s:t:3", "mimetype=application/octet-stream", "-metadata:s:t:3", "filename=third",
				p})
	case "five":
		recipe = join([]string{"-copyts"}, both("8"),
			[]string{"-i", in("a.ass"), "-i", in("b.srt"), "-i", in("two.sup"), "-fix_sub_duration", "-i", in("two.sup"), "-fix_sub_duration", "-i", in("two.sup"),
				"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map", "3:s", "-map", "4:s", "-map", "5:s", "-map", "6:s"}, h264,
			[]string{"-c:a", "aac", "-b:a", "64k", "-c:s:0", "ass", "-c:s:1", "subrip", "-c:s:2", "copy", "-c:s:3", "dvdsub", "-c:s:4", "dvbsub",
				"-metadata:s:s:0", "language=eng", "-metadata:s:s:0", "title=Full", "-disposition:s:0", "default",
				"-metadata:s:s:1", "language=nor", "-disposition:s:1", "forced",
				"-metadata:s:s:2", "language=eng", "-metadata:s:s:3", "language=ger", "-metadata:s:s:4", "language=und", p})
	case "late":
		recipe = join([]string{"-copyts"}, both("64"),
			[]string{"-i", in("late.sup"), "-fix_sub_duration", "-i", in("late.sup"), "-fix_sub_duration", "-i", in("late.sup"),
				"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map", "3:s", "-map", "4:s"}, h264,
			[]string{"-c:a", "ac3", "-b:a", "96k", "-c:s:0", "copy", "-c:s:1", "dvdsub", "-c:s:2", "dvbsub", p})
	case "recording":
		p = in(name + ".ts")
		recipe = join([]string{"-copyts"}, both("64"),
			[]string{"-fix_sub_duration", "-i", in("late.sup"), "-map", "0:v", "-map", "1:a", "-map", "2:s"}, h264,
			[]string{"-c:a", "mp2", "-c:s", "dvbsub", "-output_ts_offset", "36000", "-f", "mpegts", p})
	case "phone":
		p = in(name + ".mp4")
		recipe = join(both("6"), []string{"-i", in("b.srt"), "-map", "0:v", "-map", "1:a", "-map", "2:s"}, h264,
			[]string{"-c:a", "aac", "-b:a", "64k", "-c:s", "mov_text", "-metadata:s:s:0", "language=fra", p})
	case "streamed":
		// An MP4 made to be played while it is still arriving: the list of
		// what it holds is at its beginning, so half of it can be opened.
		p = in(name + ".mp4")
		recipe = join(both("6"), []string{"-i", in("b.srt"), "-map", "0:v", "-map", "1:a", "-map", "2:s"}, h264,
			[]string{"-c:a", "aac", "-b:a", "64k", "-c:s", "mov_text", "-movflags", "+faststart", p})
	case "live":
		// ffmpeg cannot go back in what it hands over piece by piece, so
		// the places where an MKV file says how large and how long it is
		// stay empty.
		recipe = join(both("6"), []string{"-i", in("a.ass"), "-map", "0:v", "-map", "1:a", "-map", "2:s"}, h264,
			[]string{"-c:a", "aac", "-b:a", "64k", "-c:s", "ass", "-f", "matroska", "pipe:1"})
	case "silent":
		recipe = join(picture("6"), []string{"-i", in("b.srt"), "-map", "0:v", "-map", "1:s", "-map", "1:s"}, h264, []string{"-c:s:0", "subrip", "-c:s:1", "webvtt", p})
	case "bare":
		recipe = join(both("6"), h264, []string{"-c:a", "aac", "-b:a", "64k", p})
	case "fonts":
		recipe = join(both("6"), h264, []string{"-c:a", "aac", "-b:a", "64k",
			"-attach", in("font-a"), "-metadata:s:t:0", "mimetype=font/ttf", "-metadata:s:t:0", "filename=Only.ttf", p})
	case "tail":
		recipe = join(both("5"), []string{"-i", in("tail.ass"), "-map", "0:v", "-map", "1:a", "-map", "2:s"}, h264, []string{"-c:a", "aac", "-b:a", "64k", "-c:s", "copy", p})
	case "hollow":
		recipe = []string{"-i", madeFilm(t, tl, "five"), "-map", "0", "-c", "copy", "-t", "0.4", p}
	default:
		t.Fatalf("no recipe for a film called %q", name)
	}
	cmd := exec.Command(tl.FFmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, recipe...)...)
	var said bytes.Buffer
	cmd.Stderr = &said
	if recipe[len(recipe)-1] == "pipe:1" {
		// The film is handed over as it is made, and kept as it arrives.
		file, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		cmd.Stdout = file
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("could not make the test film %q: %v\n%s", name, err, said.String())
	}
	films[name] = p
	return p
}

func pgs(t *testing.T, w, h int, captions []substest.Caption) []byte {
	t.Helper()
	b, err := substest.PGS(w, h, captions)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// asked runs ffprobe and returns the lines of its answer. (For some files
// it ends a line with a comma, for a field it has nothing to put in; that
// comma is dropped.)
func asked(t *testing.T, tl Tools, args ...string) []string {
	t.Helper()
	out, err := exec.Command(tl.FFprobe, append([]string{"-v", "error"}, args...)...).Output()
	if err != nil {
		t.Fatalf("ffprobe %v: %v", args, err)
	}
	lines := strings.Fields(strings.ReplaceAll(string(out), "\r", ""))
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, ",")
	}
	return lines
}

// extracted runs Extract on a film into a fresh folder and returns the
// folder, the result and every percentage that was reported.
func extracted(t *testing.T, tl Tools, path string) (dir string, res Result, percents []int) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "subs")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := Extract(ctx, tl, path, dir, func(p int) { percents = append(percents, p) })
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return dir, res, percents
}

// rising says what is wrong with a row of percentages, or "". They must
// rise, stay within 0 to 100, and say 100 last and only last.
func rising(percents []int) string {
	if len(percents) == 0 || percents[len(percents)-1] != 100 {
		return fmt.Sprintf("the last report should be 100: %v", percents)
	}
	for i, p := range percents {
		if p < 0 || p > 100 || i > 0 && p <= percents[i-1] {
			return fmt.Sprintf("report %d is %d after %v", i, p, percents[:i])
		}
	}
	return ""
}

// supPieces takes a ".sup" file apart into its pieces, leaving out the
// field that says when each is to be decoded: ffmpeg fills that in when it
// writes such a file, where the generator leaves it empty.
func supPieces(t *testing.T, b []byte) (pieces [][]byte) {
	t.Helper()
	for len(b) > 0 {
		if len(b) < 13 || b[0] != 'P' || b[1] != 'G' {
			t.Fatalf("not a .sup file: %d bytes left that are not a piece", len(b))
		}
		n := 13 + int(binary.BigEndian.Uint16(b[11:13]))
		if len(b) < n {
			t.Fatalf("a piece of %d bytes in the %d that are left", n, len(b))
		}
		pieces = append(pieces, append(append([]byte{}, b[:6]...), b[10:n]...))
		b = b[n:]
	}
	return pieces
}

// The stand-in. A test that wants one sets standInVar to how it should
// behave and gives Extract this program as both tools.

const standInVar = "LM_SUBS_STANDIN"

func standInTools(t *testing.T, how string) Tools {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(standInVar, how)
	return Tools{FFmpeg: self, FFprobe: self}
}

// standInFilm is what the stand-in for ffprobe says every file holds: 100
// seconds of sound, a styled track, a text track, a disc's picture track
// and a font.
const standInFilm = `{"format": {"start_time": "0.000000", "duration": "100.000000"}, "streams": [
 {"codec_type": "audio", "codec_name": "ac3"},
 {"codec_type": "subtitle", "codec_name": "ass", "tags": {"language": "eng"}},
 {"codec_type": "subtitle", "codec_name": "subrip"},
 {"codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle"},
 {"codec_type": "attachment", "tags": {"filename": "Some.ttf", "mimetype": "font/ttf"}}]}`

// standIn behaves as ffprobe when it is asked to describe a file, and
// otherwise as ffmpeg: it writes the four files that go with standInFilm
// into the folder it was started in, says on its clock that it has read 0,
// 10, 50, 99.9 and 100.5 seconds, and ends. how changes one thing about
// that:
//
//	ok           nothing
//	hang         after the first two times on the clock it never ends
//	fail         it complains about the film by its full path and ends badly
//	cut-sup, cut-ass, cut-vtt, cut-font
//	             that file is cut short (the font: is empty), and it ends well
//	no-sup       that file is not written, and it ends well
//	short:WORDS  it says WORDS as ffmpeg says what troubles it, stops
//	             reading at 50 seconds, and ends well
//	whole:WORDS  it says WORDS, reads to the end, and ends well
func standIn(how string) {
	how, words, _ := strings.Cut(how, ":")
	args := os.Args[1:]
	for _, a := range args {
		if a == "-print_format" {
			fmt.Println(standInFilm)
			os.Exit(0)
		}
	}
	var filmPath string
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			filmPath = args[i+1]
		}
	}
	sup, err := substest.PGS(1920, 1080, twoCaptions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	files := map[string][]byte{
		"s0.ass":   []byte(strings.ReplaceAll(script, "\n", "\r\n")),
		"t1.vtt":   []byte("WEBVTT\n\n00:00.500 --> 00:01.500\nFirst plain line\n\n"),
		"p2.sup":   sup,
		"fonts/f0": fontA,
	}
	switch how {
	case "cut-sup":
		files["p2.sup"] = sup[:len(sup)-7]
	case "cut-ass":
		files["s0.ass"] = files["s0.ass"][:len(files["s0.ass"])-9]
	case "cut-vtt":
		files["t1.vtt"] = []byte("WEB")
	case "cut-font":
		files["fonts/f0"] = nil
	case "no-sup":
		delete(files, "p2.sup")
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.FromSlash(name), content, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	fmt.Println("# timecode format v2")
	fmt.Println(0)
	fmt.Println(10000)
	switch how {
	case "hang":
		for {
			time.Sleep(time.Hour)
		}
	case "fail":
		fmt.Fprintf(os.Stderr, "Error opening input file %s.\nError opening input files: Invalid data found when processing input\n", filmPath)
		os.Exit(3)
	}
	fmt.Println(50000)
	if words != "" {
		fmt.Fprintln(os.Stderr, words)
	}
	if how == "short" {
		os.Exit(0)
	}
	fmt.Println(99900)
	// The last piece of sound often ends a little after the film is said
	// to: that is still not "finished".
	fmt.Println(100500)
	os.Exit(0)
}
