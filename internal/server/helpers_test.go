package server

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// counted stands a small script in for ffmpeg or ffprobe. The script notes
// each run in a folder (how many were running when it started, and what it
// was asked to do), waits a moment so that runs started together overlap,
// and then does what after says: usually, run the real program.
type counted struct {
	t    *testing.T
	dir  string
	Path string
}

func newCounted(t *testing.T, after string) *counted {
	t.Helper()
	dir := t.TempDir()
	body := `d='` + dir + `'
mkdir "$d/run.$$"
ls "$d" | grep -c '^run\.' >> "$d/running"
echo "$*" >> "$d/asked"
sleep 0.4
` + after + `
rc=$?
rmdir "$d/run.$$"
exit $rc`
	return &counted{t: t, dir: dir, Path: script(t, body)}
}

// runs is how many times the script was started.
func (c *counted) runs() int { return len(c.lines("asked")) }

// atOnce is the most copies of the script that ran at the same time.
func (c *counted) atOnce() int {
	most := 0
	for _, l := range c.lines("running") {
		n, _ := strconv.Atoi(strings.TrimSpace(l))
		most = max(most, n)
	}
	return most
}

func (c *counted) lines(name string) []string {
	b, _ := os.ReadFile(filepath.Join(c.dir, name))
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// together sends n requests at the same moment and returns the answers.
func together(n int, send func(i int) *httptest.ResponseRecorder) []*httptest.ResponseRecorder {
	out := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = send(i)
		}()
	}
	wg.Wait()
	return out
}

// However many players ask for the same subtitle track at once, the film
// is read through once, and all of them get the whole subtitle.
func TestOneSubtitleAskedForManyTimesAtOnceIsMadeOnce(t *testing.T) {
	e := newConvertEnv(t)
	c := e.setup()
	e.s.InspectNow()
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+e.firstItem(c, "Test Film")+"/play", remote, c, nil), &p)
	ffmpeg := newCounted(t, `'`+e.s.ffmpeg+`' "$@"`)
	e.s.ffmpeg = ffmpeg.Path

	answers := together(12, func(int) *httptest.ResponseRecorder { return e.do("GET", p.Subtitles[0].URL, remote, c, nil) })
	for i, w := range answers {
		if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "WEBVTT") || !strings.Contains(w.Body.String(), "Hello there") || !strings.Contains(w.Body.String(), "Goodbye") {
			t.Errorf("request %d got %d %q", i, w.Code, w.Body)
		}
	}
	if n := ffmpeg.runs(); n != 1 {
		t.Errorf("12 requests for one subtitle started ffmpeg %d times, want once", n)
	}
	// And it was kept: asking again starts nothing.
	if w := e.do("GET", p.Subtitles[0].URL, remote, c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Goodbye") || ffmpeg.runs() != 1 {
		t.Errorf("asking again: %d, ffmpeg started %d times in all", w.Code, ffmpeg.runs())
	}
}

// Different subtitles asked for at once each need a run of their own, but
// only a couple run at a time; the rest wait their turn, and a request
// that is given up while waiting starts nothing.
func TestOnlyAFewSubtitlesAreTakenOutAtOnce(t *testing.T) {
	e := newConvertEnv(t)
	langs := []string{"en", "de", "fr", "sv", "da"}
	for _, l := range langs {
		os.WriteFile(filepath.Join(e.media, "Test Film (2020)."+l+".ass"), []byte(strings.Replace(styledFile, "styled file", "styled file in "+l, 1)), 0o644)
	}
	c := e.setup()
	e.s.InspectNow()
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+e.firstItem(c, "Test Film")+"/play", remote, c, nil), &p)
	address := map[string]string{}
	for _, s := range p.Subtitles {
		if strings.HasPrefix(s.Key, "x") && s.Language != "no" {
			address[s.Language] = s.URL
		}
	}
	if len(address) != len(langs) {
		t.Fatalf("subtitle files found: %v", address)
	}
	ffmpeg := newCounted(t, `'`+e.s.ffmpeg+`' "$@"`)
	e.s.ffmpeg = ffmpeg.Path

	first := langs[:4]
	answers := together(len(first), func(i int) *httptest.ResponseRecorder { return e.do("GET", address[first[i]], remote, c, nil) })
	for i, w := range answers {
		if w.Code != 200 || !strings.Contains(w.Body.String(), "styled file in "+first[i]) {
			t.Errorf("subtitle %s: %d %q", first[i], w.Code, w.Body)
		}
	}
	if n, most := ffmpeg.runs(), ffmpeg.atOnce(); n != len(first) || most > maxSubtitleJobs {
		t.Errorf("%d subtitles: ffmpeg started %d times, %d at once; want %d runs and at most %d at once", len(first), n, most, len(first), maxSubtitleJobs)
	}

	// Every place is taken (here by the test itself, which makes "taken"
	// certain). A request for another subtitle waits, and its viewer
	// leaves before its turn comes.
	for i := 0; i < maxSubtitleJobs; i++ {
		e.s.helpers.subSlots <- struct{}{}
	}
	ctx, leave := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", address[langs[4]], nil).WithContext(ctx)
	r.RemoteAddr, r.Host = remote, "192.168.1.10:8177"
	r.AddCookie(c)
	gone := make(chan struct{})
	go func() {
		e.s.ServeHTTP(httptest.NewRecorder(), r)
		close(gone)
	}()
	time.Sleep(50 * time.Millisecond)
	leave()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("a request given up while waiting for its turn did not end")
	}
	e.s.background.Wait() // whatever it had started in the background has ended too
	for i := 0; i < maxSubtitleJobs; i++ {
		<-e.s.helpers.subSlots
	}
	if n := ffmpeg.runs(); n != len(first) {
		t.Errorf("ffmpeg started %d times in all; the request that left should not have started one (want %d)", n, len(first))
	}
	// With the places free again, the same subtitle is made as usual.
	if w := e.do("GET", address[langs[4]], remote, c, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "styled file in "+langs[4]) || ffmpeg.runs() != len(first)+1 {
		t.Errorf("asking again once there is room: %d %q, ffmpeg started %d times in all", w.Code, w.Body, ffmpeg.runs())
	}
}

// filmInMP4 puts a second small film in the library of a convert
// environment: H.264 with Dolby sound in an MP4, which a browser like
// Chrome needs repackaged, and whose key frames only ffprobe can find (an
// MKV carries a list of its own).
func filmInMP4(t *testing.T, e *env, name string) {
	t.Helper()
	out, err := exec.Command(probe.FindFFmpeg(), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=30", "-f", "lavfi", "-i", "sine=frequency=440:duration=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-pix_fmt", "yuv420p", "-c:a", "ac3", filepath.Join(e.media, name)).CombinedOutput()
	if err != nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not make %s: %v %s", name, err, out)
		}
		t.Skipf("this ffmpeg cannot make the test film: %v %s", err, out)
	}
}

var chromeLike = map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true},
	"audio": map[string]bool{"aac": true}, "hls": true}

// Pressing Play on a film for the first time starts a search for where it
// can be cut. Pressing it again while that search runs (going back and
// trying again, or several devices at once) must wait for the same search
// and not start another.
func TestOneKeyFrameSearchServesEveryPlayOfTheFilm(t *testing.T) {
	e := newConvertEnv(t)
	filmInMP4(t, e, "First Film (2001).mp4")
	filmInMP4(t, e, "Second Film (2002).mp4")
	c := e.setup()
	e.s.InspectNow()
	e.do("POST", "/api/capabilities", remote, c, chromeLike)
	first, second := e.firstItem(c, "First Film"), e.firstItem(c, "Second Film")
	ffprobe := newCounted(t, `'`+e.s.ffprobe+`' "$@"`)
	e.s.ffprobe = ffprobe.Path

	play := func(item string) *httptest.ResponseRecorder {
		w := e.do("POST", "/api/items/"+item+"/play", remote, c, nil)
		var p playAnswer
		decode(t, w, &p)
		if w.Code != 200 || p.Plan.Method != "audio" || !strings.HasPrefix(p.URL, "/hls/") {
			t.Errorf("play: %d %+v", w.Code, p)
		}
		return w
	}
	// How many times one search runs ffprobe is the converter's business;
	// it is measured here on the first film.
	play(first)
	one := ffprobe.runs()
	if one == 0 {
		t.Fatal("playing an MP4 for the first time did not run ffprobe at all, so this test proves nothing")
	}
	together(10, func(int) *httptest.ResponseRecorder { return play(second) })
	if n := ffprobe.runs() - one; n != one {
		t.Errorf("10 Play calls for one film ran ffprobe %d times; one search takes %d", n, one)
	}
	// The result was kept: the list of cuts is the real one, and playing
	// again searches no more.
	var kept int
	e.s.db.QueryRow(`SELECT count(*) FROM cuts`).Scan(&kept)
	play(second)
	if n := ffprobe.runs(); kept != 2 || n != 2*one {
		t.Errorf("%d lists of cuts kept (want 2); ffprobe ran %d times in all (want %d)", kept, n, 2*one)
	}
}

// A search that fails (a file ffprobe cannot make sense of, a disk too
// slow) is remembered for a while. Otherwise every Play of that film
// would run it again, each time making the viewer wait for nothing.
func TestAFailedKeyFrameSearchIsNotRepeatedOnEveryPlay(t *testing.T) {
	e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`, `1,'audio','dts',1,0,0,0,'eng',''`}, nil)
	e.pretendConverter(t)
	ffprobe := newCounted(t, "exit 1")
	e.s.ffprobe = ffprobe.Path
	play := func() {
		t.Helper()
		if p := e.play(t); p.Plan.Method != "audio" || !strings.HasPrefix(p.URL, "/hls/") {
			t.Fatalf("play: %+v", p)
		}
	}
	play()
	one := ffprobe.runs()
	if one == 0 {
		t.Fatal("the first Play did not run ffprobe, so this test proves nothing")
	}
	for i := 0; i < 3; i++ {
		play()
	}
	if n := ffprobe.runs(); n != one {
		t.Errorf("after a failed search, three more Plays ran ffprobe %d more times", n-one)
	}
	// Later it is tried again: the file may have been on a drive that was
	// asleep.
	e.s.helpers.cutMu.Lock()
	for id, f := range e.s.helpers.cutFails {
		f.at = time.Now().Add(-cutRetryAfter - time.Second)
		e.s.helpers.cutFails[id] = f
	}
	e.s.helpers.cutMu.Unlock()
	play()
	if n := ffprobe.runs(); n != 2*one {
		t.Errorf("after the wait, a Play ran ffprobe %d more times, want %d", n-one, one)
	}
}
