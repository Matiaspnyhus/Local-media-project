// Package substest makes picture subtitles for tests. Films on discs carry
// their subtitles as pictures, and there is no program on a test machine
// that draws such a track: ffmpeg can read one and copy one, but not make
// one. So this package writes one itself, byte by byte, in the form Blu-ray
// discs use (PGS, kept in a file ending in ".sup").
//
// Each caption is a rectangle of one colour, put at a known place for a
// known time. A test that burns the track into a picture can then look for
// that colour in those pixels, and for its absence before and after.
//
// Nothing here is used by the program itself.
package substest

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"math"
	"sort"
)

// Caption is one picture subtitle: a filled rectangle.
type Caption struct {
	// StartMs and EndMs are when it appears and when it goes, in
	// thousandths of a second.
	StartMs, EndMs int64
	// X and Y are its top left corner and W and H its size, in pixels of
	// the size the track is made for.
	X, Y, W, H int
	// Colour is what it is filled with. A is how solid it is: 255 hides
	// the film behind it, 128 lets half of it through.
	Colour color.NRGBA
	// Forced marks a caption as one to show even when subtitles are off
	// (a sign, or a line in a foreign language).
	Forced bool
}

// longestMs is the last moment a ".sup" file can name: its clock is a
// 32-bit count of 90,000ths of a second.
const longestMs = math.MaxUint32 / 90

// The kinds of piece a track is made of. Showing a caption takes one of
// each of the first four and then an end mark; taking it away takes a
// composition with nothing in it.
const (
	segPalette     = 0x14 // which colours there are
	segObject      = 0x15 // a picture, as runs of one colour
	segComposition = 0x16 // what is on the screen from now on, and where
	segWindow      = 0x17 // the part of the screen that is drawn in
	segEnd         = 0x80
)

// PGS returns a picture subtitle track made for a picture of the given
// size, as the bytes of a ".sup" file. The captions may be given in any
// order but may not overlap in time: a track like this shows one
// composition at a time, and one caption is one composition here.
func PGS(width, height int, captions []Caption) ([]byte, error) {
	if width < 16 || height < 16 || width > 4096 || height > 4096 {
		return nil, fmt.Errorf("a picture of %d by %d is not one a disc could hold", width, height)
	}
	list := append([]Caption(nil), captions...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].StartMs < list[j].StartMs })
	var out bytes.Buffer
	number := 0 // compositions are numbered, each one higher than the last
	for i, c := range list {
		switch {
		case c.StartMs < 0 || c.EndMs <= c.StartMs || c.EndMs > longestMs:
			return nil, fmt.Errorf("caption %d is shown from %d to %d ms", i, c.StartMs, c.EndMs)
		case c.W < 1 || c.H < 1 || c.X < 0 || c.Y < 0 || c.X+c.W > width || c.Y+c.H > height:
			return nil, fmt.Errorf("caption %d (%d by %d at %d,%d) does not fit a picture of %d by %d", i, c.W, c.H, c.X, c.Y, width, height)
		case i > 0 && c.StartMs < list[i-1].EndMs:
			return nil, fmt.Errorf("caption %d begins at %d ms, before the one in front of it ends", i, c.StartMs)
		}
		picture, err := runs(c.W, c.H)
		if err != nil {
			return nil, err
		}
		at := uint32(c.StartMs * 90)
		// "From now on, object 0 is shown in window 0 at this place": a
		// fresh start, which needs nothing that came before it. That is
		// what lets a player begin reading anywhere in the track.
		flags := byte(0)
		if c.Forced {
			flags = 0x40
		}
		segment(&out, at, segComposition, join(
			be16(width), be16(height), []byte{0x10}, // 0x10: the frame rate field, which nothing reads
			be16(number), []byte{0x80, 0x00, 0x00, 1}, // a fresh start; the palette is not being changed alone; palette 0; one object
			be16(0), []byte{0, flags}, be16(c.X), be16(c.Y)))
		number++
		window := join([]byte{1, 0}, be16(c.X), be16(c.Y), be16(c.W), be16(c.H))
		segment(&out, at, segWindow, window)
		// Colour 1 is the rectangle's. Colours are stored the way video
		// is: brightness and two colour differences, by the rule for
		// pictures of this size.
		y, cb, cr := videoColour(c.Colour, height)
		segment(&out, at, segPalette, []byte{0, 0, 1, y, cr, cb, c.Colour.A})
		// The object: its number, version 0, "this piece is both the
		// first and the last", how many bytes follow (the size and the
		// runs), and its size.
		segment(&out, at, segObject, join(be16(0), []byte{0, 0xC0}, be24(len(picture)+4), be16(c.W), be16(c.H), picture))
		segment(&out, at, segEnd, nil)

		// Taken away again by a composition with no object in it, unless
		// the next caption begins at that very moment and replaces it.
		if i+1 < len(list) && list[i+1].StartMs == c.EndMs {
			continue
		}
		gone := uint32(c.EndMs * 90)
		segment(&out, gone, segComposition, join(be16(width), be16(height), []byte{0x10}, be16(number), []byte{0x00, 0x00, 0x00, 0}))
		number++
		segment(&out, gone, segWindow, window)
		segment(&out, gone, segEnd, nil)
	}
	return out.Bytes(), nil
}

// segment writes one piece: the letters "PG", when it is to be shown, when
// it is to be decoded (0, as most programs that write these files leave
// it), its kind, its length and its body.
func segment(out *bytes.Buffer, at uint32, kind byte, body []byte) {
	out.WriteString("PG")
	out.Write([]byte{byte(at >> 24), byte(at >> 16), byte(at >> 8), byte(at), 0, 0, 0, 0, kind})
	out.Write(be16(len(body)))
	out.Write(body)
}

// runs draws a w by h rectangle of colour 1 in the track's way of writing
// pictures: line by line, each line as runs of one colour.
func runs(w, h int) ([]byte, error) {
	var line []byte
	for left := w; left > 0; {
		n := min(left, 0x3FFF)
		switch {
		case n == 1:
			line = append(line, 1) // a single pixel is just its colour
		case n == 2:
			line = append(line, 1, 1) // and so are two: a run is three or more
		case n < 64:
			line = append(line, 0, 0x80|byte(n), 1)
		default:
			line = append(line, 0, 0xC0|byte(n>>8), byte(n), 1)
		}
		left -= n
	}
	line = append(line, 0, 0) // end of line
	if len(line)*h > 65000 {
		// One piece holds at most 65,535 bytes. A larger picture is cut
		// into several pieces, which no test has needed yet.
		return nil, errors.New("the rectangle is too large to write in one piece")
	}
	return bytes.Repeat(line, h), nil
}

// videoColour turns red, green and blue into brightness and two colour
// differences, in the narrow range video uses (16 to 235 for brightness).
// Large pictures use one rule for it (BT.709) and small ones another
// (BT.601); players pick by the picture's height, so this does too.
func videoColour(c color.NRGBA, height int) (y, cb, cr byte) {
	kr, kb := 0.2126, 0.0722
	if height <= 576 {
		kr, kb = 0.299, 0.114
	}
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	luma := kr*r + (1-kr-kb)*g + kb*b
	round := func(v float64) byte { return byte(math.Round(min(max(v, 0), 255))) }
	return round(16 + 219*luma), round(128 + 224*(b-luma)/(2*(1-kb))), round(128 + 224*(r-luma)/(2*(1-kr)))
}

func be16(n int) []byte { return []byte{byte(n >> 8), byte(n)} }
func be24(n int) []byte { return []byte{byte(n >> 16), byte(n >> 8), byte(n)} }

func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
