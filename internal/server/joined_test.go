package server

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// These tests cover the places where the server and the converter (package
// hls) have to agree with each other, which neither package's own tests see.

// A TV recording (MPEG-TS) with AAC sound, on a device that plays both
// codecs but not the container: it is repackaged with nothing converted.
// That takes the server telling the converter which kind of sound it is
// passing on, and a list of cuts that has more than one chunk in it.
func TestATVRecordingWithAACSoundIsRepackaged(t *testing.T) {
	e := newConvertEnv(t)
	out, err := exec.Command(probe.FindFFmpeg(), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=20", "-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-pix_fmt", "yuv420p", "-c:a", "aac",
		"-f", "mpegts", filepath.Join(e.media, "Recorded Programme (2021).ts")).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg could not make the recording: %v %s", err, out)
	}
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Recorded Programme")
	chrome := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true}, "hls": true}
	if w := e.do("POST", "/api/capabilities", remote, c, chrome); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.Method != "remux" || !p.Plan.AudioCopy || p.HLS == "" {
		t.Fatalf("play: %+v", p)
	}
	list := e.do("GET", p.URL, remote, nil, nil)
	if n := strings.Count(list.Body.String(), "#EXTINF"); list.Code != 200 || n < 3 {
		t.Fatalf("a 20-second recording with a key frame every 2 seconds should be several chunks, got %d: %s", n, list.Body)
	}
	base := strings.TrimSuffix(p.URL, "index.m3u8")
	var stream bytes.Buffer
	// Chunk 1 first: a start partway through is where passed-on AAC failed.
	for _, name := range []string{"seg-1.m4s", "init.mp4", "seg-0.m4s"} {
		w := e.do("GET", base+name, remote, nil, nil)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if name != "seg-1.m4s" {
			stream.Write(w.Body.Bytes())
		}
	}
	joined := filepath.Join(t.TempDir(), "joined.mp4")
	os.WriteFile(joined, stream.Bytes(), 0o600)
	codecs, _ := exec.Command(probe.Find(), "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", joined).Output()
	if got := strings.Fields(string(codecs)); len(got) != 2 || got[0] != "h264" || got[1] != "aac" {
		t.Fatalf("the stream holds %q, want the untouched picture and AAC sound", got)
	}
	e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
}

// A file that does not say how long it is cannot be given a list of chunks
// for the whole film. The answer says so instead of handing out a list one
// second long.
func TestAFileOfUnknownLengthIsNotRepackaged(t *testing.T) {
	e := newConvertEnv(t)
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Test Film")
	chrome := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true}, "hls": true}
	e.do("POST", "/api/capabilities", remote, c, chrome)
	// The length is taken away by hand. On a slow machine the inspector that
	// ran when the library was set up can still be writing what it found,
	// and write the length back; so this is done until it has stayed away
	// across a whole Play.
	var p playAnswer
	for try := 0; ; try++ {
		e.s.db.Exec(`UPDATE files SET duration_ms=0`)
		p = playAnswer{}
		decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
		length := int64(-1)
		e.s.db.QueryRow(`SELECT max(duration_ms) FROM files`).Scan(&length)
		if length == 0 || try == 100 {
			break
		}
		if p.HLS != "" {
			e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, c, nil)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if p.Plan.Playable != "no" || p.Plan.Method != "none" || p.HLS != "" || !strings.Contains(strings.Join(p.Plan.Reasons, " "), "how long") {
		t.Fatalf("play: %+v", p)
	}
	if n := e.s.hls.Sessions(); n != 0 {
		t.Fatalf("%d conversions were opened for a film that cannot be repackaged", n)
	}
}

// When the key frames cannot be found, the film is cut every few seconds.
// The failed search knows where the film's clock starts; that list is the
// one to use, not one that starts the clock at zero.
func TestThePlainListKeepsTheFilmsOwnClock(t *testing.T) {
	tenHours := int64(10 * 3600 * 1000)
	fromSearch := hls.Grid{StartMs: tenHours, EndMs: tenHours + 30000, From: "uniform"}
	for at := tenHours; at < fromSearch.EndMs; at += 6000 {
		fromSearch.Cuts = append(fromSearch.Cuts, at)
	}
	if got := plainGrid(fromSearch, 30000); got.StartMs != tenHours || len(got.Cuts) != 5 {
		t.Errorf("the search's own list was not used: %+v", got)
	}
	if got := plainGrid(hls.Grid{}, 30000); !got.Valid() || got.StartMs != 0 || got.EndMs != 30000 {
		t.Errorf("with nothing from the search: %+v", got)
	}
}

// A clip whose player clock runs ahead of the film's gets its captions moved
// by the same amount, whichever way the subtitle reaches the browser.
func TestSubtitlesMoveWithAClockThatRunsAhead(t *testing.T) {
	for line, want := range map[string]string{
		"00:01.000 --> 00:04.000\n":                         "00:00:05.000 --> 00:00:08.000\n",
		"00:59:58.500 --> 01:00:00.250 line:10% align:left": "01:00:02.500 --> 01:00:04.250 line:10% align:left",
		"Arrows --> in the words stay as they are\n":        "Arrows --> in the words stay as they are\n",
		"12\n":                           "12\n",
		"NOTE 00:01.000 --> 00:04.000\n": "NOTE 00:01.000 --> 00:04.000\n",
	} {
		if got := string(laterCue([]byte(line), 4000)); got != want {
			t.Errorf("%q became %q, want %q", line, got, want)
		}
	}
	// The text arrives in pieces of any size.
	text := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nHello there\n\n00:00:07.500 --> 00:00:09.000\nGoodbye"
	for _, piece := range []int{1, 7, 1000} {
		rec := httptest.NewRecorder()
		w := &laterVTT{ResponseWriter: rec, ms: 4000}
		for rest := []byte(text); len(rest) > 0; {
			n := min(piece, len(rest))
			w.Write(rest[:n])
			rest = rest[n:]
		}
		w.finish()
		if got, want := rec.Body.String(), "WEBVTT\n\n00:00:05.000 --> 00:00:07.000\nHello there\n\n00:00:11.500 --> 00:00:13.000\nGoodbye"; got != want {
			t.Errorf("in pieces of %d:\n%q\nwant\n%q", piece, got, want)
		}
	}

	e := newConvertEnv(t)
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Test Film")
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if len(p.Subtitles) != 2 || strings.Contains(p.Subtitles[0].URL, "shift") {
		t.Fatalf("an ordinary film's subtitles should be asked for as they are: %+v", p.Subtitles)
	}
	// Inside the film (through ffmpeg, twice: as it is made, then the kept
	// copy), and the file beside it.
	for i := 0; i < 2; i++ {
		// (ffmpeg places this film's captions a few hundredths of a second
		// late, so only the tenths are compared.)
		if w := e.do("GET", p.Subtitles[0].URL+"?shift=4000", remote, c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "00:00:05.0") ||
			!strings.Contains(w.Body.String(), " --> 00:00:07.0") || !strings.Contains(w.Body.String(), "00:00:11.5") || strings.Contains(w.Body.String(), "00:00:01.0") {
			t.Fatalf("subtitle from inside the film, moved, try %d: %d %q", i, w.Code, w.Body)
		}
	}
	if w := e.do("GET", p.Subtitles[1].URL+"?shift=4000", remote, c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "00:00:05.000 --> 00:00:07.000\nHei på deg") {
		t.Fatalf("subtitle file beside the film, moved: %d %q", w.Code, w.Body)
	}
	// Nonsense is ignored, not obeyed.
	for _, bad := range []string{"-5", "abc", "99999999"} {
		if w := e.do("GET", p.Subtitles[1].URL+"?shift="+bad, remote, c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "00:00:01.000 --> 00:00:03.000") {
			t.Errorf("shift=%s: %d %q", bad, w.Code, w.Body)
		}
	}
}

// Finding the cuts of a long TV recording on a slow disk can take minutes.
// The viewer is not made to wait for it: the film starts with the plain
// list, the search carries on, and the next Play gets what it found.
func TestAFilmStartsWhileALongSearchCarriesOn(t *testing.T) {
	e := newConvertEnv(t)
	filmInMP4(t, e, "Slow Disk (2003).mp4")
	c := e.setup()
	e.s.InspectNow()
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	film := e.firstItem(c, "Slow Disk")
	slow := newCounted(t, `sleep 1; '`+e.s.ffprobe+`' "$@"`)
	e.s.ffprobe = slow.Path
	old := cutWait
	cutWait = 100 * time.Millisecond
	defer func() { cutWait = old }()

	began := time.Now()
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	took := time.Since(began)
	var kept int
	e.s.db.QueryRow(`SELECT count(*) FROM cuts`).Scan(&kept)
	if p.Plan.Method != "audio" || p.HLS == "" || kept != 0 {
		t.Fatalf("the film should have started with the plain list before the search ended (took %s, %d lists kept): %+v", took, kept, p)
	}
	list := e.do("GET", p.URL, remote, nil, nil).Body.String()
	if n := strings.Count(list, "#EXTINF"); n != 5 {
		t.Errorf("the plain list of a 30-second film should have 5 chunks, not %d", n)
	}
	// The search was not given up: its result arrives and is kept.
	for end := time.Now().Add(30 * time.Second); kept == 0 && time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		e.s.db.QueryRow(`SELECT count(*) FROM cuts`).Scan(&kept)
	}
	if kept != 1 {
		t.Fatal("the search that was still running when the film started never kept its result")
	}
	ran := slow.runs()
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if slow.runs() != ran {
		t.Error("the next Play searched again instead of using what was kept")
	}
}
