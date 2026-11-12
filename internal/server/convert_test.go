package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// These tests cover a film whose picture has to be converted, from the
// Play button to the chunks: the decision (package playback), the facts
// and the cuts (package hls) and the answer the page gets.

// oldFilm puts a film in the library whose picture no browser plays:
// MPEG-4 Part 2 (the format of DivX and Xvid) in an MKV, with Dolby sound.
// rate is how many pictures a second it has.
func oldFilm(t *testing.T, e *env, name, rate string) {
	t.Helper()
	out, err := exec.Command(probe.FindFFmpeg(), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate="+rate+":duration=14", "-f", "lavfi", "-i", "sine=frequency=440:duration=14",
		"-c:v", "mpeg4", "-q:v", "6", "-g", "100", "-c:a", "ac3", filepath.Join(e.media, name)).CombinedOutput()
	if err != nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not make %s: %v %s", name, err, out)
		}
		t.Skipf("this ffmpeg cannot make the test film: %v %s", err, out)
	}
}

// convertedAnswer is the part of a Play answer these tests look at.
type convertedAnswer struct {
	URL  string
	HLS  string
	Plan struct {
		Playable, Method string
		Reasons, Lost    []string
		AudioCopy        bool
		HasAudio         *bool
		PCCannot         bool
		Video            playback.Video
		Picture          playback.PictureKind
	}
}

// streamOf fetches a film's header and its chunks from, to and puts them
// together as one file a video tool can read.
func streamOf(t *testing.T, e *env, listURL string, chunks ...string) string {
	t.Helper()
	base := strings.TrimSuffix(listURL, "index.m3u8")
	var stream bytes.Buffer
	for _, name := range append([]string{"init.mp4"}, chunks...) {
		w := e.do("GET", base+name, remote, nil, nil)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		stream.Write(w.Body.Bytes())
	}
	joined := filepath.Join(t.TempDir(), "joined.mp4")
	if err := os.WriteFile(joined, stream.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return joined
}

// described asks ffprobe for some facts about each track of a file, one
// track to a line.
func described(t *testing.T, file, entries string) []string {
	t.Helper()
	out, err := exec.Command(probe.Find(), "-v", "error", "-show_entries", "stream="+entries, "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatalf("ffprobe could not read the stream: %v", err)
	}
	lines := strings.Fields(string(out))
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], ",") // ffprobe ends some lines with a comma
	}
	return lines
}

// A film in a format no browser plays is converted when Play is pressed:
// the answer says so and why, the list of chunks is cut every three
// seconds, and what arrives is H.264 with the sound converted too, at the
// film's own size.
func TestAFilmNoBrowserPlaysIsConverted(t *testing.T) {
	e := newConvertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Old Film")
	if w := e.do("POST", "/api/capabilities", remote, c, chromeLike); w.Code != 200 {
		t.Fatal(w.Body)
	}
	// The list no longer calls it unplayable.
	var movies struct{ Items []itemView }
	decode(t, e.do("GET", "/api/movies", remote, c, nil), &movies)
	for _, it := range movies.Items {
		if it.ID == film && it.Playable != "yes" {
			t.Errorf("the list says %q for a film that will be converted", it.Playable)
		}
	}

	var p convertedAnswer
	w := e.do("POST", "/api/items/"+film+"/play", remote, c, nil)
	decode(t, w, &p)
	if p.Plan.Playable != "yes" || p.Plan.Method != "convert" || p.HLS == "" || p.Plan.Video.Rung != 1 || p.Plan.Video.Encoder != "x264" {
		t.Fatalf("play: %s", w.Body)
	}
	if p.Plan.AudioCopy || p.Plan.HasAudio == nil || !*p.Plan.HasAudio {
		t.Errorf("the sound is Dolby, which this device does not play, so it is converted: %s", w.Body)
	}
	if why := strings.Join(p.Plan.Reasons, " "); !strings.Contains(why, "MPEG-4") || strings.Contains(why, "later version") {
		t.Errorf("the reasons should name the picture's format and promise nothing: %q", p.Plan.Reasons)
	}
	if p.Plan.Picture.Codec != "h264" || p.Plan.Picture.BitDepth != 8 {
		t.Errorf("the device is sent %+v", p.Plan.Picture)
	}
	list := e.do("GET", p.URL, remote, nil, nil)
	if n := strings.Count(list.Body.String(), "#EXTINF"); list.Code != 200 || n != 5 {
		t.Fatalf("14 seconds cut every 3 should be 5 chunks, got %d: %s", n, list.Body)
	}
	if n := e.s.hls.Converting(""); n != 0 {
		t.Errorf("%d conversions run before a chunk was asked for", n)
	}
	joined := streamOf(t, e, p.URL, "seg-0.m4s", "seg-1.m4s")
	if got := described(t, joined, "codec_name,width,height"); len(got) != 2 || got[0] != "h264,320,180" || got[1] != "aac" {
		t.Errorf("the stream holds %q, want H.264 at the film's own size and AAC sound", got)
	}
	// While it runs it counts as work the PC is doing, for every other
	// viewer and not for this one.
	var sessionID string
	e.s.db.QueryRow(`SELECT id FROM sessions LIMIT 1`).Scan(&sessionID)
	if all, others := e.s.hls.Converting(""), e.s.hls.Converting(sessionID); all != 1 || others != 0 {
		t.Errorf("conversions counted: %d in all and %d of other viewers, want 1 and 0", all, others)
	}
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)

	// A device that plays Dolby gets the film's own sound beside the new
	// picture.
	withDolby := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true, "ac3": true}, "hls": true}
	e.do("POST", "/api/capabilities", remote, c, withDolby)
	p = convertedAnswer{}
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.Method != "convert" || !p.Plan.AudioCopy {
		t.Fatalf("play on a device with Dolby: %+v", p.Plan)
	}
	if got := described(t, streamOf(t, e, p.URL, "seg-0.m4s"), "codec_name"); len(got) != 2 || got[0] != "h264" || got[1] != "ac3" {
		t.Errorf("the stream holds %q, want H.264 and the untouched Dolby sound", got)
	}
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
}

// A viewer can ask for a lighter picture. At the setting that leaves out
// every second picture of a fast film, the film really arrives at half its
// rate, and the answer says what is lost.
func TestALighterPictureLeavesOutEverySecondPictureOfAFastFilm(t *testing.T) {
	e := newConvertEnv(t)
	oldFilm(t, e, "Fast Film (2001).mkv", "50")
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Fast Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)

	var p convertedAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, map[string]any{"lower": 4}), &p)
	if p.Plan.Method != "convert" || p.Plan.Video.Rung != 4 || !strings.Contains(strings.Join(p.Plan.Lost, " "), "second picture") {
		t.Fatalf("play with a lighter picture asked for: %+v", p.Plan)
	}
	got := described(t, streamOf(t, e, p.URL, "seg-0.m4s", "seg-1.m4s"), "codec_name,r_frame_rate")
	if len(got) != 2 || got[0] != "h264,25/1" {
		t.Errorf("the stream holds %q, want the picture at 25 a second", got)
	}
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)

	// A wish that makes no sense is ignored.
	for _, lower := range []int{-3, 99} {
		p = convertedAnswer{}
		decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, map[string]any{"lower": lower}), &p)
		if p.Plan.Method != "convert" || p.Plan.Video.Rung != 1 {
			t.Errorf("lower=%d: %+v", lower, p.Plan)
		}
		e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
	}
}

// Where the converter on this PC cannot convert pictures (a build without
// the encoder), the film is refused as before, and the answer says it is
// the PC that cannot.
func TestAConverterThatCannotConvertPictures(t *testing.T) {
	e := newConvertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Old Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	e.s.abilities() // wait for the answer asked for at start, then replace it
	e.s.helpers.convert.mu.Lock()
	e.s.helpers.convert.can = hls.Abilities{}
	e.s.helpers.convert.mu.Unlock()

	var p convertedAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.Playable != "no" || p.Plan.Method != "none" || p.HLS != "" {
		t.Fatalf("play: %+v", p)
	}
	if n := e.s.hls.Sessions(); n != 0 {
		t.Errorf("%d conversions were opened", n)
	}
}

// However many devices press Play on a film at once, its picture is looked
// at once, and the answer is kept for the next Play.
func TestOneLookAtAFilmsPictureServesEveryPlay(t *testing.T) {
	e := newConvertEnv(t)
	oldFilm(t, e, "First Old Film (1998).mkv", "25")
	oldFilm(t, e, "Second Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	first, second := e.firstItem(c, "First Old Film"), e.firstItem(c, "Second Old Film")
	ffprobe := newCounted(t, `'`+e.s.ffprobe+`' "$@"`)
	e.s.ffprobe = ffprobe.Path

	play := func(item string) *httptest.ResponseRecorder {
		w := e.do("POST", "/api/items/"+item+"/play", remote, c, nil)
		var p convertedAnswer
		decode(t, w, &p)
		if w.Code != 200 || p.Plan.Method != "convert" || p.HLS == "" {
			t.Errorf("play: %d %s", w.Code, w.Body)
		}
		return w
	}
	// How many times one look runs ffprobe is the converter's business; it
	// is measured on the first film.
	play(first)
	one := ffprobe.runs()
	if one == 0 {
		t.Fatal("converting a film for the first time did not run ffprobe at all, so this test proves nothing")
	}
	together(10, func(int) *httptest.ResponseRecorder { return play(second) })
	if n := ffprobe.runs() - one; n != one {
		t.Errorf("10 Play calls for one film ran ffprobe %d times; one look takes %d", n, one)
	}
	play(second)
	play(first)
	if n := ffprobe.runs(); n != 2*one {
		t.Errorf("ffprobe ran %d times in all, want %d: what a look finds is kept", n, 2*one)
	}
}

// What a look at a film's picture finds is kept in the database, so the
// film is not looked at again after the program restarts. It is kept for
// the file as it was: a file that has changed is looked at afresh, and so
// is one whose kept facts cannot be read.
func TestWhatALookAtAFilmsPictureFindsIsKeptInTheDatabase(t *testing.T) {
	e := newConvertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	film := e.firstItem(c, "Old Film")
	file := e.fileOf(film)
	play := func(when string) {
		t.Helper()
		var p convertedAnswer
		w := e.do("POST", "/api/items/"+film+"/play", remote, c, nil)
		decode(t, w, &p)
		if p.Plan.Method != "convert" || p.HLS == "" {
			t.Fatalf("%s: play: %s", when, w.Body)
		}
		e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
	}
	kept := func() (stamp string, facts hls.PictureFacts) {
		t.Helper()
		var raw string
		if err := e.s.db.QueryRow(`SELECT stamp, facts FROM picture_facts WHERE file_id=?`, file).Scan(&stamp, &raw); err != nil {
			t.Fatalf("nothing is kept about the film's picture: %v", err)
		}
		if err := json.Unmarshal([]byte(raw), &facts); err != nil {
			t.Fatalf("what is kept cannot be read: %v", err)
		}
		return stamp, facts
	}
	play("the first time")
	stamp, facts := kept()
	if facts.Width != 320 || facts.Height != 180 || facts.RateNum != 25 || facts.RateDen != 1 || !strings.HasSuffix(stamp, "-"+factsForm) {
		t.Fatalf("kept under %q: %+v", stamp, facts)
	}
	// Whatever a look finds comes back from the database as it went in.
	if again, ok := e.s.keptFacts(file, stamp); !ok || !reflect.DeepEqual(again, facts) {
		t.Errorf("read back as %+v", again)
	}
	// From here on every run of the inspector is counted.
	ffprobe := newCounted(t, `'`+e.s.ffprobe+`' "$@"`)
	e.s.ffprobe = ffprobe.Path
	play("with the facts kept")
	if n := ffprobe.runs(); n != 0 {
		t.Errorf("the film was looked at again although what was found is kept (%d runs)", n)
	}
	// The file changes (the scan notes its new date): it is looked at
	// again, and what is kept is replaced.
	e.s.db.Exec(`UPDATE files SET mtime = mtime + 60 WHERE id=?`, file)
	play("after the file changed")
	one := ffprobe.runs()
	if newStamp, _ := kept(); one == 0 || newStamp == stamp {
		t.Errorf("after the file changed: %d runs of the inspector, kept under %q (was %q)", one, newStamp, stamp)
	}
	play("once more")
	if n := ffprobe.runs(); n != one {
		t.Errorf("the changed file was looked at twice (%d runs, one look takes %d)", n, one)
	}
	// Kept facts that cannot be read are no facts.
	e.s.db.Exec(`UPDATE picture_facts SET facts='{"start": "soon"' WHERE file_id=?`, file)
	play("with kept facts that cannot be read")
	if _, facts := kept(); ffprobe.runs() != 2*one || facts.Width != 320 {
		t.Errorf("after the kept facts were spoilt: %d runs, now kept %+v", ffprobe.runs(), facts)
	}
	// When the film leaves the library, what was kept about it goes too.
	e.s.db.Exec(`DELETE FROM files WHERE id=?`, file)
	if n := e.count(`SELECT count(*) FROM picture_facts`); n != 0 {
		t.Errorf("%d films' facts are kept after the film was removed", n)
	}
}

// A film whose picture cannot be made sense of is refused in plain words,
// with nothing left open.
func TestAFilmWhosePictureCannotBeReadIsNotConverted(t *testing.T) {
	e := newConvertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Old Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	e.s.ffprobe = script(t, "exit 1")

	var p convertedAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.Playable != "no" || p.Plan.Method != "none" || p.HLS != "" || !p.Plan.PCCannot ||
		!strings.Contains(strings.Join(p.Plan.Reasons, " "), "could not work out how to convert") {
		t.Fatalf("play: %+v", p)
	}
	if n := e.s.hls.Sessions(); n != 0 {
		t.Errorf("%d conversions were opened for a film that cannot be converted", n)
	}
}

// The settings a play is converted at come from the rung the decision
// picked and the film's own shape and rate.
func TestTheSettingsOfAConversion(t *testing.T) {
	film := hls.PictureFacts{StartMs: 0, LastMs: 59960, EndMs: 60000, RateNum: 25, RateDen: 1, Width: 1920, Height: 1080, SarNum: 1, SarDen: 1, Format: "matroska,webm"}
	for _, tc := range []struct {
		name          string
		facts         func(hls.PictureFacts) hls.PictureFacts
		rung          int
		w, h          int
		num, den      int64
		preset        hls.Preset
		rateK, chunks int
	}{
		{"full HD at the best setting", nil, 1, 1920, 1080, 25, 1, hls.VeryFast, 10000, 20},
		{"the second setting spends less effort and may spend more bits", nil, 2, 1920, 1080, 25, 1, hls.SuperFast, 12000, 20},
		{"720p", nil, 3, 1280, 720, 25, 1, hls.VeryFast, 5000, 20},
		{"480p", nil, 5, 854, 480, 25, 1, hls.UltraFast, 2500, 20},
		{"a small film is never enlarged", func(f hls.PictureFacts) hls.PictureFacts { f.Width, f.Height = 640, 360; return f }, 1, 640, 360, 25, 1, hls.VeryFast, 10000, 20},
		{"a slow film keeps its rate at the setting that halves fast ones", nil, 4, 1280, 720, 25, 1, hls.SuperFast, 5000, 20},
		{"50 a second is halved there", func(f hls.PictureFacts) hls.PictureFacts { f.RateNum = 50; return f }, 4, 1280, 720, 25, 1, hls.SuperFast, 5000, 20},
		{"and so is 59.94", func(f hls.PictureFacts) hls.PictureFacts { f.RateNum, f.RateDen = 60000, 1001; return f }, 4, 1280, 720, 30000, 1001, hls.SuperFast, 5000, 20},
		{"but not at any other setting", func(f hls.PictureFacts) hls.PictureFacts { f.RateNum = 50; return f }, 3, 1280, 720, 50, 1, hls.VeryFast, 5000, 20},
	} {
		f := film
		if tc.facts != nil {
			f = tc.facts(f)
		}
		g, c, err := convertSpec(f, playback.Video{Rung: tc.rung})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if c.W != tc.w || c.H != tc.h || c.Num != tc.num || c.Den != tc.den || c.Preset != tc.preset || c.MaxRateK != tc.rateK || len(g.Cuts) != tc.chunks || len(c.Targets) != tc.chunks {
			t.Errorf("%s: %dx%d at %d/%d, effort %d, %dk, %d chunks", tc.name, c.W, c.H, c.Num, c.Den, c.Preset, c.MaxRateK, len(g.Cuts))
		}
	}
	// What cannot be converted by is refused, not guessed at.
	for name, bad := range map[string]struct {
		f    hls.PictureFacts
		rung int
	}{
		"no such setting":      {film, 0},
		"a setting too many":   {film, playback.NumRungs + 1},
		"no frame rate":        {hls.PictureFacts{EndMs: 60000, Width: 1920, Height: 1080, SarNum: 1, SarDen: 1}, 1},
		"no size":              {hls.PictureFacts{EndMs: 60000, RateNum: 25, RateDen: 1}, 1},
		"no length and no end": {hls.PictureFacts{RateNum: 25, RateDen: 1, Width: 1920, Height: 1080, SarNum: 1, SarDen: 1}, 1},
	} {
		if _, c, err := convertSpec(bad.f, playback.Video{Rung: bad.rung}); err == nil || c != nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What the library noted about a picture is read the way the decision
// expects it.
func TestReadingWhatTheLibraryKnowsAboutAPicture(t *testing.T) {
	var v playback.Track
	pictureOf(&v, 3840, 2160, "24000/1001", "smpte2084", "dovi:5")
	if v.Width != 3840 || v.Height != 2160 || v.FrameRate < 23.97 || v.FrameRate > 23.98 || v.Transfer != playback.PQ || v.DoVi != 5 {
		t.Errorf("a 4K Dolby Vision film was read as %+v", v)
	}
	for text, want := range map[string]float64{"25": 25, "50/1": 50, "30000/1001": 29.97002997002997, "": 0, "0/0": 0, "abc": 0, "-5": 0, "25/0": 0} {
		if got := frameRateOf(text); got != want {
			t.Errorf("frame rate %q read as %v, want %v", text, got, want)
		}
	}
	for text, want := range map[string]string{"smpte2084": playback.PQ, "arib-std-b67": playback.HLG, "bt709": playback.SDR, "": playback.SDR} {
		if got := transferOf(text); got != want {
			t.Errorf("brightness %q read as %q, want %q", text, got, want)
		}
	}
	for text, want := range map[string]int{"dovi:5": 5, "dovi:8": 8, "": 0, "dovi:": 0, "dovi:x": 0, "other, dovi:7": 7, "dovi:-1": 0} {
		if got := doviOf(text); got != want {
			t.Errorf("Dolby Vision note %q read as %d, want %d", text, got, want)
		}
	}
}

// A report from a page of the version before pictures could be converted
// says nothing about sizes above 1080p. A 4K film must go on playing on
// such a device as it did, not be converted because nobody asked.
func TestAnOlderPagesReportIsReadAsItWas(t *testing.T) {
	old := playback.Caps{Containers: map[string]bool{"mp4": true}, Video: map[string]bool{"hevc": true, "h264": true}, Audio: map[string]bool{"aac": true}, HLS: true}
	applyOverrides(&old, "Mozilla/5.0 Chrome/130")
	if !old.Tall["hevc"] || !old.Tall["h264"] {
		t.Errorf("an older page's report: %+v", old.Tall)
	}
	asked := playback.Caps{Containers: map[string]bool{"mp4": true}, Video: map[string]bool{"hevc": true}, Tall: map[string]bool{}}
	applyOverrides(&asked, "Mozilla/5.0 Chrome/130")
	if len(asked.Tall) != 0 {
		t.Errorf("a device that says it plays nothing above 1080p was read as %+v", asked.Tall)
	}
	film := playback.Media{Probed: true, Container: "mp4", Video: []playback.Track{{Codec: "hevc", Tag: "hvc1", BitDepth: 8, Width: 3840, Height: 2160}},
		Audio: []playback.Track{{Codec: "aac", Channels: 2}}}
	can := playback.Limits{Convert: true}
	if p := playback.DecideFor(film, &old, playback.Want{Audio: -1, Subtitle: -1}, can); p.Method != playback.Direct {
		t.Errorf("a 4K film on a device with an older page's report: %+v", p)
	}
}

// The page reads the decision by the names of its fields. Every name the
// server sends has to be one the page's own description of the answer
// knows, so that a field renamed on one side is noticed on the other.
func TestThePageKnowsEveryFieldOfTheDecision(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "api.ts"))
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(src, []byte("export interface PlayPlan {"))
	if start < 0 {
		t.Fatal("web/src/api.ts has no PlayPlan")
	}
	block := src[start:]
	block = block[:bytes.Index(block, []byte("\n}\n"))]
	known := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+(\w+)\??:`).FindAllSubmatch(block, -1) {
		known[string(m[1])] = true
	}
	raw, _ := json.Marshal(viewOf(playback.Plan{}, true))
	var sent map[string]any
	json.Unmarshal(raw, &sent)
	if len(sent) < 10 {
		t.Fatalf("the decision was sent as %s", raw)
	}
	for name := range sent {
		if !known[name] {
			t.Errorf("the server sends %q in a decision, which PlayPlan in web/src/api.ts does not list", name)
		}
	}
	for name := range known {
		if _, ok := sent[name]; !ok {
			t.Errorf("PlayPlan in web/src/api.ts lists %q, which the server does not send", name)
		}
	}
	// The picture a film is converted to has names of its own.
	video := reflect.TypeOf(playback.Video{})
	for i := 0; i < video.NumField(); i++ {
		name := strings.Split(video.Field(i).Tag.Get("json"), ",")[0]
		if !regexp.MustCompile(`(?m)^\s+` + name + `\??:`).Match(src) {
			t.Errorf("the server sends %q about a converted picture, which web/src/api.ts does not list", name)
		}
	}
}
