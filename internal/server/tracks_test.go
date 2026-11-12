package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	var p playAnswer
	w := e.do("POST", "/api/items/"+e.film+"/play", remote, e.c, nil)
	if w.Code != 200 {
		t.Fatalf("play: %d %s", w.Code, w.Body)
	}
	decode(t, w, &p)
	return p
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
