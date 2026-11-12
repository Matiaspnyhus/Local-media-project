package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// These tests report trouble in films that are really being converted, by
// the ffmpeg on this machine. How fast that ffmpeg is must not decide what
// a test finds, so wherever the pace matters to the answer the test says
// what it is (see paceOf); the real pace is only looked at to see that it
// is measured at all.

// longOldFilm puts a film in the library that takes the converter a while:
// four minutes, in the old format no browser plays.
func longOldFilm(t *testing.T, e *env, name string) {
	t.Helper()
	out, err := exec.Command(probe.FindFFmpeg(), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25:duration=240", "-f", "lavfi", "-i", "sine=frequency=440:duration=240",
		"-c:v", "mpeg4", "-q:v", "10", "-g", "100", "-c:a", "ac3", filepath.Join(e.media, name)).CombinedOutput()
	if err != nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not make %s: %v %s", name, err, out)
		}
		t.Skipf("this ffmpeg cannot make the test film: %v %s", err, out)
	}
}

// until waits for something to become true, for as long as a slow machine
// may need.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Minute); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
	}
}

// The whole of stepping down, on a film that is really converted: the film
// stalls, is started again lighter, and its chunks keep coming; what was
// learned is listed; the next play starts lower by itself; and forgetting
// the lesson puts the film back to its best setting.
func TestAConvertedFilmThatStallsIsStartedAgainLighter(t *testing.T) {
	e := newConvertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	pc := e.browser(chromeOnWindows)
	pc.setup()
	e.s.InspectNow()
	film := e.firstItem(pc.session(), "Old Film")
	if w := pc.do("POST", "/api/capabilities", chromeLike); w.Code != 200 {
		t.Fatal(w.Body)
	}
	first := pc.play(film, nil)
	if first.Plan.Method != "convert" || first.Plan.Video.Rung != 1 || first.Play != first.HLS || first.Lighter != 2 {
		t.Fatalf("play: %+v", first)
	}
	// The film plays: chunks arrive, and the PC measures how fast it makes
	// them.
	streamOf(t, e, first.URL, "seg-0.m4s", "seg-1.m4s", "seg-2.m4s")
	until(t, "the pace of a film that is being converted", func() bool { return pc.state(first.Play).Chunks >= 2 })
	if st := pc.state(first.Play); !st.Alive || st.Method != "convert" || st.Rung != 1 || !(st.Pace > 0) {
		t.Errorf("the state of a film that is playing: %+v", st)
	}

	// The player runs dry although the PC kept up: the connection is slow.
	e.s.testPace = healthy
	a := pc.mustTrouble(first.Play, "starved")
	if a.Action != "replay" || a.Notice != "The film was reaching this device too slowly, so the PC now makes the picture at a lighter setting." || len(a.Lessons) != 1 {
		t.Fatalf("the answer to the stall: %+v", a)
	}
	second := pc.replay(film, first.Play)
	pc.do("POST", "/api/hls/"+first.Play+"/stop", nil)
	if second.Plan.Method != "convert" || second.Plan.Video.Rung != 3 || second.Play == first.Play {
		t.Fatalf("started again: %+v", second)
	}
	if got := described(t, streamOf(t, e, second.URL, "seg-0.m4s", "seg-1.m4s"), "codec_name,width,height"); len(got) != 2 || got[0] != "h264,320,180" || got[1] != "aac" {
		t.Errorf("started again, the stream holds %q", got)
	}
	if n := e.stepsTaken(second.Play); n != 1 {
		t.Errorf("the new play has %d steps to its name, want the one it took over", n)
	}
	pc.do("POST", "/api/hls/"+second.Play+"/stop", nil)

	// The lesson is listed, and the next play starts lower without it.
	seen := e.lessons(pc)
	if len(seen) != 1 || seen[0].ID != a.Lessons[0] || seen[0].Kind != "rate" {
		t.Fatalf("the device's lessons: %+v", seen)
	}
	next := pc.play(film, nil)
	if next.Plan.Video.Rung != 3 || e.stepsTaken(next.Play) != 0 {
		t.Errorf("the next play: setting %d with %d steps taken", next.Plan.Video.Rung, e.stepsTaken(next.Play))
	}
	pc.do("POST", "/api/hls/"+next.Play+"/stop", nil)
	// Forgetting it restores the best setting.
	if w := pc.do("DELETE", "/api/device/lessons/"+seen[0].ID, nil); w.Code != 200 {
		t.Fatalf("forgetting: %d %s", w.Code, w.Body)
	}
	restored := pc.play(film, nil)
	if restored.Plan.Video.Rung != 1 {
		t.Errorf("after forgetting, the film plays at setting %d", restored.Plan.Video.Rung)
	}

	// This time the PC itself does not keep up. Nothing else is being
	// converted, so what was seen lowers the PC's table.
	e.s.testPace = slow
	if st := pc.state(restored.Play); st.Advice != "lower" {
		t.Errorf("a conversion that is too slow is not advised against: %+v", st)
	}
	b := pc.mustTrouble(restored.Play, "starved")
	if b.Action != "replay" || b.Lower != 2 || len(b.Lessons) != 0 {
		t.Fatalf("the answer when the PC did not keep up: %+v", b)
	}
	if got := e.s.speeds("")[playback.ClassHD][0]; got != 0.5 {
		t.Errorf("the speed table after a slow conversion: %v", e.s.speeds(""))
	}
	lighter := pc.replay(film, restored.Play)
	if lighter.Plan.Video.Rung != 2 {
		t.Errorf("started again after the PC did not keep up: %+v", lighter.Plan)
	}
	if got := described(t, streamOf(t, e, lighter.URL, "seg-0.m4s"), "codec_name"); len(got) != 2 || got[0] != "h264" {
		t.Errorf("at the lighter setting the stream holds %q", got)
	}
}

// A device that turns out not to show a film's own picture gets the film
// converted, for real: the film was being repackaged with its H.264
// picture untouched, and after the report its chunks hold a picture this
// PC made.
func TestAFilmWhoseOwnPictureGivesTroubleIsReallyConverted(t *testing.T) {
	e := newConvertEnv(t)
	pc := e.browser(chromeOnWindows)
	pc.setup()
	e.s.InspectNow()
	film := e.firstItem(pc.session(), "Test Film")
	pc.do("POST", "/api/capabilities", chromeLike)
	first := pc.play(film, nil)
	if first.Plan.Method != "audio" || first.Play != first.HLS {
		t.Fatalf("play: %+v", first)
	}
	a := pc.mustTrouble(first.Play, "decode")
	if a.Action != "replay" || a.Notice != "This device had trouble showing the picture, so the PC now converts the picture to a kind every device plays." {
		t.Fatalf("the answer: %+v", a)
	}
	second := pc.replay(film, first.Play)
	if second.Plan.Method != "convert" || second.Plan.Video.Rung != 1 {
		t.Fatalf("started again: %+v", second.Plan)
	}
	list := e.do("GET", second.URL, remote, nil, nil)
	if n := strings.Count(list.Body.String(), "#EXTINF"); list.Code != 200 || n != 4 {
		t.Fatalf("12 seconds cut every 3 should be 4 chunks, got %d: %s", n, list.Body)
	}
	if got := described(t, streamOf(t, e, second.URL, "seg-0.m4s", "seg-1.m4s"), "codec_name,width,height"); len(got) != 2 || got[0] != "h264,320,180" || got[1] != "aac" {
		t.Errorf("the converted stream holds %q", got)
	}
	if how := e.methods(pc); how[film] != "convert" {
		t.Errorf("the list says %q for the film", how[film])
	}
}

// A speed seen while another viewer's film was being converted as well is
// not kept: each got a share of the PC. The film that stalled is still
// started again lighter.
func TestASlowConversionBesideAnotherViewersTeachesNothingAboutThePC(t *testing.T) {
	e := newConvertEnv(t)
	oldFilm(t, e, "Old Film (1999).mkv", "25")
	longOldFilm(t, e, "Long Old Film (1998).mkv")
	pc := e.browser(chromeOnWindows)
	pc.setup()
	e.s.InspectNow()
	film, long := e.firstItem(pc.session(), "Old Film"), e.firstItem(pc.session(), "Long Old Film")
	pc.do("POST", "/api/capabilities", chromeLike)
	mine := pc.play(film, nil)
	if mine.Plan.Video.Rung != 1 {
		t.Fatalf("play: %+v", mine.Plan)
	}
	// Someone else starts a long film, which the PC goes on converting.
	other := e.signedIn(firefoxOnLinux)
	other.do("POST", "/api/capabilities", chromeLike)
	theirs := other.play(long, nil)
	if theirs.Plan.Method != "convert" {
		t.Fatalf("the other viewer's play: %+v", theirs.Plan)
	}
	streamOf(t, e, theirs.URL, "seg-0.m4s")
	var mySignIn string
	if err := e.s.db.QueryRow(`SELECT id FROM sessions WHERE device_id=?`, pc.device()).Scan(&mySignIn); err != nil {
		t.Fatal(err)
	}
	if n := e.s.hls.Converting(mySignIn); n != 1 {
		t.Fatalf("%d other conversions are running after another viewer's film started, want 1", n)
	}

	e.s.testPace = slow
	a := pc.mustTrouble(mine.Play, "starved")
	if a.Action != "replay" || a.Lower != 2 {
		t.Fatalf("the answer: %+v", a)
	}
	if got := e.s.speeds(""); len(got) != 0 {
		t.Errorf("a speed seen beside another conversion was kept: %v", got)
	}
	other.do("POST", "/api/hls/"+theirs.Play+"/stop", nil)
	until(t, "the other viewer's conversion to stop", func() bool { return e.s.hls.Converting("") == 0 })
	if again := pc.replay(film, mine.Play); again.Plan.Video.Rung != 2 {
		t.Errorf("started again: setting %d, want 2 for the rest of this play", again.Plan.Video.Rung)
	}
	if fresh := pc.play(film, nil); fresh.Plan.Video.Rung != 1 {
		t.Errorf("a new play starts at setting %d although nothing was learned about the PC", fresh.Plan.Video.Rung)
	}
}
