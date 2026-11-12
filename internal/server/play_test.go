package server

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// newConvertEnv is a server with the real ffmpeg and one small film made for
// the test: H.264 picture, two sound tracks (English Dolby, marked as the
// main one, and Norwegian AAC), English subtitles inside the file and
// Norwegian ones in a file beside it.
func newConvertEnv(t *testing.T) *env {
	t.Helper()
	ffmpeg, ffprobe := probe.FindFFmpeg(), probe.Find()
	if ffmpeg == "" || ffprobe == "" {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg and ffprobe were not found, and LM_REQUIRE_FFMPEG says they must be")
		}
		t.Skip("ffmpeg and ffprobe are not installed here")
	}
	data, media := t.TempDir(), t.TempDir()
	srt := filepath.Join(t.TempDir(), "in.srt")
	os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:03,000\nHello there\n\n2\n00:00:07,500 --> 00:00:09,000\nGoodbye\n"), 0o644)
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=12", "-f", "lavfi", "-i", "sine=frequency=440:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=880:duration=12", "-i", srt,
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-pix_fmt", "yuv420p", "-c:a:0", "ac3", "-c:a:1", "aac", "-c:s", "srt",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=nor", "-metadata:s:s:0", "language=eng",
		"-disposition:a:0", "default", "-disposition:a:1", "0", filepath.Join(media, "Test Film (2020).mkv")).CombinedOutput()
	if err != nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not make the test film: %v %s", err, out)
		}
		t.Skipf("this ffmpeg cannot make the test film: %v %s", err, out)
	}
	os.WriteFile(filepath.Join(media, "Test Film (2020).no.srt"), []byte("\xEF\xBB\xBF1\r\n00:00:01,000 --> 00:00:03,000\r\nHei p\xC3\xA5 deg\r\n"), 0o644)
	d, err := db.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}}
	s, err := New(Config{DB: d, DataDir: data, Version: "test", Port: 8177, Assets: assets, FFprobe: ffprobe, FFmpeg: ffmpeg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &env{t: t, s: s, media: media}
}

type playAnswer struct {
	URL  string
	HLS  string
	Plan struct {
		Playable, Method string
		AudioTrack       int
		AudioCopy        bool
		Reasons          []string
	}
	Explain   string
	Audio     []audioView
	Subtitles []subtitleView
}

func TestConvertingPlaysWhatTheBrowserCannot(t *testing.T) {
	e := newConvertEnv(t)
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Test Film")
	// A browser like Chrome: opens MKV, no Dolby, can play repackaged video.
	chrome := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true}, "hls": true}
	if w := e.do("POST", "/api/capabilities", remote, c, chrome); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var list struct{ Items []itemView }
	decode(t, e.do("GET", "/api/movies", remote, c, nil), &list)
	if len(list.Items) != 1 || list.Items[0].Playable != "yes" || list.Items[0].Method != "audio" {
		t.Fatalf("the list should say this film plays, with its sound converted: %+v", list.Items)
	}

	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.Method != "audio" || p.Plan.AudioCopy || !strings.HasPrefix(p.URL, "/hls/") || p.HLS == "" || !strings.Contains(p.Explain, "converting its sound") {
		t.Fatalf("play: %+v", p)
	}
	if len(p.Audio) != 2 || !p.Audio[0].Selected || !strings.Contains(p.Audio[0].Label, "English") || !strings.Contains(p.Audio[1].Label, "Norwegian") {
		t.Fatalf("sound tracks: %+v", p.Audio)
	}
	if len(p.Subtitles) != 2 || p.Subtitles[0].Language != "en" || p.Subtitles[1].Language != "no" || p.Subtitles[0].Selected || p.Subtitles[1].Selected {
		t.Fatalf("subtitles: %+v", p.Subtitles)
	}
	// The chunks are fetched without a cookie, as a phone's player would.
	base := strings.TrimSuffix(p.URL, "index.m3u8")
	if w := e.do("GET", p.URL, remote, nil, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "#EXTINF") || !strings.Contains(w.Body.String(), "seg-1.m4s") {
		t.Fatalf("chunk list: %d %s", w.Code, w.Body)
	}
	var stream bytes.Buffer
	for _, name := range []string{"init.mp4", "seg-0.m4s", "seg-1.m4s"} {
		w := e.do("GET", base+name, remote, nil, nil)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		stream.Write(w.Body.Bytes())
	}
	joined := filepath.Join(t.TempDir(), "joined.mp4")
	os.WriteFile(joined, stream.Bytes(), 0o600)
	codecs, _ := exec.Command(probe.Find(), "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", joined).Output()
	if got := strings.Fields(string(codecs)); len(got) != 2 || got[0] != "h264" || got[1] != "aac" {
		t.Fatalf("the stream holds %q, want the untouched picture and AAC sound", got)
	}
	if w := e.do("GET", base+"seg-99.m4s", remote, nil, nil); w.Code != 404 {
		t.Errorf("a chunk past the end answered %d", w.Code)
	}

	// Picking the other sound track: it is AAC already, so nothing is converted.
	var p2 playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, map[string]int{"audio": 1}), &p2)
	if p2.Plan.Method != "remux" || !p2.Plan.AudioCopy || p2.Plan.AudioTrack != 1 || !p2.Audio[1].Selected {
		t.Fatalf("after choosing the Norwegian sound: %+v", p2)
	}
	// Closing the player stops that conversion at once; only its owner may.
	other := sessionCookie(e.do("POST", "/api/login", "192.168.1.60:1", nil, map[string]string{"name": "owner", "password": "a good password"}))
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, other, nil)
	if w := e.do("GET", p.URL, remote, nil, nil); w.Code != 200 {
		t.Fatal("another sign-in stopped this one's film")
	}
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
	if w := e.do("GET", p.URL, remote, nil, nil); w.Code != 403 {
		t.Fatalf("after closing the player its chunk list answered %d", w.Code)
	}

	// Subtitles arrive as WebVTT: the track inside the file through ffmpeg
	// (and from the kept copy the second time), the file beside it directly.
	for i := 0; i < 2; i++ {
		w := e.do("GET", p.Subtitles[0].URL, remote, c, nil)
		if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "WEBVTT") || !strings.Contains(w.Body.String(), "Hello there") || !strings.Contains(w.Body.String(), "00:07.5") {
			t.Fatalf("subtitle from inside the file, try %d: %d %q", i, w.Code, w.Body)
		}
	}
	if w := e.do("GET", p.Subtitles[1].URL, remote, c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "00:00:01.000 --> 00:00:03.000\nHei på deg") {
		t.Fatalf("subtitle file beside the film: %d %q", w.Code, w.Body)
	}
	if w := e.do("GET", p.Subtitles[0].URL, remote, nil, nil); w.Code != 401 {
		t.Errorf("a subtitle without sign-in answered %d", w.Code)
	}

	// The choice is remembered: next time the film starts the same way.
	if w := e.do("PUT", "/api/items/"+film+"/choice", remote, c, map[string]any{"audioLang": "nor", "subLang": "en", "subsOn": true}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var p3 playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p3)
	if !p3.Audio[1].Selected || !p3.Subtitles[0].Selected || p3.Plan.AudioTrack != 1 {
		t.Fatalf("the remembered choice was not used: %+v", p3)
	}

	// A device that plays Dolby but not MKV (an iPhone): repackaged, nothing converted.
	iphone := map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true, "ac3": true}, "hls": true}
	e.do("POST", "/api/capabilities", remote, other, iphone)
	var p4 playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, other, map[string]int{"audio": 0}), &p4)
	if p4.Plan.Method != "remux" || !p4.Plan.AudioCopy || !strings.Contains(p4.Explain, "nothing is lost") {
		t.Fatalf("on a device that plays Dolby: %+v", p4)
	}
	if w := e.do("GET", strings.TrimSuffix(p4.URL, "index.m3u8")+"seg-0.m4s", remote, nil, nil); w.Code != 200 {
		t.Fatalf("its first chunk: %d", w.Code)
	}

	var diag struct{ Converter bool }
	decode(t, e.do("GET", "/api/diagnostics", remote, c, nil), &diag)
	if !diag.Converter {
		t.Errorf("diagnostics: %+v", diag)
	}

	// Signing out stops what was being converted for that sign-in and
	// kills its links.
	e.do("POST", "/api/logout", remote, other, nil)
	if w := e.do("GET", p4.URL, remote, nil, nil); w.Code != 403 {
		t.Fatalf("after signing out the chunk list answered %d", w.Code)
	}
	e.do("POST", "/api/logout", remote, c, nil)
	if n := e.s.hls.Running(); n != 0 {
		t.Fatalf("%d converters still running after everyone signed out", n)
	}
}

func TestWithoutAConverterNothingPretendsToConvert(t *testing.T) {
	e := newEnv(t) // no ffmpeg here
	c := e.setup()
	film := e.firstItem(c, "Some Movie")
	var fileID string
	e.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, film).Scan(&fileID)
	e.s.db.Exec(`UPDATE files SET probed=1, container='matroska', duration_ms=5400000 WHERE id=?`, fileID)
	e.s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,bit_depth,is_default,language) VALUES(?,0,'video','h264',8,1,''),(?,1,'audio','aac',0,1,'eng'),(?,2,'audio','aac',0,0,'nor')`, fileID, fileID, fileID)
	e.do("POST", "/api/capabilities", remote, c, map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true},
		"video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}, "hls": true})
	// The viewer prefers Norwegian sound, which would take repackaging.
	if w := e.do("PUT", "/api/prefs", remote, c, map[string]string{"audioLang": "no", "subLang": "", "subs": "auto"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.Playable != "yes" || p.Plan.Method != "direct" || !strings.HasPrefix(p.URL, "/stream/") || !p.Audio[0].Selected {
		t.Fatalf("without a converter the film should still play as it is: %+v", p)
	}
	for _, bad := range []map[string]string{{"audioLang": "english!", "subs": "auto"}, {"audioLang": "en", "subs": "sometimes"}} {
		if w := e.do("PUT", "/api/prefs", remote, c, bad); w.Code != 400 {
			t.Errorf("preferences %v answered %d", bad, w.Code)
		}
	}
	var prefs struct{ AudioLang, Subs string }
	decode(t, e.do("GET", "/api/prefs", remote, c, nil), &prefs)
	if prefs.AudioLang != "no" || prefs.Subs != "auto" {
		t.Errorf("preferences read back: %+v", prefs)
	}
	var diag struct{ Converter bool }
	decode(t, e.do("GET", "/api/diagnostics", remote, c, nil), &diag)
	if diag.Converter {
		t.Error("diagnostics claim a converter that is not there")
	}
	var _ http.Handler = e.s
}

func TestSubtitleFilesBecomeWebVTT(t *testing.T) {
	srt := "\xEF\xBB\xBF1\r\n00:00:01,000 --> 00:00:02,500\r\n<font color=\"#fff\">Hello</font> <i>there</i>\r\nsecond line\r\n\r\n2\r\n0:01:02,5 --> 0:01:04,25\r\n{\\an8}Up top --> here\r\n\r\n3\r\n00:02:00,000 --> 00:02:01,000\r\n42\r\n"
	got := string(srtToVTT([]byte(srt)))
	want := "WEBVTT\n\n00:00:01.000 --> 00:00:02.500\nHello <i>there</i>\nsecond line\n\n00:01:02.500 --> 00:01:04.250\nUp top -> here\n\n00:02:00.000 --> 00:02:01.000\n42\n\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	// An old Western European file: "blåbær" with one byte per letter.
	if got := string(decodeText([]byte("bl\xE5b\xE6r \x93quoted\x94"))); got != "blåbær “quoted”" {
		t.Errorf("Windows-1252: %q", got)
	}
	// UTF-16, as Windows Notepad once saved by default.
	if got := string(decodeText([]byte{0xFF, 0xFE, 'h', 0, 'e', 0, 'i', 0, 0xE5, 0})); got != "heiå" {
		t.Errorf("UTF-16: %q", got)
	}
	if got := string(srtToVTT([]byte("not a subtitle file at all"))); got != "WEBVTT\n\n" {
		t.Errorf("rubbish in: %q", got)
	}
}
