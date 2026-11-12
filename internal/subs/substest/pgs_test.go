package substest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

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

// Three captions on a picture of 640 by 360, and the same at twice the
// size. The first two follow each other without a gap; the second lets
// half of the film through; the third is marked as one to show even with
// subtitles off.
func three(scale int) []Caption {
	return []Caption{
		{StartMs: 2000, EndMs: 3500, X: 50 * scale, Y: 250 * scale, W: 200 * scale, H: 40 * scale, Colour: color.NRGBA{230, 30, 30, 255}},
		{StartMs: 3500, EndMs: 5000, X: 350 * scale, Y: 50 * scale, W: 150 * scale, H: 76 * scale, Colour: color.NRGBA{0, 200, 50, 128}},
		{StartMs: 5500, EndMs: 6500, X: 20 * scale, Y: 20 * scale, W: 100 * scale, H: 30 * scale, Colour: color.NRGBA{20, 40, 240, 255}, Forced: true},
	}
}

// grey is the picture the captions are laid over.
var grey = color.NRGBA{128, 128, 128, 255}

// shown is the colour a caption gives the picture where it lies: its own,
// or part-way from the picture's to its own if it lets some through.
func shown(c Caption) color.NRGBA {
	mix := func(over, under uint8) uint8 {
		return uint8((int(over)*int(c.Colour.A) + int(under)*(255-int(c.Colour.A)) + 127) / 255)
	}
	return color.NRGBA{mix(c.Colour.R, grey.R), mix(c.Colour.G, grey.G), mix(c.Colour.B, grey.B), 255}
}

// frames lays a ".sup" file over a grey picture of w by h with ffmpeg and
// returns the pictures at the given moments (in 25ths of a second), each
// as w*h*3 bytes of red, green and blue.
func frames(t *testing.T, ffmpeg, sup string, w, h int, forcedOnly bool, at []int) [][]byte {
	t.Helper()
	var picked []string
	for _, n := range at {
		picked = append(picked, fmt.Sprintf(`eq(n\,%d)`, n))
	}
	// "-copyts": without it ffmpeg counts a subtitle file's times from its
	// first caption, and everything would be shown two seconds early.
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-copyts",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=gray:size=%dx%d:rate=25:duration=7", w, h)}
	if forcedOnly {
		args = append(args, "-forced_subs_only", "1")
	}
	args = append(args, "-i", sup,
		"-filter_complex", "[0:v][1:s:0]overlay=eof_action=pass,select='"+strings.Join(picked, "+")+"',format=rgb24[o]",
		"-map", "[o]", "-fps_mode", "passthrough", "-f", "rawvideo", "pipe:1")
	cmd := exec.Command(ffmpeg, args...)
	var said bytes.Buffer
	cmd.Stderr = &said
	out, err := cmd.Output()
	if err != nil || len(out) != len(at)*w*h*3 {
		t.Fatalf("laying the captions over a picture: %v, %d bytes for %d pictures\n%s", err, len(out), len(at), said.String())
	}
	var list [][]byte
	for i := range at {
		list = append(list, out[i*w*h*3:(i+1)*w*h*3])
	}
	return list
}

// near says whether the pixel at x, y of a picture is the wanted colour,
// give or take what converting between the two ways of writing colours
// costs.
func near(frame []byte, w, x, y int, want color.NRGBA, slack int) (got color.NRGBA, ok bool) {
	o := (y*w + x) * 3
	got = color.NRGBA{frame[o], frame[o+1], frame[o+2], 255}
	off := func(a, b uint8) bool { return int(a)-int(b) > slack || int(b)-int(a) > slack }
	return got, !off(got.R, want.R) && !off(got.G, want.G) && !off(got.B, want.B)
}

// lies fails the test unless caption c is in the picture (or, with there
// false, is not): its colour just inside each corner and in the middle,
// and the picture's own grey just outside each side.
func lies(t *testing.T, what string, frame []byte, w int, c Caption, there bool) {
	t.Helper()
	inside := [][2]int{{c.X + c.W/2, c.Y + c.H/2}, {c.X + 3, c.Y + 3}, {c.X + c.W - 4, c.Y + 3}, {c.X + 3, c.Y + c.H - 4}, {c.X + c.W - 4, c.Y + c.H - 4}}
	outside := [][2]int{{c.X - 4, c.Y + c.H/2}, {c.X + c.W + 3, c.Y + c.H/2}, {c.X + c.W/2, c.Y - 4}, {c.X + c.W/2, c.Y + c.H + 3}}
	want := grey
	if there {
		want = shown(c)
	}
	for _, p := range inside {
		if got, ok := near(frame, w, p[0], p[1], want, 10); !ok {
			t.Errorf("%s: the pixel at %d,%d, inside the caption's place, is %v, want about %v", what, p[0], p[1], got, want)
			return
		}
	}
	for _, p := range outside {
		if got, ok := near(frame, w, p[0], p[1], grey, 4); !ok {
			t.Errorf("%s: the pixel at %d,%d, just outside the caption's place, is %v, want the picture's grey", what, p[0], p[1], got)
			return
		}
	}
}

// ffmpeg reads what PGS writes: ffprobe finds a disc's picture track of
// the right size with the captions' times, and laid over a picture, each
// rectangle is where and when it should be, in its colour. Once on a small
// picture and once on a large one, because the two write colours by
// different rules.
func TestFFmpegShowsWhatWasWritten(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	for _, scale := range []int{1, 2} {
		w, h := 640*scale, 360*scale
		captions := three(scale)
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			b, err := PGS(w, h, captions)
			if err != nil {
				t.Fatal(err)
			}
			sup := filepath.Join(t.TempDir(), "three.sup")
			if err := os.WriteFile(sup, b, 0o600); err != nil {
				t.Fatal(err)
			}

			out, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "packet=pts_time:stream=codec_name,width,height", "-of", "csv=p=0", sup).Output()
			if err != nil {
				t.Fatalf("ffprobe: %v", err)
			}
			lines := strings.Fields(strings.ReplaceAll(string(out), "\r", ""))
			// Every piece of a caption carries its time: five pieces to
			// show one, three to take it away, and nothing between two
			// that follow each other directly.
			var want []string
			for _, group := range []struct {
				at     string
				pieces int
			}{{"2.000000", 5}, {"3.500000", 5}, {"5.000000", 3}, {"5.500000", 5}, {"6.500000", 3}} {
				for i := 0; i < group.pieces; i++ {
					want = append(want, group.at)
				}
			}
			want = append(want, fmt.Sprintf("hdmv_pgs_subtitle,%d,%d", w, h))
			if !reflect.DeepEqual(lines, want) {
				t.Errorf("ffprobe reads\n%v\nwant\n%v", lines, want)
			}

			// Pictures at 1, 3, 4.4, 5.2, 6 and 6.8 seconds.
			got := frames(t, ffmpeg, sup, w, h, false, []int{25, 75, 110, 130, 150, 170})
			for i, which := range [][]bool{{false, false, false}, {true, false, false}, {false, true, false}, {false, false, false}, {false, false, true}, {false, false, false}} {
				for c, there := range which {
					lies(t, fmt.Sprintf("picture %d, caption %d", i, c), got[i], w, captions[c], there)
				}
			}
			// Asked for forced captions only, ffmpeg shows the third and
			// not the first.
			got = frames(t, ffmpeg, sup, w, h, true, []int{75, 150})
			lies(t, "forced only, at 3 s", got[0], w, captions[0], false)
			lies(t, "forced only, at 6 s", got[1], w, captions[2], true)
		})
	}
}

// pieces takes a track apart: for each piece its kind and its time in
// thousandths of a second.
func pieces(t *testing.T, b []byte) (kinds []byte, times []int64) {
	t.Helper()
	for len(b) > 0 {
		if len(b) < 13 || b[0] != 'P' || b[1] != 'G' {
			t.Fatalf("%d bytes left that are not a piece", len(b))
		}
		n := 13 + int(binary.BigEndian.Uint16(b[11:13]))
		if len(b) < n {
			t.Fatalf("a piece of %d bytes in the %d that are left", n, len(b))
		}
		kinds = append(kinds, b[10])
		times = append(times, int64(binary.BigEndian.Uint32(b[2:6]))/90)
		b = b[n:]
	}
	return
}

func TestWhatATrackIsMadeOf(t *testing.T) {
	// Given out of order, written in order.
	b, err := PGS(1920, 1080, []Caption{
		{StartMs: 9000, EndMs: 9500, X: 0, Y: 0, W: 1920, H: 1080, Colour: color.NRGBA{1, 2, 3, 4}},
		{StartMs: 1000, EndMs: 2000, X: 10, Y: 20, W: 1, H: 1, Colour: color.NRGBA{255, 255, 255, 255}},
		{StartMs: 2000, EndMs: 3000, X: 10, Y: 20, W: 70, H: 2, Colour: color.NRGBA{255, 255, 255, 255}},
	})
	if err != nil {
		t.Fatal(err)
	}
	kinds, times := pieces(t, b)
	show, hide := []byte{segComposition, segWindow, segPalette, segObject, segEnd}, []byte{segComposition, segWindow, segEnd}
	var wantKinds []byte
	var wantTimes []int64
	for _, part := range []struct {
		kinds []byte
		at    int64
	}{{show, 1000}, {show, 2000}, {hide, 3000}, {show, 9000}, {hide, 9500}} {
		for range part.kinds {
			wantTimes = append(wantTimes, part.at)
		}
		wantKinds = append(wantKinds, part.kinds...)
	}
	if !bytes.Equal(kinds, wantKinds) || !reflect.DeepEqual(times, wantTimes) {
		t.Errorf("pieces % x at %v\n    want % x at %v", kinds, times, wantKinds, wantTimes)
	}
	// The same captions give the same bytes, every time.
	if again, _ := PGS(1920, 1080, []Caption{
		{StartMs: 1000, EndMs: 2000, X: 10, Y: 20, W: 1, H: 1, Colour: color.NRGBA{255, 255, 255, 255}},
		{StartMs: 2000, EndMs: 3000, X: 10, Y: 20, W: 70, H: 2, Colour: color.NRGBA{255, 255, 255, 255}},
		{StartMs: 9000, EndMs: 9500, X: 0, Y: 0, W: 1920, H: 1080, Colour: color.NRGBA{1, 2, 3, 4}},
	}); !bytes.Equal(again, b) {
		t.Error("the same captions in another order gave other bytes")
	}
	// No captions: an empty track.
	if b, err := PGS(1920, 1080, nil); err != nil || len(b) != 0 {
		t.Errorf("no captions: %d bytes, %v", len(b), err)
	}
	// A long film's worth is no trouble.
	var many []Caption
	for i := int64(0); i < 3000; i++ {
		many = append(many, Caption{StartMs: 20000 + i*3000, EndMs: 22000 + i*3000, X: 600, Y: 900, W: 700, H: 90, Colour: color.NRGBA{255, 255, 255, 255}})
	}
	if b, err := PGS(1920, 1080, many); err != nil || len(b) < 3000*100 {
		t.Errorf("3000 captions: %d bytes, %v", len(b), err)
	} else if kinds, _ := pieces(t, b); len(kinds) != 3000*8 {
		t.Errorf("3000 captions: %d pieces", len(kinds))
	}
}

func TestWhatCannotBeWrittenIsRefused(t *testing.T) {
	ok := Caption{StartMs: 1000, EndMs: 2000, X: 10, Y: 10, W: 100, H: 50, Colour: color.NRGBA{255, 0, 0, 255}}
	with := func(change func(c *Caption)) []Caption {
		c := ok
		change(&c)
		return []Caption{c}
	}
	for name, captions := range map[string][]Caption{
		"ends before it begins":     with(func(c *Caption) { c.EndMs = 500 }),
		"no time at all":            with(func(c *Caption) { c.EndMs = c.StartMs }),
		"begins before zero":        with(func(c *Caption) { c.StartMs = -1 }),
		"later than the clock goes": with(func(c *Caption) { c.StartMs, c.EndMs = 47721000, 47722000 }),
		"off the right edge":        with(func(c *Caption) { c.X = 600 }),
		"off the bottom":            with(func(c *Caption) { c.Y = 340 }),
		"left of the picture":       with(func(c *Caption) { c.X = -1 }),
		"no width":                  with(func(c *Caption) { c.W = 0 }),
		"no height":                 with(func(c *Caption) { c.H = 0 }),
		"two at once":               {ok, {StartMs: 1500, EndMs: 2500, X: 200, Y: 200, W: 10, H: 10}},
	} {
		if b, err := PGS(640, 360, captions); err == nil {
			t.Errorf("%s: written as %d bytes", name, len(b))
		}
	}
	for _, size := range [][2]int{{0, 0}, {8, 8}, {5000, 1080}, {1920, -1}} {
		if _, err := PGS(size[0], size[1], []Caption{ok}); err == nil {
			t.Errorf("a picture of %d by %d was accepted", size[0], size[1])
		}
	}
	if _, err := PGS(640, 360, []Caption{ok}); err != nil {
		t.Errorf("a caption that is in order: %v", err)
	}
}
