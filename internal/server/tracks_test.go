package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// trackEnv is a library with one film whose tracks the test decides, on a
// device that plays everything in it.
type trackEnv struct {
	*env
	c      *http.Cookie
	film   string
	fileID string
}

// filmWith makes one inspected MKV film. streams are rows for the streams
// table after file_id: idx, kind, codec, is_default, is_forced,
// is_commentary, is_hearing_impaired, language, title. beside are files
// put next to the film.
func filmWith(t *testing.T, streams []string, beside map[string]string) trackEnv {
	t.Helper()
	e := newEnv(t)
	dir := filepath.Join(e.media, "Films", "Track Film (2019)")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Track Film (2019).mkv"), []byte("x"), 0o644)
	for name, body := range beside {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	c := e.setup()
	film := e.firstItem(c, "Track Film")
	var fileID string
	e.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, film).Scan(&fileID)
	e.s.db.Exec(`UPDATE files SET probed=1, container='matroska', duration_ms=5400000 WHERE id=?`, fileID)
	for _, row := range streams {
		if _, err := e.s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,is_default,is_forced,is_commentary,is_hearing_impaired,language,title) VALUES(?,`+row+`)`, fileID); err != nil {
			t.Fatal(err)
		}
	}
	e.do("POST", "/api/capabilities", remote, c, map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true},
		"video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true, "ac3": true}, "hls": true})
	return trackEnv{e, c, film, fileID}
}

func (e trackEnv) play(t *testing.T) playAnswer {
	t.Helper()
	return e.playItem(t, e.film)
}

func (e trackEnv) playItem(t *testing.T, item string) playAnswer {
	t.Helper()
	var p playAnswer
	w := e.do("POST", "/api/items/"+item+"/play", remote, e.c, nil)
	if w.Code != 200 {
		t.Fatalf("play: %d %s", w.Code, w.Body)
	}
	decode(t, w, &p)
	return p
}

// pretendConverter makes the server behave as on a PC with ffmpeg, so that
// a sound track other than the first can be planned for. Nothing is
// converted unless chunks are fetched, which these tests do not do.
func (e trackEnv) pretendConverter(t *testing.T) {
	t.Helper()
	m, err := hls.New("ffmpeg-is-not-run-here", filepath.Join(e.s.dataDir, "cache", "chunks"))
	if err != nil {
		t.Fatal(err)
	}
	e.s.hls = m
}

// choose tells the server what the viewer picked, as the page does.
func (e trackEnv) choose(t *testing.T, item string, body map[string]any) {
	t.Helper()
	if w := e.do("PUT", "/api/items/"+item+"/choice", remote, e.c, body); w.Code != 200 {
		t.Fatalf("choice %v: %d %s", body, w.Code, w.Body)
	}
}

// selected is what a play answer starts with: the sound track's number and
// the subtitle's key ("" for none).
func selected(p playAnswer) (audio int, sub string) {
	audio = -1
	for _, a := range p.Audio {
		if a.Selected {
			audio = a.Index
		}
	}
	for _, s := range p.Subtitles {
		if s.Selected {
			sub = s.Key
		}
	}
	return audio, sub
}

const oneCue = "1\n00:00:01,000 --> 00:00:02,000\nHei\n"

// The library is looked over again every 15 minutes. A subtitle file's
// address handed out when the film started must still work afterwards: the
// page only fetches it when the viewer switches the subtitles on.
func TestASubtitleFilesAddressSurvivesTheNextScan(t *testing.T) {
	e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`, `1,'audio','aac',1,0,0,0,'eng',''`},
		map[string]string{"Track Film (2019).no.srt": oneCue, "Track Film (2019).en.srt": strings.Replace(oneCue, "Hei", "Hello", 1)})
	p := e.play(t)
	if len(p.Subtitles) != 2 {
		t.Fatalf("subtitles: %+v", p.Subtitles)
	}
	var no subtitleView
	for _, s := range p.Subtitles {
		if s.Language == "no" {
			no = s
		}
	}
	if w := e.do("GET", no.URL, remote, e.c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Hei") {
		t.Fatalf("before the scan: %d %q", w.Code, w.Body)
	}
	e.s.ScanAll()
	if w := e.do("GET", no.URL, remote, e.c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Hei") {
		t.Fatalf("after the scan the address handed out before it answers %d %q", w.Code, w.Body)
	}
	for _, s := range e.play(t).Subtitles {
		if s.Language == "no" && s.Key != no.Key {
			t.Errorf("the same subtitle file is now called %s, was %s", s.Key, no.Key)
		}
	}
	// A key that belongs to no file beside this film is still refused.
	if w := e.do("GET", "/api/files/"+e.fileID+"/subtitles/x"+strings.Repeat("0", 32), remote, e.c, nil); w.Code != 404 {
		t.Errorf("a made-up subtitle key answered %d", w.Code)
	}
}

// Checklist step 2.6: "play the same film again. It should resume where
// you stopped, with the same sound and subtitles."
func TestTheSameFilmComesBackWithTheSameSoundAndSubtitles(t *testing.T) {
	e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`,
		`1,'audio','ac3',1,0,0,0,'eng','Surround'`, `2,'audio','aac',0,0,0,0,'eng','Stereo'`, `3,'audio','aac',0,0,1,0,'eng','Commentary'`,
		`4,'subtitle','subrip',0,0,0,0,'eng',''`, `5,'subtitle','subrip',0,1,0,0,'eng','Forced'`, `6,'subtitle','subrip',0,0,0,1,'eng','SDH'`}, nil)
	e.pretendConverter(t)
	if a, s := selected(e.play(t)); a != 0 || s != "e1" {
		t.Fatalf("first play: sound %d, subtitle %q; want the main sound and the forced subtitles", a, s)
	}
	for _, c := range []struct {
		name  string
		audio int
		sub   string
	}{
		{"the second English sound track, forced subtitles left showing", 1, "e1"},
		{"the commentary", 2, "e1"},
		{"subtitles switched off", 1, ""},
		{"the subtitles with sounds described", 0, "e2"},
		{"the full subtitles", 1, "e0"},
	} {
		// The page sends the languages too, as it always has.
		e.choose(t, e.film, map[string]any{"audioLang": "en", "subLang": "en", "subsOn": c.sub != "", "audioIndex": c.audio, "subKey": c.sub})
		if a, s := selected(e.play(t)); a != c.audio || s != c.sub {
			t.Errorf("%s: picked sound %d and subtitle %q, the film came back with sound %d and subtitle %q", c.name, c.audio, c.sub, a, s)
		}
	}
	// A pick the film does not have is not stored as one; the languages
	// sent with it are used instead.
	e.choose(t, e.film, map[string]any{"audioLang": "en", "subLang": "en", "subsOn": true, "audioIndex": 7, "subKey": "e9"})
	if a, s := selected(e.play(t)); a != 0 || s != "e0" {
		t.Errorf("after a pick that does not exist: sound %d, subtitle %q", a, s)
	}
}

// A subtitle file with no language in its name ("Film.srt") or a whole
// word for one ("Film.English.srt") must be remembered like any other,
// from the page as it is now and from a page loaded before the update,
// which sends only languages.
func TestSubtitleFilesWithoutALanguageCodeAreRemembered(t *testing.T) {
	for name, file := range map[string]string{"no language": "Track Film (2019).srt", "a whole word": "Track Film (2019).English.srt"} {
		for _, page := range []string{"today's page", "an older page"} {
			e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`, `1,'audio','aac',1,0,0,0,'eng',''`}, map[string]string{file: oneCue})
			// The viewer has a language of their own, which the file lacks.
			e.do("PUT", "/api/prefs", remote, e.c, map[string]string{"audioLang": "no", "subLang": "", "subs": "auto"})
			p := e.play(t)
			if len(p.Subtitles) != 1 {
				t.Fatalf("%s: subtitles %+v", name, p.Subtitles)
			}
			if _, s := selected(p); s != "" {
				t.Fatalf("%s: a subtitle was on before anything was picked: %q", name, s)
			}
			body := map[string]any{"audioLang": p.Audio[0].Language, "subLang": p.Subtitles[0].Language, "subsOn": true}
			if page == "today's page" {
				body["audioIndex"], body["subKey"] = 0, p.Subtitles[0].Key
			}
			e.choose(t, e.film, body)
			if _, s := selected(e.play(t)); s != p.Subtitles[0].Key {
				t.Errorf("%s, %s: the subtitle file was switched on and is off again", name, page)
			}
		}
	}
}

// Checklist step 4.2: "Play the next episode. It should start with the
// same choice." And a choice made in one show is not another show's.
func TestAChoiceCarriesToTheNextEpisode(t *testing.T) {
	env, c := showEnv(t)
	e := trackEnv{env: env, c: c}
	e.pretendConverter(t)
	ep1, ep2, other := env.episode(c, "Good Show", 1, 1).ID, env.episode(c, "Good Show", 1, 2).ID, env.episode(c, "Other Show", 1, 1).ID
	for _, id := range []string{ep1, ep2, other} {
		var fileID string
		e.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, id).Scan(&fileID)
		e.s.db.Exec(`UPDATE files SET probed=1, container='matroska', duration_ms=1400000 WHERE id=?`, fileID)
		for _, row := range []string{`0,'video','h264',1,0,'',''`, `1,'audio','aac',1,0,'jpn',''`, `2,'audio','aac',0,0,'eng','5.1'`, `3,'audio','aac',0,0,'eng','Stereo'`,
			`4,'subtitle','subrip',0,0,'eng',''`, `5,'subtitle','subrip',0,1,'eng','Signs'`} {
			if _, err := e.s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,is_default,is_forced,language,title) VALUES(?,`+row+`)`, fileID); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.do("POST", "/api/capabilities", remote, c, map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true},
		"video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}, "hls": true})
	if a, s := selected(e.playItem(t, ep2)); a != 0 || s != "" {
		t.Fatalf("episode 2 before anything was picked: sound %d, subtitle %q", a, s)
	}
	// In episode 1 the viewer picks the English stereo sound and the
	// signs-only subtitles.
	e.choose(t, ep1, map[string]any{"audioLang": "en", "subLang": "en", "subsOn": true, "audioIndex": 2, "subKey": "e1"})
	if a, s := selected(e.playItem(t, ep2)); a != 2 || s != "e1" {
		t.Errorf("episode 2 after the pick in episode 1: sound %d, subtitle %q; want sound 2 and the signs-only subtitles", a, s)
	}
	if a, s := selected(e.playItem(t, other)); a != 0 || s != "" {
		t.Errorf("another show took over the choice: sound %d, subtitle %q", a, s)
	}
}

// A file found to be WebM plays in a browser that plays WebM and not MKV
// (Firefox, Safari), and is sent as WebM even when it is named .mkv.
func TestAWebMFilePlaysWhereMKVDoesNot(t *testing.T) {
	e := filmWith(t, []string{`0,'video','vp9',1,0,0,0,'',''`, `1,'audio','opus',1,0,0,0,'eng',''`}, nil)
	e.do("POST", "/api/capabilities", remote, e.c, map[string]any{"containers": map[string]bool{"mp4": true, "webm": true},
		"video": map[string]bool{"h264": true, "vp9": true}, "audio": map[string]bool{"aac": true, "opus": true}, "hls": true})
	play := func() (p struct {
		URL, MimeType string
		Plan          struct {
			Playable, Method string
			Reasons          []string
		}
	}) {
		decode(t, e.do("POST", "/api/items/"+e.film+"/play", remote, e.c, nil), &p)
		return p
	}
	// Stored as an earlier version stored every such file: as MKV.
	if p := play(); p.Plan.Playable != "no" || !strings.Contains(strings.Join(p.Plan.Reasons, " "), "MKV") {
		t.Fatalf("stored as MKV: %+v", p)
	}
	e.s.db.Exec(`UPDATE files SET container='webm' WHERE id=?`, e.fileID)
	p := play()
	if p.Plan.Playable != "yes" || p.Plan.Method != "direct" || p.MimeType != "video/webm" {
		t.Fatalf("stored as WebM: %+v", p)
	}
	if w := e.do("GET", p.URL, remote, nil, nil); w.Code != 200 || w.Header().Get("Content-Type") != "video/webm" {
		t.Errorf("the file itself: %d, sent as %q", w.Code, w.Header().Get("Content-Type"))
	}
}

// needFFmpeg returns the converter, or skips the test where there is none
// (unless LM_REQUIRE_FFMPEG says there must be).
func needFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg := probe.FindFFmpeg()
	if ffmpeg == "" {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg was not found, and LM_REQUIRE_FFMPEG says it must be")
		}
		t.Skip("ffmpeg is not installed here")
	}
	return ffmpeg
}

const styledFile = `[Script Info]
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,2,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello from a styled file
`

// A styled subtitle file beside a film is converted by ffmpeg and the
// result kept. After a scan the same kept copy must be found again, not a
// second one made beside it.
func TestAConvertedSubtitleFileIsKeptOnceAcrossScans(t *testing.T) {
	ffmpeg := needFFmpeg(t)
	e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`, `1,'audio','aac',1,0,0,0,'eng',''`},
		map[string]string{"Track Film (2019).en.ass": styledFile})
	e.s.ffmpeg = ffmpeg
	kept := func() []string {
		m, _ := filepath.Glob(filepath.Join(e.s.dataDir, "cache", "subtitles", "*.vtt"))
		return m
	}
	for round := 0; round < 3; round++ {
		p := e.play(t)
		if len(p.Subtitles) != 1 {
			t.Fatalf("subtitles: %+v", p.Subtitles)
		}
		if w := e.do("GET", p.Subtitles[0].URL, remote, e.c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Hello from a styled file") {
			t.Fatalf("round %d: %d %q", round, w.Code, w.Body)
		}
		if got := kept(); len(got) != 1 {
			t.Fatalf("round %d: %d kept copies of one subtitle file, want 1: %v", round, len(got), got)
		}
		e.s.ScanAll()
	}
}
