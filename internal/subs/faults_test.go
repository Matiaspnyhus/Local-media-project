package subs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests are about films that are not in order: cut short, on a drive
// that stops answering, with a track that is damaged, or with text in an
// encoding nobody uses any more. Films like these are made here by hand,
// since ffmpeg will not write one on purpose.

// made has ffmpeg make something, and fails the test if it cannot.
func made(t *testing.T, tl Tools, args ...string) {
	t.Helper()
	out, err := exec.Command(tl.FFmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// srtOf writes captions in the commonest text format, one a second from
// the first second on, each shown for eight tenths of a second.
func srtOf(captions ...string) []byte {
	var b bytes.Buffer
	for i, text := range captions {
		fmt.Fprintf(&b, "%d\n00:%02d:%02d,000 --> 00:%02d:%02d,800\n%s\n\n", i+1, (i+1)/60, (i+1)%60, (i+1)/60, (i+1)%60, text)
	}
	return b.Bytes()
}

// mixedFilm makes a film of 12 seconds with a styled track, a text track
// put in exactly as given (ffmpeg does not look at what encoding it is
// in), and a disc's picture track.
func mixedFilm(t *testing.T, tl Tools, text []byte) string {
	t.Helper()
	dir := t.TempDir()
	film := filepath.Join(dir, "mixed.mkv")
	made(t, tl, "-copyts", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=12", "-f", "lavfi", "-i", "sine=duration=12",
		"-i", written(t, "a.ass", []byte(script)), "-f", "srt", "-i", written(t, "b.srt", text), "-i", written(t, "c.sup", pgs(t, 1920, 1080, twoCaptions)),
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map", "3:s", "-map", "4:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-c:s:0", "ass", "-c:s:1", "copy", "-c:s:2", "copy", film)
	return film
}

// firstPart writes the beginning of a file into a new one: what a copy
// that was broken off leaves behind.
func firstPart(t *testing.T, path string, share float64) string {
	t.Helper()
	b := mustRead(t, path)
	return written(t, "cut"+filepath.Ext(path), b[:int(float64(len(b))*share)])
}

// A film whose file stops half-way. ffmpeg reads what there is and ends as
// if all were well, with every track written in good form as far as it
// got. That is not a result: the second half was never read.
//
// An MKV and an MP4 say at their beginning how large they are, and half of
// one is turned away before it is read. (Of this half MP4, neither
// converter says anything at all.) A recording that does not say its size
// is read, and found out by what ffmpeg says about its end.
func TestAFilmCutShortIsNotAResult(t *testing.T) {
	tl := tools(t)
	for _, name := range []string{"late", "streamed", "live"} {
		t.Run(name, func(t *testing.T) {
			whole := madeFilm(t, tl, name)
			if stopsEarly(whole) {
				t.Fatal("the whole film is taken for one that is cut short")
			}
			half := firstPart(t, whole, 0.5)
			if early := stopsEarly(half); early != (name != "live") {
				t.Errorf("half the film is known to be cut short before it is read: %v", early)
			}
			dir := filepath.Join(t.TempDir(), "subs")
			var percents []int
			res, err := Extract(context.Background(), tl, half, dir, func(p int) { percents = append(percents, p) })
			if !errors.Is(err, ErrNotReadThrough) {
				t.Fatalf("half a film was read (reports: %v): %v, %+v", percents, err, res.Tracks)
			}
			if strings.Contains(err.Error(), filepath.Dir(half)) {
				t.Errorf("the error names the film's folder: %v", err)
			}
			if len(percents) > 0 && percents[len(percents)-1] == 100 {
				t.Errorf("a run that did not finish reported 100: %v", percents)
			}
			nothingKept(t, dir)
		})
	}
}

// The same when the drive a film is on stops answering part-way (a network
// drive that drops out): ffmpeg says that reading failed and ends well.
// That cannot be brought about with the real ffmpeg on every system, so
// the stand-in says what each converter was heard to say when reads of a
// film were made to fail half-way through it.
//
// ffmpeg's words for it may change. So anything it complains of counts
// when the reading also stopped well short of the film's length. A film
// that merely is shorter than it says, with no complaint, is in order: a
// film's length is that of its longest track, and the clock follows one.
func TestAReadingThatFailedPartWayIsNotAResult(t *testing.T) {
	for name, c := range map[string]struct {
		how  string
		kept bool
	}{
		"an MKV on the converter that ships":         {"short:[in#0/matroska,webm @ 0x5652e1412f40] Read error\n[in#0/matroska,webm @ 0x5652e1412c40] Error during demuxing: Input/output error", false},
		"an MKV on the older converter":              {"short:[matroska,webm @ 0x55a5d8339080] Read error\n[in#0/matroska,webm @ 0x55a5d8338f80] Error during demuxing: Input/output error\n[in#0/matroska,webm @ 0x55a5d8338f80] Error retrieving a packet from demuxer: Input/output error", false},
		"an MP4":                                     {"short:[in#0/mov,mp4,m4a,3gp,3g2,mj2 @ 0x55e4989e7f40] stream 1, offset 0xc8662: partial file\n[in#0/mov,mp4,m4a,3gp,3g2,mj2 @ 0x55e4989e7c40] Error during demuxing: Input/output error", false},
		"a TV recording":                             {"short:[in#0/mpegts @ 0x559d63182b40] Error during demuxing: Input/output error", false},
		"a drive that drops out in the last seconds": {"whole:[in#0/matroska,webm @ 0x5652e1412c40] Error during demuxing: Connection timed out", false},
		"words never heard before, stopped early":    {"short:[in#0 @ 0x1] Something it has never said before", false},
		"words never heard before, read to the end":  {"whole:[in#0 @ 0x1] Something it has never said before", true},
		"no complaint, shorter than it says":         {"short:", true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "subs")
			var percents []int
			res, err := Extract(context.Background(), standInTools(t, c.how), filepath.Join(t.TempDir(), "Household Videos", "any.mkv"), dir, func(p int) { percents = append(percents, p) })
			if c.kept {
				if err != nil || len(res.Tracks) != 3 || !res.Tracks[0].Done || percents[len(percents)-1] != 100 {
					t.Errorf("a film that is in order: %v, %+v, reports %v", err, res, percents)
				}
				if _, err := Load(dir); err != nil {
					t.Errorf("Load: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrNotReadThrough) || strings.Contains(err.Error(), "Household") {
				t.Errorf("error: %v", err)
			}
			if len(percents) == 0 || percents[len(percents)-1] == 100 {
				t.Errorf("reports: %v", percents)
			}
			nothingKept(t, dir)
		})
	}
}

// How a file is known to be cut short without reading it through: the two
// kinds of file most films are kept in say how much they hold.
func TestAFileThatSaysItHoldsMore(t *testing.T) {
	box := func(name string, size int) []byte {
		b := make([]byte, size)
		b[0], b[1], b[2], b[3] = byte(size>>24), byte(size>>16), byte(size>>8), byte(size)
		copy(b[4:], name)
		return b
	}
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	// A box of 40 bytes that gives its size the long way, and one that
	// runs to wherever the file ends.
	long := append([]byte{0, 0, 0, 1, 'm', 'd', 'a', 't', 0, 0, 0, 0, 0, 0, 0, 40}, make([]byte, 24)...)
	toEnd := append([]byte{0, 0, 0, 0, 'm', 'd', 'a', 't'}, make([]byte, 50)...)
	mp4 := join(box("ftyp", 24), box("moov", 100), box("mdat", 300))
	// An MKV: a heading of 4 bytes, then the element that holds the film,
	// here said to be 100 bytes, and after it the same without a size.
	mkv := append([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x84, 'x', 'x', 'x', 'x', 0x18, 0x53, 0x80, 0x67, 0x40, 100}, make([]byte, 100)...)
	unsized := append([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x84, 'x', 'x', 'x', 'x', 0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, make([]byte, 100)...)
	for name, c := range map[string]struct {
		file []byte
		cut  bool
	}{
		"an MP4":                                {mp4, false},
		"an MP4 without its last byte":          {mp4[:len(mp4)-1], true},
		"an MP4 cut inside its second box":      {mp4[:60], true},
		"an MP4 cut between two boxes":          {mp4[:124], false}, // nothing in what is left says more was to come
		"an MP4 cut inside a box's first 8":     {mp4[:128], false},
		"an MP4 with stray bytes after it":      {join(mp4, []byte{0, 0, 0}), false},
		"a long box":                            {join(box("ftyp", 24), long), false},
		"a long box cut":                        {join(box("ftyp", 24), long[:30]), true},
		"a long box cut inside its size":        {join(box("ftyp", 24), long[:12]), false},
		"a long box with a size of nothing":     {join(box("ftyp", 24), []byte{0, 0, 0, 1, 'm', 'd', 'a', 't', 0xFF, 0, 0, 0, 0, 0, 0, 40}), false},
		"a last box that runs to the end":       {join(box("ftyp", 24), toEnd), false},
		"the same, cut":                         {join(box("ftyp", 24), toEnd[:20]), false},
		"a box too small to be one":             {join(box("ftyp", 24), []byte{0, 0, 0, 4, 'm', 'd', 'a', 't'}), false},
		"something else after the first box":    {join(box("ftyp", 24), []byte{0, 9, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8}), false},
		"an MKV":                                {mkv, false},
		"an MKV without its last byte":          {mkv[:len(mkv)-1], true},
		"an MKV cut in half":                    {mkv[:60], true},
		"an MKV with more after it":             {join(mkv, []byte("more")), false},
		"an MKV that does not say its size":     {unsized[:60], false},
		"an MKV cut inside its heading":         {mkv[:11], false},
		"a TV recording":                        {append([]byte{0x47, 0x40, 0x00, 0x10}, make([]byte, 184)...), false},
		"words":                                 {[]byte("WEBVTT\n\n00:01.000 --> 00:02.000\nNot a film\n"), false},
		"four bytes":                            {[]byte("ftyp"), false},
		"nothing":                               {nil, false},
		"a name that begins like a box's":       {[]byte("\x7F\xFF\xFF\xFFmoov and so on"), true},
		"a first box of a kind not known":       {box("abcd", 2000)[:100], false},
		"five thousand boxes of nothing at all": {bytes.Repeat(box("free", 8), 5000), false},
	} {
		if got := saysMoreThan(bytes.NewReader(c.file), int64(len(c.file))); got != c.cut {
			t.Errorf("%s: taken for cut short: %v, want %v", name, got, c.cut)
		}
	}
	if stopsEarly(filepath.Join(t.TempDir(), "not there.mkv")) {
		t.Error("a file that is not there is taken for one that is cut short")
	}
}
