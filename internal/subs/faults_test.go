package subs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
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

// Text tracks are not always in today's encoding: a film put together from
// an old subtitle file carries its text as that file had it. ffmpeg, asked
// to convert such a track, drops every caption it cannot read as today's
// encoding and says the track is done; and when it can read too few of
// them it gives up on the whole film, the other tracks with it. So text
// tracks are not converted by ffmpeg: they are copied out as they are and
// turned into WebVTT here, where a line in the old Western European
// encoding is read as that.
func TestATextTrackInAnOldEncodingKeepsItsLines(t *testing.T) {
	tl := tools(t)
	// Two of five captions are in the old encoding; one of them has two
	// lines. The last is in today's, with letters the old one cannot hold.
	film := mixedFilm(t, tl, srtOf("Bl\xe5b\xe6rsyltet\xf8y er godt", "Plain line", "Enda en bl\xe5 linje\nover to linjer", "Plain line", "Blåbær og ♪ i dag"))
	dir, res, _ := extracted(t, tl, film)
	if len(res.Tracks) != 3 || !res.Tracks[0].Done || !res.Tracks[1].Done || !res.Tracks[2].Done || res.Tracks[1].File != "t1.vtt" || res.Tracks[1].FirstMs != 1000 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	want := "WEBVTT\n" +
		"\n00:00:01.000 --> 00:00:01.800\nBlåbærsyltetøy er godt\n" +
		"\n00:00:02.000 --> 00:00:02.800\nPlain line\n" +
		"\n00:00:03.000 --> 00:00:03.800\nEnda en blå linje\nover to linjer\n" +
		"\n00:00:04.000 --> 00:00:04.800\nPlain line\n" +
		"\n00:00:05.000 --> 00:00:05.800\nBlåbær og ♪ i dag\n"
	if got := string(mustRead(t, filepath.Join(dir, "t1.vtt"))); got != want {
		t.Errorf("the text track:\n%q\nwant\n%q", got, want)
	}
}

// A text track of which not one caption is in today's encoding (Greek, in
// the encoding Greek PCs used) made ffmpeg give up on the whole film. Now
// the film's other tracks come out, and so does this one: its ten captions
// are there, at their times. (Their letters are wrong: only the Western
// European encoding is known here. That is what the program does with a
// subtitle file in such an encoding too.)
func TestATextTrackNobodyCanReadDoesNotLoseTheFilm(t *testing.T) {
	tl := tools(t)
	var greek []string
	for i := 0; i < 10; i++ {
		greek = append(greek, "\xca\xe1\xeb\xe7\xec\xdd\xf1\xe1 \xea\xfc\xf3\xec\xe5")
	}
	dir, res, _ := extracted(t, tl, mixedFilm(t, tl, srtOf(greek...)))
	if len(res.Tracks) != 3 || !res.Tracks[0].Done || !res.Tracks[1].Done || !res.Tracks[2].Done {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	vtt := string(mustRead(t, filepath.Join(dir, "t1.vtt")))
	if got := strings.Count(vtt, "-->"); got != 10 || !strings.Contains(vtt, "\n00:00:10.000 --> 00:00:10.800\n") {
		t.Errorf("%d of 10 captions came out:\n%s", got, vtt)
	}
	// The other two are what they would be in any film.
	if got := Flatten(mustRead(t, filepath.Join(dir, "s0.ass"))); len(got) != 2 {
		t.Errorf("the styled track: %+v", got)
	}
	if facts, err := ReadPicture(filepath.Join(dir, "p2.sup")); err != nil || facts != (PictureFacts{Width: 1920, Height: 1080, FirstMs: 2000}) {
		t.Errorf("the disc's track: %+v, %v", facts, err)
	}
}

// onlyTheDamagedTrackIsLost fails the test unless a run gave every track
// but the one numbered bad, said of that one that it is damaged, and left
// a folder that Load takes.
func onlyTheDamagedTrackIsLost(t *testing.T, dir string, res Result, bad int) {
	t.Helper()
	for n, tr := range res.Tracks {
		switch {
		case n == bad && (tr.Done || !tr.Damaged || tr.File != "" || tr.Size != 0 || tr.FirstMs != -1):
			t.Errorf("the damaged track: %+v", tr)
		case n != bad && (!tr.Done || tr.Damaged || tr.File == "" || tr.FirstMs < 0):
			t.Errorf("track %d, which is in order: %+v", n, tr)
		}
	}
	if back, err := Load(dir); err != nil || !reflect.DeepEqual(back, res) {
		t.Errorf("read back: %v\n got %+v\nwant %+v", err, back, res)
	}
	for _, name := range onlyOwnNames(t, dir) {
		if strings.HasPrefix(name, "p"+strconv.Itoa(bad)+".") || strings.HasPrefix(name, "t"+strconv.Itoa(bad)+".") {
			t.Errorf("the folder holds %s, a file of the damaged track", name)
		}
	}
}

// One damaged track must not cost a film its other tracks. Here a disc's
// picture track has a piece that says it is longer than it is; ffmpeg
// stops the whole pass over it ("invalid data"). The film is then read
// again for fewer tracks at a time until the damaged one is found, and
// that one alone is left out.
func TestADamagedPictureTrackDoesNotLoseTheFilm(t *testing.T) {
	tl := tools(t)
	film := mixedFilm(t, tl, srtOf("One", "Two", "Three"))
	b := mustRead(t, film)
	// The second caption's first piece: of kind 0x16, 19 bytes long, for a
	// picture of 1920 by 1080. Its length is made 65,535.
	piece := []byte{0x16, 0x00, 0x13, 0x07, 0x80, 0x04, 0x38, 0x10}
	first := bytes.Index(b, piece)
	second := first + 1 + bytes.Index(b[first+1:], piece)
	if first < 0 || second <= first {
		t.Fatal("the film does not hold the picture track's pieces where expected")
	}
	b[second+1], b[second+2] = 0xFF, 0xFF
	dir, res, percents := extracted(t, tl, written(t, "damaged.mkv", b))
	if len(res.Tracks) != 3 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	onlyTheDamagedTrackIsLost(t, dir, res, 2)
	if got := Flatten(mustRead(t, filepath.Join(dir, "s0.ass"))); len(got) != 2 {
		t.Errorf("the styled track: %+v", got)
	}
	if got := string(mustRead(t, filepath.Join(dir, "t1.vtt"))); strings.Count(got, "-->") != 3 {
		t.Errorf("the text track:\n%s", got)
	}
	// However many times the film was read, the reports only ever rose.
	if why := rising(percents); why != "" {
		t.Error(why)
	}
}

// The same for the one sort of text track ffmpeg still converts itself,
// that of MP4 files: when it can read none of a track's captions it gives
// up on the pass ("decode error rate"). The film has two such tracks; the
// captions of the first are spoiled in the file.
func TestADamagedTextTrackInAnMP4DoesNotLoseTheFilm(t *testing.T) {
	tl := tools(t)
	film := filepath.Join(t.TempDir(), "two.mp4")
	srt := written(t, "b.srt", srtOf("One", "Two", "Three"))
	made(t, tl, "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=6", "-f", "lavfi", "-i", "sine=duration=6", "-i", srt, "-i", srt,
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map", "3:s", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-c:s", "mov_text", film)
	b := mustRead(t, film)
	// A caption is kept as its length and its words. Each is in the file
	// twice, once for each track; the first is given a byte that is no
	// character in today's encoding.
	for _, words := range []string{"\x00\x03One", "\x00\x03Two", "\x00\x05Three"} {
		at := bytes.Index(b, []byte(words))
		if at < 0 || bytes.Count(b, []byte(words)) != 2 {
			t.Fatalf("the film does not hold %q twice", words)
		}
		b[at+2] = 0xE5
	}
	dir, res, _ := extracted(t, tl, written(t, "spoiled.mp4", b))
	if len(res.Tracks) != 2 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	onlyTheDamagedTrackIsLost(t, dir, res, 0)
	if got := string(mustRead(t, filepath.Join(dir, "t1.vtt"))); strings.Count(got, "-->") != 3 || !strings.Contains(got, "Three") {
		t.Errorf("the track that is in order:\n%s", got)
	}
}

// How the damaged track is found, with the stand-in playing ffmpeg: the
// film is read for half the tracks at a time. A half that comes out is
// done; a half that fails is halved again; and no track is called damaged
// before it has failed in a reading of its own. The list is of the files
// each reading was for.
func TestFindingTheDamagedTrack(t *testing.T) {
	eight := `{"format": {"start_time": "0.000000", "duration": "100.000000"}, "streams": [{"codec_type": "audio", "codec_name": "ac3"}` +
		strings.Repeat(`, {"codec_type": "subtitle", "codec_name": "ass"}, {"codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle"}`, 4) + `]}`
	alone := `{"format": {"start_time": "0.000000", "duration": "100.000000"}, "streams": [{"codec_type": "audio", "codec_name": "ac3"}, {"codec_type": "subtitle", "codec_name": "ass"}]}`
	for name, c := range map[string]struct {
		how, film string
		damaged   []int
		runs      []string
	}{
		"the last of three": {how: "bad:p2.sup", damaged: []int{2}, runs: []string{
			"fonts/f0 s0.ass t1.mks p2.sup", "fonts/f0 s0.ass", "fonts/f0 t1.mks", "fonts/f0 p2.sup"}},
		"the first of three": {how: "bad:s0.ass", damaged: []int{0}, runs: []string{
			"fonts/f0 s0.ass t1.mks p2.sup", "fonts/f0 s0.ass", "fonts/f0 t1.mks p2.sup"}},
		"the middle one": {how: "bad:t1.mks", damaged: []int{1}, runs: []string{
			"fonts/f0 s0.ass t1.mks p2.sup", "fonts/f0 s0.ass", "fonts/f0 t1.mks", "fonts/f0 p2.sup"}},
		"two of three": {how: "bad:s0.ass,p2.sup", damaged: []int{0, 2}, runs: []string{
			"fonts/f0 s0.ass t1.mks p2.sup", "fonts/f0 s0.ass", "fonts/f0 t1.mks p2.sup", "fonts/f0 t1.mks", "fonts/f0 p2.sup"}},
		// With every track damaged nothing has shown that the film can be
		// read at all, so it is read once more, for nothing but its clock.
		"all three": {how: "bad:s0.ass,t1.mks,p2.sup", damaged: []int{0, 1, 2}, runs: []string{
			"fonts/f0 s0.ass t1.mks p2.sup", "fonts/f0 s0.ass", "fonts/f0 t1.mks p2.sup", "fonts/f0 t1.mks", "fonts/f0 p2.sup", "fonts/f0"}},
		"one of eight": {how: "bad:p5.sup", film: eight, damaged: []int{5}, runs: []string{
			"s0.ass p1.sup s2.ass p3.sup s4.ass p5.sup s6.ass p7.sup", "s0.ass p1.sup s2.ass p3.sup", "s4.ass p5.sup", "s4.ass", "p5.sup", "s6.ass p7.sup"}},
		// A film's only track is read for once more before it is given up.
		"the only one": {how: "bad:s0.ass", film: alone, damaged: []int{0}, runs: []string{"s0.ass", "s0.ass", ""}},
		// What failed once and then no more was not a track's fault.
		"a fault that does not come back": {how: "statuses:183,0", runs: []string{
			"fonts/f0 s0.ass t1.mks p2.sup", "fonts/f0 s0.ass", "fonts/f0 t1.mks", "fonts/f0 p2.sup"}},
		"too many captions that could not be read, once": {how: "statuses:69,0", runs: []string{
			"fonts/f0 s0.ass t1.mks p2.sup", "fonts/f0 s0.ass", "fonts/f0 t1.mks", "fonts/f0 p2.sup"}},
	} {
		t.Run(name, func(t *testing.T) {
			tl := standInTools(t, c.how)
			if c.film != "" {
				t.Setenv(standInFilmVar, c.film)
			}
			runs := standInRuns(t)
			dir, res, percents := extracted(t, tl, "any.mkv")
			if got := runs(); !reflect.DeepEqual(got, c.runs) {
				t.Errorf("the film was read for:\n %q\nwant\n %q", got, c.runs)
			}
			var damaged []int
			for n, tr := range res.Tracks {
				if tr.Damaged {
					damaged = append(damaged, n)
				}
				if tr.Damaged == tr.Done || tr.Damaged != (tr.File == "") {
					t.Errorf("track %d: %+v", n, tr)
				}
			}
			if !reflect.DeepEqual(damaged, c.damaged) {
				t.Errorf("damaged: %v, want %v", damaged, c.damaged)
			}
			// What the folder holds is what the result lists: no file of a
			// damaged track from a reading that failed.
			want := []string{"index.json"}
			for _, tr := range res.Tracks {
				if tr.File != "" {
					want = append(want, tr.File)
				}
			}
			if len(res.Fonts) != strings.Count(c.runs[0], "fonts/") {
				t.Errorf("fonts: %+v", res.Fonts)
			}
			for _, f := range res.Fonts {
				want = append(want, "fonts", f.File)
			}
			if len(res.Fonts) == 0 {
				want = append(want, "fonts")
			}
			got := onlyOwnNames(t, dir)
			slices.Sort(got)
			slices.Sort(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("the folder holds %v, want %v", got, want)
			}
			if back, err := Load(dir); err != nil || !reflect.DeepEqual(back, res) {
				t.Errorf("read back: %v, %+v", err, back)
			}
			// The reports only ever rise, however often the film is read.
			if why := rising(percents); why != "" {
				t.Error(why)
			}
		})
	}
}

// Looking for a damaged track is only for when ffmpeg says the film's
// data is at fault. Anything else that goes wrong, at first or while
// looking, ends the run with nothing kept: a disk that is full must not
// be put down as a damaged track for good.
func TestOtherFaultsAreNotBlamedOnATrack(t *testing.T) {
	mute := `{"format": {"start_time": "0.000000", "duration": "100.000000"}, "streams": [{"codec_type": "subtitle", "codec_name": "ass"}]}`
	for name, c := range map[string]struct {
		how, film string
		status    string
		runs      int
	}{
		"the disk is full":                         {how: "statuses:228", status: "exit status 228", runs: 1},
		"ffmpeg is stopped by something else":      {how: "statuses:255", status: "exit status 255", runs: 1},
		"any other ending":                         {how: "statuses:1", status: "exit status 1", runs: 1},
		"the disk fills up while looking":          {how: "statuses:183,228", status: "exit status 228", runs: 2},
		"the disk fills up later while looking":    {how: "statuses:183,0,228", status: "exit status 228", runs: 3},
		"the drive drops out while looking":        {how: "statuses:183,254", status: "exit status 254", runs: 2},
		"a film that is damaged all through":       {how: "statuses:183", status: "exit status 183", runs: 6},
		"a damaged track and nothing to read by":   {how: "bad:s0.ass", film: mute, status: "exit status 183", runs: 2},
		"a film that cannot be read when asked to": {how: "statuses:183,183,183,183,183,1", status: "exit status 183", runs: 6},
	} {
		t.Run(name, func(t *testing.T) {
			tl := standInTools(t, c.how)
			if c.film != "" {
				t.Setenv(standInFilmVar, c.film)
			}
			runs := standInRuns(t)
			dir := filepath.Join(t.TempDir(), "subs")
			var percents []int
			res, err := Extract(context.Background(), tl, "any.mkv", dir, func(p int) { percents = append(percents, p) })
			if err == nil || !strings.Contains(err.Error(), c.status) {
				t.Errorf("error: %v (result %+v), want one with %q", err, res, c.status)
			}
			if got := runs(); len(got) != c.runs {
				t.Errorf("ffmpeg was run %d times, want %d: %q", len(got), c.runs, got)
			}
			if len(percents) > 0 && percents[len(percents)-1] == 100 {
				t.Errorf("a run that failed reported 100: %v", percents)
			}
			nothingKept(t, dir)
		})
	}
}

// A track that calls itself text and turns out, when copied, not to be
// (or to hold more than any film has words) is left out like a damaged
// one; the film's other tracks are a result.
func TestATextTrackThatIsNotTextIsLeftOut(t *testing.T) {
	dir, res, percents := extracted(t, standInTools(t, "odd-txt"), "any.mkv")
	if len(res.Tracks) != 3 {
		t.Fatalf("tracks: %+v", res.Tracks)
	}
	onlyTheDamagedTrackIsLost(t, dir, res, 1)
	if got := strings.Join(onlyOwnNames(t, dir), " "); got != "fonts fonts/f0 index.json p2.sup s0.ass" {
		t.Errorf("the folder holds: %s", got)
	}
	if why := rising(percents); why != "" {
		t.Error(why)
	}
}
