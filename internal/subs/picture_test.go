package subs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/subs/substest"
)

func written(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A disc's picture track says in each caption what size of picture it is
// for, and its first piece gives the first caption's time.
func TestReadingADiscTrack(t *testing.T) {
	for _, c := range []struct {
		w, h  int
		first int64
	}{{1920, 1080, 20000}, {720, 576, 0}, {720, 480, 5400123}, {3840, 2160, 1}} {
		caps := []substest.Caption{
			{StartMs: c.first + 5000, EndMs: c.first + 6000, X: 0, Y: 0, W: 20, H: 20, Colour: color.NRGBA{9, 9, 9, 255}},
			{StartMs: c.first, EndMs: c.first + 1000, X: 0, Y: 0, W: 20, H: 20, Colour: color.NRGBA{9, 9, 9, 255}},
		}
		got, err := ReadPicture(written(t, "track.SUP", pgs(t, c.w, c.h, caps)))
		if want := (PictureFacts{Width: c.w, Height: c.h, FirstMs: c.first}); err != nil || got != want {
			t.Errorf("got %+v, %v; want %+v", got, err, want)
		}
	}
	// A track without captions is an empty file, and that is no mistake.
	if got, err := ReadPicture(written(t, "empty.sup", nil)); err != nil || got != (PictureFacts{FirstMs: -1}) {
		t.Errorf("an empty track: %+v, %v", got, err)
	}
	if _, err := ReadPicture(written(t, "other.sup", []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nNot pictures\n"))); err == nil {
		t.Error("a text file was read as a disc's picture track")
	}
	if _, err := ReadPicture(written(t, "film.avi", nil)); err == nil {
		t.Error("a file of some other sort was read")
	}
	if _, err := ReadPicture(filepath.Join(t.TempDir(), "gone.sup")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file that is not there: %v", err)
	}
}

// When a file must be whole, it is whole only if it ends between two
// pieces. Cut anywhere else, it is refused; this is what stands between a
// full disk and a result that looks finished.
func TestADiscTrackCutShortIsNoticed(t *testing.T) {
	whole := pgs(t, 1920, 1080, twoCaptions)
	ends := map[int]bool{0: true}
	for at := 0; at < len(whole); {
		at += 13 + int(binary.BigEndian.Uint16(whole[at+11:at+13]))
		ends[at] = true
	}
	if len(ends) != 17 { // nothing, and after each of two captions' eight pieces
		t.Fatalf("the track has %d places where it could end", len(ends))
	}
	for cut := 0; cut <= len(whole); cut++ {
		facts, err := readSUP(bytes.NewReader(whole[:cut]), true)
		if ends[cut] != (err == nil) {
			t.Fatalf("cut after %d of %d bytes: %v (%+v)", cut, len(whole), err, facts)
		}
		if err != nil && !errors.Is(err, errNotWhole) {
			t.Fatalf("cut after %d bytes: %v", cut, err)
		}
		// Read for its facts alone, nothing makes it stumble.
		readSUP(bytes.NewReader(whole[:cut]), false)
	}
	if facts, err := readSUP(bytes.NewReader(whole), true); err != nil || facts != (PictureFacts{Width: 1920, Height: 1080, FirstMs: 2000}) {
		t.Errorf("the whole track: %+v, %v", facts, err)
	}
}

// A DVD's picture track comes with a text that describes it: beside it as
// an ".idx" file, or inside a Matroska file.
func TestReadingADVDTracksDescription(t *testing.T) {
	idx := "# VobSub index file, v7 (do not modify this line!)\r\n#\r\n# Settings\r\n\r\n# Original frame size\r\nsize: 720x576\r\n\r\n" +
		"org: 0, 0\r\npalette: 000000, 828282, 828282, 828282, 828282, 828282, 828282, ffffff, 828282, bababa, 828282, 828282, 828282, 828282, 828282, 828282\r\n" +
		"langidx: 0\r\n\r\nid: en, index: 0\r\ntimestamp: 00:01:02:345, filepos: 000000000\r\ntimestamp: 00:01:05:000, filepos: 000001000\r\n"
	got, err := ReadPicture(written(t, "film.idx", []byte(idx)))
	if want := (PictureFacts{Width: 720, Height: 576, FirstMs: 62345}); err != nil || got != want {
		t.Errorf("got %+v, %v; want %+v", got, err, want)
	}
	for text, want := range map[string]PictureFacts{
		"size: 1920x1080\n": {Width: 1920, Height: 1080, FirstMs: -1},
		"  Size:  720 x 480\ntimestamp: 01:00:00:000, filepos: 0": {Width: 720, Height: 480, FirstMs: 3600000},
		"# size: 1x1 is only talked about here\n":                 {FirstMs: -1},
		"":                {FirstMs: -1},
		"oversize: 9x9\n": {FirstMs: -1},
	} {
		if got := readIDX([]byte(text)); got != want {
			t.Errorf("%q read as %+v, want %+v", text, got, want)
		}
	}
}

// A broadcast track says its size in a piece of its captions, if at all.
func TestReadingABroadcastCaptionsSize(t *testing.T) {
	piece := func(kind byte, body ...byte) []byte {
		return append([]byte{0x0F, kind, 0x00, 0x01, byte(len(body) >> 8), byte(len(body))}, body...)
	}
	size := piece(0x14, 0x00, 0x07, 0x7F, 0x04, 0x37) // 1920 by 1080, each less one
	page := piece(0x10, 0x05, 0x00)
	end := piece(0x80)
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for name, c := range map[string]struct {
		caption []byte
		w, h    int
		ok      bool
	}{
		"size first":                 {join(size, page, end), 1920, 1080, true},
		"size after other pieces":    {join(page, page, size, end), 1920, 1080, true},
		"two bytes in front":         {join([]byte{0x20, 0x00}, size, page, end), 1920, 1080, true},
		"no size":                    {join(page, end), 0, 0, false},
		"nothing":                    {nil, 0, 0, false},
		"cut inside the size":        {size[:8], 0, 0, false},
		"not a caption":              {[]byte("WEBVTT and so on, long enough to look into"), 0, 0, false},
		"a size too short to be one": {join(piece(0x14, 0x00, 0x07), end), 0, 0, false},
	} {
		if w, h, ok := dvbSize(c.caption); w != c.w || h != c.h || ok != c.ok {
			t.Errorf("%s: %d by %d, %v", name, w, h, ok)
		}
	}
}

// The Matroska files ffmpeg writes for DVD and broadcast tracks, read for
// their facts and checked for being whole. (That the facts are right for
// real files is in the tests of Extract.)
func TestReadingAMatroskaTrack(t *testing.T) {
	tl := tools(t)
	dir, _, _ := extracted(t, tl, madeFilm(t, tl, "five"))
	for file, codec := range map[string]string{"p3.mks": "DVD", "p4.mks": "broadcast"} {
		whole, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		want := PictureFacts{Width: 1920, Height: 1080, FirstMs: 2000}
		for _, must := range []bool{true, false} {
			if got, err := readMKS(bytes.NewReader(whole), int64(len(whole)), must); err != nil || got != want {
				t.Errorf("%s track (must be whole: %v): %+v, %v", codec, must, got, err)
			}
		}
		// Cut anywhere, it is not whole; and read for its facts alone,
		// nothing makes it stumble.
		for cut := 0; cut < len(whole); cut++ {
			if _, err := readMKS(bytes.NewReader(whole[:cut]), int64(cut), true); err == nil {
				t.Fatalf("%s track cut after %d of %d bytes was taken for whole", codec, cut, len(whole))
			}
			readMKS(bytes.NewReader(whole[:cut]), int64(cut), false)
		}
		// With bytes after its end it is not what ffmpeg wrote either.
		if _, err := readMKS(bytes.NewReader(append(append([]byte{}, whole...), 0)), int64(len(whole))+1, true); err == nil {
			t.Errorf("%s track with a byte too many was taken for whole", codec)
		}
		// Damaged anywhere, it is read without stumbling, whatever comes
		// of it.
		for at := 0; at < len(whole); at++ {
			spoiled := append([]byte{}, whole...)
			spoiled[at] ^= 0xFF
			readMKS(bytes.NewReader(spoiled), int64(len(spoiled)), true)
			readMKS(bytes.NewReader(spoiled), int64(len(spoiled)), false)
		}
	}
	// A broadcast track that does not say its size is for a standard TV
	// picture. ffmpeg always writes the size, so it is struck out here:
	// its piece is given a kind that means nothing.
	whole, _ := os.ReadFile(filepath.Join(dir, "p4.mks"))
	silent := bytes.ReplaceAll(whole, []byte{0x0F, 0x14, 0x00, 0x01, 0x00, 0x05}, []byte{0x0F, 0x7F, 0x00, 0x01, 0x00, 0x05})
	if bytes.Equal(silent, whole) {
		t.Fatal("the broadcast track made by ffmpeg has no piece that says its size")
	}
	if got, err := ReadPicture(written(t, "silent.mks", silent)); err != nil || got != (PictureFacts{Width: 720, Height: 576, FirstMs: 2000}) {
		t.Errorf("a broadcast track that does not say its size: %+v, %v", got, err)
	}
	if _, err := ReadPicture(written(t, "text.mks", []byte("not a Matroska file at all, just some words"))); !errors.Is(err, errNotMatroska) {
		t.Errorf("a text file as Matroska: %v", err)
	}
}

// A DVD's picture track kept in the kind of file DVDs themselves use
// (".vob", ".mpg") comes without the text that describes it, and nothing
// in the film says what size of picture it was made for. It is taken out
// like any other, with a size of 0 by 0: whoever lays it over the film
// then goes by the film's own picture.
func TestADVDTrackWithoutItsDescriptionHasNoSize(t *testing.T) {
	tl := tools(t)
	film := filepath.Join(t.TempDir(), "disc.vob")
	made(t, tl, "-copyts", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=6", "-f", "lavfi", "-i", "sine=duration=6",
		"-fix_sub_duration", "-i", written(t, "two.sup", pgs(t, 1920, 1080, twoCaptions)),
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-c:v", "mpeg2video", "-c:a", "mp2", "-c:s", "dvdsub", "-f", "vob", film)
	dir, res, _ := extracted(t, tl, film)
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	tr := res.Tracks[0]
	if tr.Codec != "dvd_subtitle" || tr.File != "p0.mks" || !tr.Done || tr.Width != 0 || tr.Height != 0 || tr.FirstMs < 2000 || tr.FirstMs > 3000 {
		t.Errorf("the track: %+v", tr)
	}
	if facts, err := ReadPicture(filepath.Join(dir, "p0.mks")); err != nil || facts != (PictureFacts{FirstMs: tr.FirstMs}) {
		t.Errorf("ReadPicture: %+v, %v", facts, err)
	}
}

// counted counts how often a file is read from.
type counted struct {
	r     *bytes.Reader
	reads int
}

func (c *counted) ReadAt(p []byte, off int64) (int, error) {
	c.reads++
	return c.r.ReadAt(p, off)
}

// ReadPicture is for picture subtitle files, and looks no further into a
// file than it takes to find a first caption. A film is not such a file;
// and a file with millions of things in it and no caption among them is
// given up on, not gone through to its end.
func TestReadPictureDoesNotReadWhatIsNotATrack(t *testing.T) {
	whole := textMKS("S_VOBSUB", textCaption{2000, 1500, "a picture"})
	if _, err := ReadPicture(written(t, "film.mkv", whole)); err == nil {
		t.Error("a file called .mkv was read as a picture subtitle track")
	}
	if facts, err := ReadPicture(written(t, "track.mks", whole)); err != nil || facts.FirstMs != 2000 {
		t.Errorf("the same as .mks: %+v, %v", facts, err)
	}
	// Two million elements with nothing in them, where the captions would
	// be: by themselves, and inside one group.
	nothing := bytes.Repeat([]byte{0xEC, 0x80}, 2000000)
	about := element(idTracks, element(idTrackEntry, element(idCodecID, []byte("S_VOBSUB"))))
	for name, file := range map[string][]byte{
		"at the top":      append(element(idEBML), element(idSegment, about, nothing)...),
		"inside a group":  append(element(idEBML), element(idSegment, about, element(idCluster, nothing))...),
		"in group on row": append(element(idEBML), element(idSegment, about, bytes.Repeat(element(idCluster, nothing[:20]), 100000))...),
	} {
		c := &counted{r: bytes.NewReader(file)}
		facts, err := readMKS(c, int64(len(file)), false)
		if c.reads > 300000 || err == nil {
			t.Errorf("%s: the file was read from %d times: %+v, %v", name, c.reads, facts, err)
		}
		// When the file must be whole it is gone through, as the files
		// this package has ffmpeg write must be. It is whole.
		if _, err := readMKS(bytes.NewReader(file), int64(len(file)), true); err != nil {
			t.Errorf("%s, as a file that must be whole: %v", name, err)
		}
	}
}

// A file that must be whole is not if a group of captions, or a caption
// in one, says it is longer than what it is in.
func TestAMatroskaTrackWithAnOverlongPieceIsNotWhole(t *testing.T) {
	whole := textMKS("S_VOBSUB", textCaption{2000, 1500, "a picture"}, textCaption{5000, 1500, "another"})
	if facts, err := readMKS(bytes.NewReader(whole), int64(len(whole)), true); err != nil || facts.FirstMs != 2000 {
		t.Fatalf("the whole file: %+v, %v", facts, err)
	}
	// Every element here gives its length in the eight bytes before its
	// content; the last of them is raised, by more than there is room for
	// in what the element is in.
	block := bytes.Index(whole, []byte{0x81, 0, 0, 0, 'a', ' ', 'p'})
	group := block - 9 - 8 // the block's name and length, and the group's length
	for name, at := range map[string]int{"a caption": block - 1, "a group's caption and length together": group + 7} {
		spoiled := append([]byte{}, whole...)
		spoiled[at] += 64
		if _, err := readMKS(bytes.NewReader(spoiled), int64(len(spoiled)), true); !errors.Is(err, errNotWhole) {
			t.Errorf("%s said to be 64 bytes longer than it is: %v", name, err)
		}
		// Read for its facts alone, it does not stumble.
		readMKS(bytes.NewReader(spoiled), int64(len(spoiled)), false)
	}
}
