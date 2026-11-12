package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
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
	URL        string
	HLS        string
	Explain    string
	DurationMs int64
	Plan       struct {
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
	e := convertEnv(t)
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
	// So when this viewer presses Play again (to change the sound, say) the
	// film is still made at the best setting, while a film started from
	// another sign-in meanwhile is made one setting lighter.
	var again, other convertedAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &again)
	guest := sessionCookie(e.do("POST", "/api/login", remote, nil, map[string]string{"name": "owner", "password": "a good password"}))
	if guest == nil {
		t.Fatal("the second sign-in failed")
	}
	e.do("POST", "/api/capabilities", remote, guest, chromeLike)
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, guest, nil), &other)
	if again.Plan.Method != "convert" || again.Plan.Video.Rung != 1 || other.Plan.Method != "convert" || other.Plan.Video.Rung != 2 {
		t.Errorf("while one film is being converted: the same viewer's next Play is at setting %d (want 1), another sign-in's at setting %d (want 2)",
			again.Plan.Video.Rung, other.Plan.Video.Rung)
	}
	// The lists go by an idle PC: a film is not judged by what the PC
	// happens to be doing this minute. Asked as the other sign-in, for
	// whom one conversion is under way, the film is still judged at the
	// best setting.
	var guestSession, owner string
	e.s.db.QueryRow(`SELECT id, user_id FROM sessions WHERE id <> ?`, sessionID).Scan(&guestSession, &owner)
	copies, err := e.s.copiesOf(film)
	if err == nil {
		err = e.s.judge(&User{ID: owner, SessionID: guestSession}, copies)
	}
	if err != nil || len(copies) != 1 || copies[0].plan.Method != playback.Convert || copies[0].plan.Video.Rung != 1 {
		t.Errorf("the lists judge the film for a second viewer as %+v (%v), want converted at setting 1", copies, err)
	}
	e.do("POST", "/api/hls/"+again.HLS+"/stop", remote, c, nil)
	e.do("POST", "/api/hls/"+other.HLS+"/stop", remote, guest, nil)
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
	e := convertEnv(t)
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
	e := convertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Old Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	converterAnswered(t, e) // the answer asked for at start is waited for, then replaced
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
	// The fault: the answer still said "Converting the picture arrives in a
	// later version" and did not name the PC, so the page went on to
	// suggest another browser.
	it := listed(t, e, c, film)
	for what, reasons := range map[string][]string{"Play": p.Plan.Reasons, "the list": it.Reasons} {
		why := strings.Join(reasons, " ")
		if !strings.Contains(why, "MPEG-4") || !strings.Contains(why, "The converter on this PC cannot convert pictures") || strings.Contains(why, "later version") {
			t.Errorf("%s gives the reasons %q", what, reasons)
		}
	}
	if !p.Plan.PCCannot || !strings.Contains(p.Explain, "this PC cannot convert it") || it.Playable != "no" || it.Method != "none" {
		t.Errorf("it is the PC that cannot: pcCannot=%v, %q, and the list says %s/%s", p.Plan.PCCannot, p.Explain, it.Playable, it.Method)
	}
}

// However many devices press Play on a film at once, its picture is looked
// at once, and the answer is kept for the next Play.
func TestOneLookAtAFilmsPictureServesEveryPlay(t *testing.T) {
	e := convertEnv(t)
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

// A film whose picture cannot be made sense of is refused in plain words,
// with nothing left open.
func TestAFilmWhosePictureCannotBeReadIsNotConverted(t *testing.T) {
	e := convertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Old Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	asked := filepath.Join(t.TempDir(), "asked")
	e.s.ffprobe = script(t, "echo x >> '"+asked+"'\nexit 1")
	looks := func() int {
		b, _ := os.ReadFile(asked)
		return bytes.Count(b, []byte("\n"))
	}

	refused := func(when string) {
		t.Helper()
		var p convertedAnswer
		decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
		if p.Plan.Playable != "no" || p.Plan.Method != "none" || p.HLS != "" || !p.Plan.PCCannot ||
			!strings.Contains(strings.Join(p.Plan.Reasons, " "), "could not work out how to convert") {
			t.Fatalf("play %s: %+v", when, p)
		}
		// The sentence above the reasons is about the refusal, not about
		// the conversion that was planned before the look failed.
		if !strings.Contains(p.Explain, "this PC cannot convert it") {
			t.Errorf("play %s is explained as %q", when, p.Explain)
		}
		if n := e.s.hls.Sessions(); n != 0 {
			t.Errorf("%d conversions were opened for a film that cannot be converted", n)
		}
	}
	refused("the first time")
	one := looks()
	if one == 0 {
		t.Fatal("ffprobe was not run at all, so this test proves nothing")
	}
	// A look that failed is not made again on every Play: pressing Play
	// once more gets the same answer at once.
	refused("again")
	if n := looks(); n != one {
		t.Errorf("pressing Play again ran ffprobe %d more times", n-one)
	}
	// It is remembered for the file as it was. A file that has changed
	// since is looked at afresh.
	var rel, lib string
	e.s.db.QueryRow(`SELECT f.rel_path, l.path FROM files f JOIN libraries l ON l.id=f.library_id WHERE f.rel_path LIKE 'Old Film%'`).Scan(&rel, &lib)
	f, err := os.OpenFile(filepath.Join(lib, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0})
	f.Close()
	refused("after the file changed")
	if n := looks(); n <= one {
		t.Error("a file that changed after a failed look was not looked at again")
	}
}

// The settings a play is converted at come from the rung the decision
// picked and the film's own shape and rate.
func TestTheSettingsOfAConversion(t *testing.T) {
	film := hls.PictureFacts{StartMs: 0, LastMs: 59960, EndMs: 60000, RateNum: 25, RateDen: 1, Width: 1920, Height: 1080, SarNum: 1, SarDen: 1, Format: "matroska,webm"}
	upright := func(f hls.PictureFacts) hls.PictureFacts { f.Width, f.Height = 1080, 1920; return f }
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
		// A phone held upright: as much picture as the same clip lying
		// down, so it is fitted to the frame stood on end.
		{"a clip filmed upright stays whole at 1080p", upright, 1, 1080, 1920, 25, 1, hls.VeryFast, 10000, 20},
		{"also when it is stored lying down and marked as turned", func(f hls.PictureFacts) hls.PictureFacts { f.Rotation = 90; return f }, 1, 1080, 1920, 25, 1, hls.VeryFast, 10000, 20},
		{"upright at 720p", upright, 3, 720, 1280, 25, 1, hls.VeryFast, 5000, 20},
		{"upright at 480p", upright, 5, 480, 854, 25, 1, hls.UltraFast, 2500, 20},
		{"a 720p clip filmed upright is not shrunk at 1080p", func(f hls.PictureFacts) hls.PictureFacts { f.Width, f.Height, f.Rotation = 1280, 720, 270; return f }, 1, 720, 1280, 25, 1, hls.VeryFast, 10000, 20},
		{"a very tall clip is fitted by its height", func(f hls.PictureFacts) hls.PictureFacts { f.Width, f.Height = 1080, 2340; return f }, 1, 886, 1920, 25, 1, hls.VeryFast, 10000, 20},
		// 1088 lines is how some files store 1080p.
		{"1088 lines stay as they are at 1080p", func(f hls.PictureFacts) hls.PictureFacts { f.Height = 1088; return f }, 1, 1920, 1088, 25, 1, hls.VeryFast, 10000, 20},
		{"upright as well", func(f hls.PictureFacts) hls.PictureFacts { f.Width, f.Height = 1088, 1920; return f }, 2, 1088, 1920, 25, 1, hls.SuperFast, 12000, 20},
		{"and are made smaller like any other picture at 720p", func(f hls.PictureFacts) hls.PictureFacts { f.Height = 1088; return f }, 3, 1270, 720, 25, 1, hls.VeryFast, 5000, 20},
		{"a picture really larger than 1080p is not let through", func(f hls.PictureFacts) hls.PictureFacts { f.Height = 1200; return f }, 1, 1728, 1080, 25, 1, hls.VeryFast, 10000, 20},
		{"a DVD's narrow pixels are made square", func(f hls.PictureFacts) hls.PictureFacts {
			f.Width, f.Height, f.SarNum, f.SarDen = 720, 576, 64, 45
			return f
		}, 1, 1024, 576, 25, 1, hls.VeryFast, 10000, 20},
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

	// Nor did that page ask whether the device shows Dolby Vision of the
	// kind that needs it (profile 5). Such a film played on the device as
	// it was, and must not be refused with words that say the device was
	// asked and cannot, until the page has been loaded again and asks.
	vision := playback.Media{Probed: true, Container: "mp4", Video: []playback.Track{{Codec: "hevc", Tag: "hvc1", BitDepth: 10, Width: 1920, Height: 1080, Transfer: playback.PQ, DoVi: 5}},
		Audio: []playback.Track{{Codec: "aac", Channels: 2}}}
	tv := func() playback.Caps {
		return playback.Caps{Containers: map[string]bool{"mp4": true}, Video: map[string]bool{"hevc": true, "hevc10": true, "h264": true}, Audio: map[string]bool{"aac": true}, HLS: true}
	}
	released := tv()
	applyOverrides(&released, "Mozilla/5.0 Safari/605")
	if p := playback.DecideFor(vision, &released, playback.Want{Audio: -1, Subtitle: -1}, can); p.Method != playback.Direct {
		t.Errorf("a Dolby Vision film on a device with an older page's report: %s/%s %q", p.Playable, p.Method, p.Reasons)
	}
	today := tv()
	today.Video["dovi5"] = false // a page that asks sends the answer
	applyOverrides(&today, "Mozilla/5.0 Safari/605")
	if p := playback.DecideFor(vision, &today, playback.Want{Audio: -1, Subtitle: -1}, can); p.Method == playback.Direct {
		t.Error("a device that was asked and does not show this kind of Dolby Vision is sent the film as it is")
	}
	// A report that lists no picture formats at all is read without harm.
	bare := playback.Caps{Containers: map[string]bool{"mp4": true}}
	applyOverrides(&bare, "Mozilla/5.0 Chrome/130")
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

// until waits for something to become true, for as long as a slow machine
// may need.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Minute); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("waited two minutes for %s", what)
		}
	}
}

// converterAnswered waits until ffmpeg has said what it can do. The server
// itself waits only a moment for that (see abilities); a test that is
// about converting must not depend on how fast this machine starts ffmpeg.
func converterAnswered(t *testing.T, e *env) {
	t.Helper()
	until(t, "ffmpeg to say what it can do", func() bool { _, known := e.s.abilities(); return known })
}

// convertEnv is newConvertEnv once its converter has said what it can do.
func convertEnv(t *testing.T) *env {
	t.Helper()
	e := newConvertEnv(t)
	converterAnswered(t, e)
	return e
}

// heldProgram is a small program that stands in for ffmpeg or ffprobe.
// While the file "hold" lies in its folder it waits; while "fail" does, it
// fails; otherwise it runs the real program. Every start is noted.
type heldProgram struct {
	dir, Path string
}

func newHeldProgram(t *testing.T, real string) *heldProgram {
	t.Helper()
	dir := t.TempDir()
	return &heldProgram{dir: dir, Path: script(t, `d='`+dir+`'
echo "$*" >> "$d/asked"
while [ -e "$d/hold" ]; do sleep 0.05; done
if [ -e "$d/fail" ]; then exit 1; fi
exec '`+real+`' "$@"`)}
}

func (p *heldProgram) set(t *testing.T, name string, on bool) {
	t.Helper()
	var err error
	if on {
		err = os.WriteFile(filepath.Join(p.dir, name), nil, 0o600)
	} else {
		err = os.Remove(filepath.Join(p.dir, name))
	}
	if err != nil {
		t.Fatal(err)
	}
}

// starts is how many times the program was started.
func (p *heldProgram) starts() int {
	b, _ := os.ReadFile(filepath.Join(p.dir, "asked"))
	return bytes.Count(b, []byte("\n"))
}

// newEnvWith is a server that uses the given programs as its ffmpeg and
// ffprobe, with a library that holds nothing yet.
func newEnvWith(t *testing.T, ffmpeg, ffprobe string) *env {
	t.Helper()
	data := t.TempDir()
	d, err := db.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := New(Config{DB: d, DataDir: data, Version: "test", Port: 8177, Assets: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}},
		FFprobe: ffprobe, FFmpeg: ffmpeg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &env{t: t, s: s, media: t.TempDir()}
}

// realPrograms finds ffmpeg and ffprobe, or skips the test (fails it where
// LM_REQUIRE_FFMPEG says they must be there).
func realPrograms(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, ffprobe = probe.FindFFmpeg(), probe.Find()
	if ffmpeg == "" || ffprobe == "" {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg and ffprobe were not found, and LM_REQUIRE_FFMPEG says they must be")
		}
		t.Skip("ffmpeg and ffprobe are not installed here")
	}
	return ffmpeg, ffprobe
}

// The lists and Play must not wait for ffmpeg to say what it can do: the
// program is large, and on a PC that is busy starting up it can take its
// time, or hang. The fault: whoever asked first held a lock while ffmpeg
// ran, and every list and every Play queued behind it, for up to half a
// minute.
func TestNoListWaitsWhileTheConverterIsAsked(t *testing.T) {
	real, ffprobe := realPrograms(t)
	slow := newHeldProgram(t, real)
	slow.set(t, "hold", true) // ffmpeg does not answer until the test lets it
	usual := abilityWait
	abilityWait = time.Second
	t.Cleanup(func() { abilityWait = usual })
	e := newEnvWith(t, slow.Path, ffprobe)
	// Whoever asks in the program's first moments waits a little for the
	// answer, and stops waiting while ffmpeg is still being asked.
	_, known := e.s.abilities()
	e.s.helpers.convert.mu.Lock()
	stillAsking := e.s.helpers.convert.asking != nil
	e.s.helpers.convert.mu.Unlock()
	if known || !stillAsking {
		t.Fatalf("asking in the first moments waited until ffmpeg was given up on (answered: %v)", known)
	}
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	film := e.firstItem(c, "Old Film")

	// The list is answered while ffmpeg is still being asked, and says what
	// is true for now: this PC cannot convert the film yet.
	it := listed(t, e, c, film)
	var p convertedAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if _, known := e.s.abilities(); known {
		t.Fatal("ffmpeg answered although the test holds it, so this test proves nothing")
	}
	if it.Playable != "no" || !strings.Contains(strings.Join(it.Reasons, " "), "has not said yet what it can do") {
		t.Errorf("the list, while ffmpeg has not answered: %s %q", it.Playable, it.Reasons)
	}
	if p.Plan.Playable != "no" || !p.Plan.PCCannot || p.HLS != "" {
		t.Errorf("Play, while ffmpeg has not answered: %+v", p)
	}

	// Once it answers, the same film is converted.
	slow.set(t, "hold", false)
	converterAnswered(t, e)
	if it := listed(t, e, c, film); it.Playable != "yes" || it.Method != "convert" {
		t.Errorf("the list, once ffmpeg has answered: %s/%s %q", it.Playable, it.Method, it.Reasons)
	}
}

// When ffmpeg cannot be asked (it fails, as on a PC that is short of
// memory while it starts up), it is asked again a minute later and not
// before: not by every list in between, and not never.
func TestAConverterThatCouldNotBeAskedIsAskedAgainLater(t *testing.T) {
	real, ffprobe := realPrograms(t)
	broken := newHeldProgram(t, real)
	broken.set(t, "fail", true)
	e := newEnvWith(t, broken.Path, ffprobe)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	film := e.firstItem(c, "Old Film")
	cv := e.s.helpers.convert
	asking := func() bool {
		cv.mu.Lock()
		defer cv.mu.Unlock()
		return cv.asking != nil
	}
	until(t, "the first ask to fail", func() bool { return !asking() })
	one := broken.starts()
	if one == 0 {
		t.Fatal("ffmpeg was not asked at start")
	}
	for i := 0; i < 12; i++ {
		if it := listed(t, e, c, film); it.Playable != "no" || !strings.Contains(strings.Join(it.Reasons, " "), "could not be asked what it can do") {
			t.Fatalf("the list after a failed ask: %s %q", it.Playable, it.Reasons)
		}
	}
	if n := broken.starts(); n != one || asking() {
		t.Errorf("ffmpeg was started %d more times by 12 lists within the minute", n-one)
	}

	// A minute later (the test moves the mark, not the clock) the next
	// list has it asked again, without waiting for it.
	broken.set(t, "fail", false)
	cv.mu.Lock()
	cv.retryAt = time.Now()
	cv.mu.Unlock()
	listed(t, e, c, film)
	until(t, "ffmpeg to be asked again and to answer", func() bool {
		cv.mu.Lock()
		defer cv.mu.Unlock()
		return cv.asked
	})
	if it := listed(t, e, c, film); it.Playable != "yes" || it.Method != "convert" {
		t.Errorf("the list after ffmpeg answered at the second ask: %s/%s %q", it.Playable, it.Method, it.Reasons)
	}
}

// A look at a film on a disk that is slow to wake is not waited for
// without end: Play answers that the film could not be read in time, the
// look carries on, and the next Play finds what it found.
func TestPlayDoesNotWaitWithoutEndForALookAtTheFilm(t *testing.T) {
	e := convertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Old Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	asleep := newHeldProgram(t, e.s.ffprobe)
	asleep.set(t, "hold", true)
	e.s.ffprobe = asleep.Path
	usual := lookWait
	lookWait = 50 * time.Millisecond
	t.Cleanup(func() { lookWait = usual })

	var p convertedAnswer
	w := e.do("POST", "/api/items/"+film+"/play", remote, c, nil)
	decode(t, w, &p)
	if p.Plan.Playable != "no" || !p.Plan.PCCannot || p.HLS != "" || !strings.Contains(strings.Join(p.Plan.Reasons, " "), "could not read the film in time") {
		t.Fatalf("play while the look hangs: %s", w.Body)
	}
	// Pressing Play again joins the same look.
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if n := asleep.starts(); n != 1 {
		t.Errorf("ffprobe was started %d times for one look", n)
	}
	asleep.set(t, "hold", false)
	lookWait = usual
	p = convertedAnswer{}
	w = e.do("POST", "/api/items/"+film+"/play", remote, c, nil)
	decode(t, w, &p)
	if p.Plan.Method != "convert" || p.HLS == "" {
		t.Errorf("play once the disk is awake: %s", w.Body)
	}
}

// listed is what the list of films says about one of them.
func listed(t *testing.T, e *env, c *http.Cookie, id string) itemView {
	t.Helper()
	var movies struct{ Items []itemView }
	w := e.do("GET", "/api/movies", remote, c, nil)
	if w.Code != 200 {
		t.Fatalf("the list of films: %d %s", w.Code, w.Body)
	}
	decode(t, w, &movies)
	for _, it := range movies.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("film %s is not in the list", id)
	return itemView{}
}

// pretendConverter makes a server without ffmpeg answer as one on a PC
// whose converter repackages and converts pictures in normal colour.
// Nothing is run: these tests are about what is decided, and a film that
// gets as far as being opened is refused when its picture is looked at.
func pretendConverter(t *testing.T, e *env) {
	t.Helper()
	m, err := hls.New("ffmpeg-is-not-run-here", filepath.Join(e.s.dataDir, "cache", "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	e.s.hls, e.s.ffmpeg = m, "ffmpeg-is-not-run-here"
	cv := e.s.helpers.convert
	cv.mu.Lock()
	cv.asked, cv.can = true, hls.Abilities{X264: true, Basics: true}
	cv.mu.Unlock()
}

// oneCopy is a library with one film in one file, recorded as inspected
// with the given picture and stereo AAC sound. about is the rest of what
// the inspector noted about the picture, as column=value pairs.
func oneCopy(t *testing.T, container, pictureRow, about string) versionsEnv {
	t.Helper()
	v := twoVersions(t)
	if _, err := v.s.db.Exec(`DELETE FROM files WHERE id=?`, v.file["1080p"]); err != nil {
		t.Fatal(err)
	}
	v.inspected(t, "2160p", container, pictureRow, sound("aac", "eng", "", true, false))
	if about != "" {
		if _, err := v.s.db.Exec(`UPDATE streams SET `+about+` WHERE file_id=? AND kind='video'`, v.file["2160p"]); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

// A device's answer that it does not play 4K smoothly is advice: where
// this PC can make a smaller picture, it does. It is not a refusal. The
// fault: where this PC could not convert the film (it has no converter, or
// the film is HDR, which is not converted yet), a film that played on the
// device in the version before was refused.
func TestNotPlaying4KSmoothlyIsAdvice(t *testing.T) {
	notSmooth := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true, "hevc": true, "hevc10": true},
		"audio": map[string]bool{"aac": true}, "hls": true, "tall": map[string]bool{"h264": false, "hevc": false, "hevc10": false}, "pageFullscreen": true}
	const hdr = `color_transfer='smpte2084', codec_tag='hvc1'`
	for _, tc := range []struct {
		name            string
		converter       bool
		picture, about  string
		playable, how   string
		setting         int
		reasonMustHold  string
		reasonMustNotBe string
	}{
		{"a 4K HDR film, which this PC cannot convert", true, picture("hevc", 10, 3840, 2160), hdr, "yes", "direct", 0, "", ""},
		{"a 4K film on a PC with no converter", false, picture("h264", 8, 3840, 2160), "", "yes", "direct", 0, "", ""},
		{"a 4K HDR film on a PC with no converter", false, picture("hevc", 10, 3840, 2160), hdr, "yes", "direct", 0, "", ""},
		{"a 4K film where this PC can convert it", true, picture("h264", 8, 3840, 2160), "", "yes", "convert", 3, "does not say it plays smoothly", ""},
		// What the device cannot show at all stays refused, and it is the
		// PC that is named.
		{"a 4K HDR film in a format the device lacks", true, picture("vp9", 10, 3840, 2160), `color_transfer='smpte2084'`, "no", "none", 0, "cannot turn HDR into normal colour", "smoothly"},
	} {
		v := oneCopy(t, "mp4", tc.picture, tc.about)
		if tc.converter {
			pretendConverter(t, v.env)
		}
		v.do("POST", "/api/capabilities", remote, v.c, notSmooth)
		it := listed(t, v.env, v.c, v.film)
		if it.Playable != tc.playable || it.Method != tc.how {
			t.Errorf("%s: the list says %s/%s %q, want %s/%s", tc.name, it.Playable, it.Method, it.Reasons, tc.playable, tc.how)
		}
		if why := strings.Join(it.Reasons, " "); !strings.Contains(why, tc.reasonMustHold) || (tc.reasonMustNotBe != "" && strings.Contains(why, tc.reasonMustNotBe)) {
			t.Errorf("%s: the list gives the reasons %q", tc.name, it.Reasons)
		}
		if tc.how == "convert" {
			continue // nothing is there to convert with: the list is all this case can ask
		}
		var p convertedAnswer
		w := v.do("POST", "/api/items/"+v.film+"/play", remote, v.c, nil)
		decode(t, w, &p)
		if p.Plan.Playable != tc.playable || p.Plan.Method != tc.how || (tc.how == "direct" && !strings.HasPrefix(p.URL, "/stream/")) {
			t.Errorf("%s: Play says %s", tc.name, w.Body)
		}
	}
}

// An HDR film that a device cannot show as it is cannot be played yet:
// turning HDR into normal colour is not built, and a film converted
// without it would come out grey and dim. The lists and Play both say so,
// and name the PC.
func TestAnHDRFilmThatNeedsConvertingIsRefusedForNow(t *testing.T) {
	asked := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true, "hevc": true, "hevc10": true, "dovi5": false},
		"audio": map[string]bool{"aac": true}, "hls": true, "tall": map[string]bool{"h264": true, "hevc": true, "hevc10": true}, "pageFullscreen": true}
	for _, tc := range []struct {
		name, picture, about string
		caps                 map[string]any
		why                  string
	}{
		{"HDR (PQ) on a device without 10-bit HEVC", picture("hevc", 10, 1920, 1080), `color_transfer='smpte2084'`, chromeLike, "cannot turn HDR into normal colour"},
		{"HDR (HLG) on a device without 10-bit HEVC", picture("hevc", 10, 1920, 1080), `color_transfer='arib-std-b67'`, chromeLike, "cannot turn HDR into normal colour"},
		{"Dolby Vision profile 5 on a device that does not show it", picture("hevc", 10, 1920, 1080), `side_data='dovi:5', codec_tag='hvc1'`, asked, "cannot turn Dolby Vision of this kind into normal colour"},
	} {
		v := oneCopy(t, "mp4", tc.picture, tc.about)
		pretendConverter(t, v.env)
		v.do("POST", "/api/capabilities", remote, v.c, tc.caps)
		it := listed(t, v.env, v.c, v.film)
		var p convertedAnswer
		w := v.do("POST", "/api/items/"+v.film+"/play", remote, v.c, nil)
		decode(t, w, &p)
		if it.Playable != "no" || it.Method != "none" || !strings.Contains(strings.Join(it.Reasons, " "), tc.why) {
			t.Errorf("%s: the list says %s/%s %q", tc.name, it.Playable, it.Method, it.Reasons)
		}
		if p.Plan.Playable != "no" || p.Plan.Method != "none" || !p.Plan.PCCannot || p.HLS != "" || !strings.Contains(strings.Join(p.Plan.Reasons, " "), tc.why) {
			t.Errorf("%s: Play says %s", tc.name, w.Body)
		}
		if n := v.s.hls.Sessions(); n != 0 {
			t.Errorf("%s: %d conversions were opened", tc.name, n)
		}
	}
}

// Of two copies whose picture both has to be converted, a device is given
// the one that comes out better: the 1080p copy, converted at its full
// size, not the 4K copy, which is shown at 720p and is four times the work
// to read. The fault: copies that cost the same were ranked by the size of
// the file's own picture, which was right while that picture was what the
// device got.
func TestOfTwoCopiesToConvertTheBetterResultIsChosen(t *testing.T) {
	noHEVC := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true}, "hls": true, "tall": map[string]bool{"h264": true}, "pageFullscreen": true}
	phone := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true, "hevc": true},
		"audio": map[string]bool{"aac": true}, "hls": true, "tall": map[string]bool{"h264": false, "hevc": false}, "pageFullscreen": true}
	stereo := sound("aac", "eng", "", true, false)
	for _, tc := range []struct {
		name         string
		caps         map[string]any
		small, large []string // what the 1080p copy and the 2160p copy hold
		want, how    string
	}{
		{"neither picture plays on the device", noHEVC, []string{picture("hevc", 8, 1920, 1080), stereo}, []string{picture("hevc", 8, 3840, 2160), stereo}, "1080p", "convert"},
		{"a phone that does not play 4K smoothly, and a 1080p copy in an old format", phone, []string{picture("mpeg4", 8, 1920, 1080), stereo}, []string{picture("h264", 8, 3840, 2160), stereo}, "1080p", "convert"},
		// Cheapest first still holds.
		{"the 1080p copy plays as it is", phone, []string{picture("h264", 8, 1920, 1080), stereo}, []string{picture("h264", 8, 3840, 2160), stereo}, "1080p", "direct"},
		{"the 4K copy needs only its sound converted", noHEVC, []string{picture("mpeg4", 8, 1920, 1080), stereo}, []string{picture("h264", 8, 3840, 2160), sound("dts", "eng", "", true, false)}, "2160p", "audio"},
		{"both are converted to the same size: the copy with more in it", noHEVC, []string{picture("mpeg4", 8, 1280, 720), stereo}, []string{picture("mpeg4", 8, 1280, 720), stereo}, "2160p", "convert"},
	} {
		v := twoVersions(t)
		pretendConverter(t, v.env)
		v.inspected(t, "1080p", "matroska", tc.small...)
		v.inspected(t, "2160p", "matroska", tc.large...)
		v.do("POST", "/api/capabilities", remote, v.c, tc.caps)
		list, play := v.answers(t, v.c)
		if which(list.DurationMs) != tc.want || list.Playable != "yes" || list.Method != tc.how {
			t.Errorf("%s: the list judges the %s copy as %s/%s, want the %s copy, %s", tc.name, which(list.DurationMs), list.Playable, list.Method, tc.want, tc.how)
		}
		if which(play.DurationMs) != tc.want {
			t.Errorf("%s: Play opens the %s copy, want the %s copy", tc.name, which(play.DurationMs), tc.want)
		}
	}
}

// makeFilm makes a film in a format no browser plays (MPEG-4 Part 2 with
// AAC sound) at path.
func makeFilm(t *testing.T, path, size, rate string, seconds int) {
	t.Helper()
	length := strconv.Itoa(seconds)
	out, err := exec.Command(probe.FindFFmpeg(), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size="+size+":rate="+rate+":duration="+length, "-f", "lavfi", "-i", "sine=frequency=440:duration="+length,
		"-c:v", "mpeg4", "-q:v", "6", "-g", "100", "-c:a", "aac", path).CombinedOutput()
	if err != nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not make %s: %v %s", filepath.Base(path), err, out)
		}
		t.Skipf("this ffmpeg cannot make the test film: %v %s", err, out)
	}
}

// A clip filmed with the phone held upright is as much picture as the same
// clip lying down, and is converted as such. The fault: it was fitted by
// its height alone, so a 1080 by 1920 clip came out 608 by 1080 while the
// answer said nothing was lost.
func TestAnUprightClipIsNotShrunkToFitAFrameLyingDown(t *testing.T) {
	e := convertEnv(t)
	// Stored lying down and marked as turned, the way phones do it.
	lying := filepath.Join(t.TempDir(), "lying.mp4")
	makeFilm(t, lying, "640x360", "30", 8)
	out, err := exec.Command(probe.FindFFmpeg(), "-hide_banner", "-loglevel", "error", "-y", "-display_rotation", "90", "-i", lying, "-c", "copy",
		filepath.Join(e.media, "Upright Clip (2015).mp4")).CombinedOutput()
	if err != nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not mark the clip as turned: %v %s", err, out)
		}
		t.Skipf("this ffmpeg cannot mark a clip as turned: %v %s", err, out)
	}
	c := e.setup()
	e.s.InspectNow()
	clip := e.firstItem(c, "Upright Clip")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)

	// At the lightest setting the frame is 480 lines high lying down, and
	// 854 standing on end: the clip, 360 by 640 upright, fits as it is.
	var p convertedAnswer
	w := e.do("POST", "/api/items/"+clip+"/play", remote, c, map[string]any{"lower": 5})
	decode(t, w, &p)
	if p.Plan.Method != "convert" || p.Plan.Video.Rung != 5 || len(p.Plan.Lost) != 0 {
		t.Fatalf("play: %s", w.Body)
	}
	if got := described(t, streamOf(t, e, p.URL, "seg-0.m4s"), "codec_name,width,height"); len(got) != 2 || got[0] != "h264,360,640" {
		t.Errorf("the stream holds %q, want the clip upright at its own size, 360 by 640", got)
	}
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
}

// A conversion goes by the file as it is when Play is pressed, not by what
// the library last noted about it.
func TestAFilmIsConvertedAsTheFileIsNow(t *testing.T) {
	e := convertEnv(t)
	path := filepath.Join(e.media, "Changing Film (2003).mkv")
	makeFilm(t, path, "320x180", "25", 14)
	other := filepath.Join(t.TempDir(), "other.mkv")
	makeFilm(t, other, "160x90", "25", 8)
	c := e.setup()
	e.s.InspectNow()
	id := e.firstItem(c, "Changing Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	play := func() (convertedAnswer, int) {
		t.Helper()
		var p convertedAnswer
		w := e.do("POST", "/api/items/"+id+"/play", remote, c, nil)
		decode(t, w, &p)
		if p.Plan.Method != "convert" || p.HLS == "" {
			t.Fatalf("play: %s", w.Body)
		}
		list := e.do("GET", p.URL, remote, nil, nil)
		return p, strings.Count(list.Body.String(), "#EXTINF")
	}

	// A film whose length the library does not know is converted all the
	// same: its picture is looked at, and that tells where it ends. (Only
	// repackaging needs the library's figure.)
	if _, err := e.s.db.Exec(`UPDATE files SET duration_ms=0`); err != nil {
		t.Fatal(err)
	}
	p, chunks := play()
	if chunks != 5 {
		t.Errorf("14 seconds cut every 3 should be 5 chunks, got %d", chunks)
	}
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)

	// The file is replaced by another under the same name, and Play is
	// pressed before the library has been scanned again. The fault: the
	// new file was converted by what the look at the old one had found
	// (its length, its size, its rate), because what was kept was marked
	// with the size and date the library had noted, not the file's own.
	b, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	p, chunks = play()
	if chunks != 3 {
		t.Errorf("the film is now 8 seconds long, which is 3 chunks, and its list has %d", chunks)
	}
	if got := described(t, streamOf(t, e, p.URL, "seg-0.m4s"), "codec_name,width,height"); len(got) != 2 || got[0] != "h264,160,90" {
		t.Errorf("the stream holds %q, want the new file's picture, 160 by 90", got)
	}
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
}

// A film whose file is gone (a drive that was unplugged) is answered the
// way every other kind of play answers it: it is not available. The
// fault: the look at its picture came before the check that the file is
// there, so the answer was that the PC could not work out how to convert
// it, with a link to a file that does not exist.
func TestAMissingFilmIsNotLookedAt(t *testing.T) {
	e := convertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	c := e.setup()
	e.s.InspectNow()
	id := e.firstItem(c, "Old Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	if err := os.Remove(filepath.Join(e.media, "Old Film (1999).mkv")); err != nil {
		t.Fatal(err)
	}
	ffprobe := newCounted(t, `'`+e.s.ffprobe+`' "$@"`)
	e.s.ffprobe = ffprobe.Path
	w := e.do("POST", "/api/items/"+id+"/play", remote, c, nil)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "not available right now") {
		t.Errorf("play of a film whose file is gone: %d %s", w.Code, w.Body)
	}
	if n := ffprobe.runs(); n != 0 {
		t.Errorf("ffprobe was run %d times on a file that is not there", n)
	}
}

// lookAnswering stands a program in for ffprobe that answers a look at a
// film's picture with the two frame rates given (the average, and the one
// the frames' times are counted in), for a film 14 seconds long.
func lookAnswering(t *testing.T, average, base string) string {
	t.Helper()
	about := `{"packets":[{"pts_time":"0.000000","flags":"K_"}],"streams":[{"width":320,"height":180,"sample_aspect_ratio":"1:1","r_frame_rate":"` + base +
		`","avg_frame_rate":"` + average + `","start_time":"0.000000"}],"format":{"format_name":"matroska,webm","start_time":"0.000000","duration":"14.000000"}}`
	last := `{"packets":[{"pts_time":"13.900000","flags":"K_"}]}`
	return script(t, "case \"$*\" in\n*packet=pts_time,flags*) cat <<'XX'\n"+last+"\nXX\n;;\n*) cat <<'XX'\n"+about+"\nXX\n;;\nesac")
}

// What the answer says is lost and what the converter does come from the
// same number. The fault: the answer went by the frame rate the library had
// noted, and the converter by the one found when the picture is looked at,
// which is the truer of the two. Where they differed, a film could have
// every second picture left out without the answer saying so, or the
// other way round.
func TestWhatIsSaidToBeLostIsWhatTheConverterDoes(t *testing.T) {
	for _, tc := range []struct {
		name, library  string // the rate the library noted
		average, base  string // the rates the look finds
		rate           string // the rate the film arrives at, at the setting that halves fast films
		everySecondOut bool
	}{
		// The look settles on 60 a second for this one.
		{"a film that states no steady rate and counts in thousandths", "25", "0/0", "1000/1", "30/1", true},
		{"a film the library took for a fast one", "50", "25/1", "25/1", "25/1", false},
		{"a fast film", "50", "50/1", "50/1", "25/1", true},
	} {
		e := convertEnv(t)
		oldFilm(t, e, "Old Film (1999).mkv", tc.library)
		c := e.setup()
		e.s.InspectNow()
		id := e.firstItem(c, "Old Film")
		e.do("POST", "/api/capabilities", remote, c, chromeLike)
		e.s.ffprobe = lookAnswering(t, tc.average, tc.base)

		var p convertedAnswer
		w := e.do("POST", "/api/items/"+id+"/play", remote, c, map[string]any{"lower": 4})
		decode(t, w, &p)
		if p.Plan.Method != "convert" || p.Plan.Video.Rung != 4 || p.HLS == "" {
			t.Fatalf("%s: play: %s", tc.name, w.Body)
		}
		if said := strings.Contains(strings.Join(p.Plan.Lost, " "), "second picture"); said != tc.everySecondOut {
			t.Errorf("%s: the answer lists as lost %q", tc.name, p.Plan.Lost)
		}
		if got := described(t, streamOf(t, e, p.URL, "seg-0.m4s", "seg-1.m4s"), "codec_name,r_frame_rate"); len(got) != 2 || got[0] != "h264,"+tc.rate {
			t.Errorf("%s: the stream holds %q, want the picture at %s a second", tc.name, got, tc.rate)
		}
		e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
	}
}

// What the server is told and what it tells, in small things that nothing
// else holds.
func TestSmallPromisesOfThePlayAnswer(t *testing.T) {
	// A report that lists more formats than there are is refused, also in
	// the list of what plays above 1080p.
	e := newEnv(t)
	c := e.setup()
	tall := map[string]bool{}
	for i := 0; i < 41; i++ {
		tall[fmt.Sprintf("format%d", i)] = true
	}
	if w := e.do("POST", "/api/capabilities", remote, c, map[string]any{"containers": map[string]bool{"mp4": true}, "tall": tall}); w.Code != 400 {
		t.Errorf("a report with 41 formats above 1080p: %d %s", w.Code, w.Body)
	}
	// Whether a file has sound is left open until it has been inspected.
	var p convertedAnswer
	w := e.do("POST", "/api/items/"+e.firstItem(c, "clip")+"/play", remote, c, nil)
	decode(t, w, &p)
	if w.Code != 200 || p.Plan.Playable != "unknown" || p.Plan.HasAudio != nil {
		t.Errorf("play of a file that has not been inspected: %s", w.Body)
	}
}

// The reason says "picked for you" when the app chose the sound track from
// the viewer's settings, and "you chose" only when the viewer did, in this
// play.
func TestWhoChoseTheSoundIsSaidRight(t *testing.T) {
	e := convertEnv(t)
	c := e.setup()
	e.s.InspectNow()
	id := e.firstItem(c, "Test Film")
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	if w := e.do("PUT", "/api/prefs", remote, c, map[string]string{"audioLang": "no", "subLang": "", "subs": "off"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	for _, tc := range []struct {
		body       any
		says, nots string
	}{
		{nil, "picked for you", "You chose"},
		{map[string]any{"audio": 1}, "You chose", "picked for you"},
	} {
		var p convertedAnswer
		w := e.do("POST", "/api/items/"+id+"/play", remote, c, tc.body)
		decode(t, w, &p)
		if why := strings.Join(p.Plan.Reasons, " "); !strings.Contains(why, tc.says) || strings.Contains(why, tc.nots) {
			t.Errorf("play with %v gives the reasons %q", tc.body, p.Plan.Reasons)
		}
		e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
	}
}

// Only a few films are looked at at once, however many are started
// together: each look reads from the disk the films are on.
func TestOnlyAFewFilmsAreLookedAtAtOnce(t *testing.T) {
	e := convertEnv(t)
	oldFilm(t, e, "Old Film (1990).mkv", "25")
	b, err := os.ReadFile(filepath.Join(e.media, "Old Film (1990).mkv"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 6; i++ {
		os.WriteFile(filepath.Join(e.media, fmt.Sprintf("Old Film (%d).mkv", 1990+i)), b, 0o644)
	}
	c := e.setup()
	e.s.InspectNow()
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	var movies struct{ Items []itemView }
	decode(t, e.do("GET", "/api/movies", remote, c, nil), &movies)
	var films []string
	for _, it := range movies.Items {
		if it.Title == "Old Film" {
			films = append(films, it.ID)
		}
	}
	if len(films) != 6 {
		t.Fatalf("the library holds %d of the 6 films", len(films))
	}
	ffprobe := newCounted(t, `'`+e.s.ffprobe+`' "$@"`)
	e.s.ffprobe = ffprobe.Path
	for _, w := range together(len(films), func(i int) *httptest.ResponseRecorder {
		return e.do("POST", "/api/items/"+films[i]+"/play", remote, c, nil)
	}) {
		var p convertedAnswer
		decode(t, w, &p)
		if p.Plan.Method != "convert" || p.HLS == "" {
			t.Errorf("play: %s", w.Body)
		}
	}
	if most := ffprobe.atOnce(); most == 0 || most > 2 {
		t.Errorf("%d looks ran at the same time, and the most allowed is 2", most)
	}
}
