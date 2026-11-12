package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// Five captions as a subtitle file saved long ago in Western Europe holds
// them, with one byte for every letter (Windows-1252), and as they read.
// The first and the last have no letter that differs between that encoding
// and today's.
var (
	oldCaptions  = []string{"Plain first line", "Jeg liker bl\xe5b\xe6r", "\xc5se sa \xe7a va", "\x93Quoted\x94 and it\x92s fine", "Plain last line"}
	readCaptions = []string{"Plain first line", "Jeg liker blåbær", "Åse sa ça va", "“Quoted” and it’s fine", "Plain last line"}
)

// styled writes captions as a styled subtitle file (.ass), one every two
// seconds.
func styled(captions []string) string {
	b := strings.Replace(styledFile, "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello from a styled file\n", "", 1)
	for i, c := range captions {
		b += fmt.Sprintf("Dialogue: 0,0:00:%02d.00,0:00:%02d.00,Default,,0,0,0,,%s\n", 2*i+1, 2*i+2, c)
	}
	return strings.ReplaceAll(b, "\n", "\r\n")
}

// styledOlder writes them in the older form of the same format (.ssa).
func styledOlder(captions []string) string {
	b := `[Script Info]
ScriptType: v4.00

[V4 Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, TertiaryColour, BackColour, Bold, Italic, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, AlphaLevel, Encoding
Style: Default,Arial,20,16777215,65535,65535,0,0,0,1,2,2,2,10,10,10,0,1

[Events]
Format: Marked, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
`
	for i, c := range captions {
		b += fmt.Sprintf("Dialogue: Marked=0,0:00:%02d.00,0:00:%02d.00,Default,,0,0,0,,%s\n", 2*i+1, 2*i+2, c)
	}
	return strings.ReplaceAll(b, "\n", "\r\n")
}

// byFrames writes them in the format that counts in frames of the film
// (MicroDVD, .sub).
func byFrames(captions []string) string {
	b := ""
	for i, c := range captions {
		b += fmt.Sprintf("{%d}{%d}%s\r\n", 24*(2*i+1), 24*(2*i+2), c)
	}
	return b
}

// numbered writes them in the commonest format (SubRip, .srt).
func numbered(captions []string) string {
	b := ""
	for i, c := range captions {
		b += fmt.Sprintf("%d\r\n00:00:%02d,000 --> 00:00:%02d,000\r\n%s\r\n\r\n", i+1, 2*i+1, 2*i+2, c)
	}
	return b
}

// twoBytes saves text the way Windows Notepad once did by default (UTF-16).
func twoBytes(text string) string {
	b := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(text)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return string(b)
}

// keptSubtitles lists the subtitles the server has kept for next time.
func keptSubtitles(e *env) []string {
	m, _ := filepath.Glob(filepath.Join(e.s.dataDir, "cache", "subtitles", "*.vtt"))
	return m
}

// lacking lists the captions that text does not hold.
func lacking(text string, captions []string) (missing []string) {
	for _, c := range captions {
		if !strings.Contains(text, c) {
			missing = append(missing, c)
		}
	}
	return missing
}

// A styled subtitle file beside a film goes through ffmpeg, which throws
// away every line that is not in today's encoding and still says all went
// well. A file saved in the old Western European encoding lost every line
// with an accented letter that way, and the cut-down result was kept for
// good. Every line has to arrive, the first time and from the kept copy,
// and files that are in today's encodings must come through unharmed.
func TestSubtitleFilesInAnOldEncodingKeepEveryLine(t *testing.T) {
	ffmpeg := needFFmpeg(t)
	e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`, `1,'audio','aac',1,0,0,0,'eng',''`}, map[string]string{
		"Track Film (2019).no.ass": styled(oldCaptions),
		"Track Film (2019).sv.ssa": styledOlder(oldCaptions),
		"Track Film (2019).da.sub": byFrames(oldCaptions),
		"Track Film (2019).en.ass": styled(readCaptions),           // in today's encoding already
		"Track Film (2019).de.ass": twoBytes(styled(readCaptions)), // two bytes a letter
	})
	e.s.ffmpeg = ffmpeg
	address := map[string]string{}
	for _, s := range e.play(t).Subtitles {
		address[s.Language] = s.URL
	}
	// A .sub file is not offered on the page, but its address answers.
	var bySub string
	e.s.db.QueryRow(`SELECT rel_path FROM sidecars WHERE format='sub'`).Scan(&bySub)
	if bySub == "" {
		t.Fatal("the .sub file beside the film was not found by the scan")
	}
	address["da"] = "/api/files/" + e.fileID + "/subtitles/" + sidecarKey(bySub)
	for _, lang := range []string{"no", "sv", "da", "en", "de"} {
		if address[lang] == "" {
			t.Fatalf("no address for the %q subtitle file: %v", lang, address)
		}
		for _, when := range []string{"as it is made", "from the kept copy"} {
			w := e.do("GET", address[lang], remote, e.c, nil)
			if missing := lacking(w.Body.String(), readCaptions); w.Code != 200 || len(missing) > 0 {
				t.Errorf("the %q file, %s: answered %d without %q:\n%s", lang, when, w.Code, missing, w.Body)
			}
		}
	}
	if kept := keptSubtitles(e.env); len(kept) != 5 {
		t.Errorf("%d subtitles were kept for next time, want 5: %v", len(kept), kept)
	}
}

// filmWithCaptionsInside adds to a convert environment's library a film
// whose subtitle track holds the captions byte for byte as given, which is
// how a film made from an old subtitle file holds them.
func filmWithCaptionsInside(t *testing.T, e *env, name string, captions []string) {
	t.Helper()
	srt := filepath.Join(t.TempDir(), "in.srt")
	os.WriteFile(srt, []byte(numbered(captions)), 0o644)
	out, err := exec.Command(probe.FindFFmpeg(), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=12:duration=12", "-i", srt, "-map", "0:v", "-map", "1:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:s", "copy", "-metadata:s:s:0", "language=nor",
		filepath.Join(e.media, name)).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg could not make %s: %v %s", name, err, out)
	}
}

// The same loss happened to subtitles stored inside a film in the old
// encoding (films made from old subtitle files hold them so).
func TestSubtitlesInsideAFilmInAnOldEncodingKeepEveryLine(t *testing.T) {
	e := newConvertEnv(t)
	filmWithCaptionsInside(t, e, "Old Letters (2001).mkv", oldCaptions)
	c := e.setup()
	e.s.InspectNow()
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+e.firstItem(c, "Old Letters")+"/play", remote, c, nil), &p)
	if len(p.Subtitles) != 1 || p.Subtitles[0].Key != "e0" {
		t.Fatalf("subtitles: %+v", p.Subtitles)
	}
	for _, when := range []string{"as it is made", "from the kept copy"} {
		w := e.do("GET", p.Subtitles[0].URL, remote, c, nil)
		if missing := lacking(w.Body.String(), readCaptions); w.Code != 200 || len(missing) > 0 {
			t.Errorf("%s: answered %d without %q:\n%s", when, w.Code, missing, w.Body)
		}
	}
}

// A film that is cut short (a copy that was interrupted, a damaged disk)
// gives up only the first part of its subtitles. ffmpeg says so and still
// reports success. The viewer gets what there is, but that is not the
// film's subtitles and must not be kept as if it were.
func TestASubtitleThatLostLinesIsNotKept(t *testing.T) {
	e := newConvertEnv(t)
	var nine []string
	for i := 1; i <= 9; i++ {
		nine = append(nine, fmt.Sprintf("Caption number %d", i))
	}
	srt := filepath.Join(t.TempDir(), "in.srt")
	os.WriteFile(srt, []byte(numbered(nine)), 0o644)
	whole := filepath.Join(t.TempDir(), "whole.mkv")
	out, err := exec.Command(probe.FindFFmpeg(), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=20", "-f", "lavfi", "-i", "sine=frequency=440:duration=20", "-i", srt,
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-c:s", "srt",
		whole).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg could not make the film: %v %s", err, out)
	}
	all, err := os.ReadFile(whole)
	if err != nil {
		t.Fatal(err)
	}
	// The library is in a folder with a name that must stay out of the log.
	lib := filepath.Join(t.TempDir(), "Household Videos")
	os.MkdirAll(lib, 0o755)
	e.media = lib
	os.WriteFile(filepath.Join(lib, "Cut Short (2015).mkv"), all[:len(all)*55/100], 0o644)
	c := e.setup()
	e.s.InspectNow()
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+e.firstItem(c, "Cut Short")+"/play", remote, c, nil), &p)
	if len(p.Subtitles) != 1 {
		t.Fatalf("subtitles: %+v", p.Subtitles)
	}
	for round := 1; round <= 2; round++ {
		w := e.do("GET", p.Subtitles[0].URL, remote, c, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Caption number 1") || strings.Contains(w.Body.String(), "Caption number 9") {
			t.Fatalf("round %d: the cut-short film's subtitles answered %d %q; want its first captions and not its last", round, w.Code, w.Body)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("round %d: a subtitle that lost lines was sent with Cache-Control %q, so a browser may keep it", round, cc)
		}
		if kept := keptSubtitles(e); len(kept) != 0 {
			t.Fatalf("round %d: a subtitle that lost lines was kept for next time: %v", round, kept)
		}
	}
	// The log says which film it was, and not where it is.
	onDisk, _ := os.ReadFile(filepath.Join(e.s.dataDir, "logs", "localmedia.log"))
	if !strings.Contains(string(onDisk), "Cut Short (2015).mkv") || !strings.Contains(string(onDisk), "not kept") {
		t.Errorf("the log does not say that the subtitles of Cut Short (2015).mkv were not kept:\n%s", onDisk)
	}
	for _, bad := range []string{lib, "Household Videos", e.s.dataDir} {
		if strings.Contains(string(onDisk), bad) {
			t.Errorf("the log contains %q:\n%s", bad, onDisk)
		}
	}
}

// Text that comes from ffmpeg unchecked is put right a line at a time, so
// that one subtitle may hold lines in the old encoding and in today's.
func TestEachLineIsReadInItsOwnEncoding(t *testing.T) {
	in := "Plain\nbl\xe5b\xe6r\nblåbær\n\nthe last line has no end \x93x\x94"
	want := "Plain\nblåbær\nblåbær\n\nthe last line has no end “x”"
	if got := string(readable([]byte(in))); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := string(readable([]byte(want))); got != want {
		t.Errorf("text in today's encoding was changed: %q", got)
	}
}

// When ffmpeg gives up on a subtitle it has usually written the first line
// of the answer already ("WEBVTT"). That used to go out as an answer that
// succeeded, with nothing in it, which a browser was allowed to keep for an
// hour. Here every caption of the film is in the old encoding, which made
// ffmpeg give up in just that way (it no longer does: the captions are
// rescued now). The answer must be the captions or an error; never an
// empty success.
func TestASubtitleFFmpegGivesUpOnIsNeverAnEmptySuccess(t *testing.T) {
	e := newConvertEnv(t)
	filmWithCaptionsInside(t, e, "All Accents (2002).mkv", oldCaptions[1:4])
	filmWithCaptionsInside(t, e, "Spoiled Later (2003).mkv", readCaptions)
	c := e.setup()
	e.s.InspectNow()
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+e.firstItem(c, "All Accents")+"/play", remote, c, nil), &p)
	if len(p.Subtitles) != 1 {
		t.Fatalf("subtitles: %+v", p.Subtitles)
	}
	w := e.do("GET", p.Subtitles[0].URL, remote, c, nil)
	cc := w.Header().Get("Cache-Control")
	switch {
	case w.Code == 200:
		if missing := lacking(w.Body.String(), readCaptions[1:4]); len(missing) > 0 {
			t.Errorf("answered 200 %q with Cache-Control %q: a success without %q", w.Body, cc, missing)
		}
	case cc != "no-store" || !strings.Contains(w.Body.String(), `"error"`):
		t.Errorf("answered %d %q with Cache-Control %q; an error must say so and must not be kept", w.Code, w.Body, cc)
	}

	// A film that has become unreadable since it was inspected: ffmpeg
	// fails on it outright, and the answer says so.
	p = playAnswer{}
	decode(t, e.do("POST", "/api/items/"+e.firstItem(c, "Spoiled Later")+"/play", remote, c, nil), &p)
	if len(p.Subtitles) != 1 {
		t.Fatalf("subtitles: %+v", p.Subtitles)
	}
	os.WriteFile(filepath.Join(e.media, "Spoiled Later (2003).mkv"), []byte("this is not a film any more"), 0o644)
	w = e.do("GET", p.Subtitles[0].URL, remote, c, nil)
	if w.Code != 500 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("a film ffmpeg cannot read answered %d %q with Cache-Control %q; want an error that is not kept", w.Code, w.Body, w.Header().Get("Cache-Control"))
	}
	onDisk, _ := os.ReadFile(filepath.Join(e.s.dataDir, "logs", "localmedia.log"))
	if !strings.Contains(string(onDisk), "the subtitles of Spoiled Later (2003).mkv could not be prepared") || strings.Contains(string(onDisk), e.media) {
		t.Errorf("the log should say which film's subtitles failed, without its folder (%s):\n%s", e.media, onDisk)
	}
}

// What remains when ffmpeg does fail, shown with a stand-in for it that
// fails in each of the ways it can.
func TestASubtitleThatCannotBeMadeIsAnErrorThatIsNotKept(t *testing.T) {
	e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`, `1,'audio','aac',1,0,0,0,'eng',''`,
		`2,'subtitle','subrip',0,0,0,0,'eng',''`, `3,'subtitle','subrip',0,0,0,0,'nor',''`, `4,'subtitle','subrip',0,0,0,0,'dan',''`, `5,'subtitle','subrip',0,0,0,0,'swe',''`}, nil)
	subs := e.play(t).Subtitles
	if len(subs) != 4 {
		t.Fatalf("subtitles: %+v", subs)
	}
	isError := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 500 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("%s: answered %d %q with Cache-Control %q; want an error that is not kept", what, w.Code, w.Body, w.Header().Get("Cache-Control"))
		}
		if kept := keptSubtitles(e.env); len(kept) != 0 {
			t.Errorf("%s: something was kept for next time: %v", what, kept)
		}
	}

	// It stops with an error before the first caption.
	e.s.ffmpeg = script(t, `printf 'WEBVTT\n\n'; echo 'Error decoding subtitles' >&2; exit 69`)
	isError("ffmpeg gave up before the first caption", e.do("GET", subs[0].URL, remote, e.c, nil))
	// The same with the captions asked to be moved later (see laterVTT).
	isError("ffmpeg gave up before the first caption, captions moved", e.do("GET", subs[0].URL+"?shift=4000", remote, e.c, nil))

	// It reports success and has written nothing at all.
	e.s.ffmpeg = script(t, `exit 0`)
	isError("ffmpeg wrote nothing", e.do("GET", subs[1].URL, remote, e.c, nil))

	// It dies after some captions have been sent on. The answer began as a
	// success and cannot be taken back, so the connection is cut: a browser
	// then knows the subtitle did not arrive whole, and keeps nothing.
	e.s.ffmpeg = script(t, `printf 'WEBVTT\n\n00:01.000 --> 00:02.000\nThe only caption that got out\n\n'; sleep 0.3; exit 1`)
	srv := httptest.NewServer(e.s)
	defer srv.Close()
	r, _ := http.NewRequest("GET", srv.URL+subs[2].URL, nil)
	r.AddCookie(e.c)
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatalf("asking for a subtitle that fails partway: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil {
		t.Errorf("a subtitle that failed partway ended like a whole one: %d %q", resp.StatusCode, body)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("a subtitle still being made was sent with Cache-Control %q, so a browser may keep it if it fails", cc)
	}
	if !strings.Contains(string(body), "The only caption that got out") {
		t.Errorf("what ffmpeg had made before it died was not sent on: %q", body)
	}
	if kept := keptSubtitles(e.env); len(kept) != 0 {
		t.Errorf("a subtitle that failed partway was kept for next time: %v", kept)
	}

	// And when all goes well: the answer being made is not for keeping (it
	// might have failed), the kept copy sent next time is.
	e.s.ffmpeg = script(t, `printf 'WEBVTT\n\n00:01.000 --> 00:02.000\nAll of it\n\n'`)
	for i, want := range []string{"no-store", "private, max-age=3600"} {
		w := e.do("GET", subs[3].URL, remote, e.c, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "All of it") || w.Header().Get("Cache-Control") != want {
			t.Errorf("a good subtitle, request %d: %d %q with Cache-Control %q, want %q", i+1, w.Code, w.Body, w.Header().Get("Cache-Control"), want)
		}
	}
	if kept := keptSubtitles(e.env); len(kept) != 1 {
		t.Errorf("after a good run %d subtitles are kept, want 1: %v", len(kept), kept)
	}
}
