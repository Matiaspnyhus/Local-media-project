package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// A clip cut out of a longer film without re-encoding plays, when it is
// repackaged, with its clock a few seconds ahead of the film's own (see
// hls.Grid.LeadMs). Where a viewer stopped was saved as that player's clock
// read, so a device that plays the file as it is carried on those few
// seconds late, and the other way round those few seconds early. The place
// is the film's own time now, whichever way the clip was playing when it
// was saved and whichever way it plays when it is carried on with.
func TestWhereAViewerStoppedIsTheFilmsOwnTime(t *testing.T) {
	e := newConvertEnv(t)
	ffmpeg := probe.FindFFmpeg()
	// A minute of film with a key frame every 12 seconds, and its last 43
	// seconds cut out of it 5 seconds after a key frame.
	whole := filepath.Join(t.TempDir(), "whole.mp4")
	for _, recipe := range [][]string{
		{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=60", "-f", "lavfi", "-i", "sine=frequency=440:duration=60",
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "288", "-keyint_min", "288", "-sc_threshold", "0", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "96k", whole},
		{"-ss", "17", "-i", whole, "-c", "copy", filepath.Join(e.media, "Trimmed Clip (2011).mp4")},
	} {
		if out, err := exec.Command(ffmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, recipe...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg could not make the clip: %v %s", err, out)
		}
	}
	// Two devices: a browser without Dolby sound, for which the clip is
	// repackaged, and one that plays the file as it is.
	device := map[string]*http.Cookie{"repackaged": e.setup()}
	e.s.InspectNow()
	film := e.firstItem(device["repackaged"], "Trimmed Clip")
	e.do("POST", "/api/capabilities", remote, device["repackaged"], chromeLike)
	device["as it is"] = sessionCookie(e.do("POST", "/api/login", "192.168.1.60:1", nil, map[string]string{"name": "owner", "password": "a good password"}))
	e.do("POST", "/api/capabilities", remote, device["as it is"], map[string]any{"containers": map[string]bool{"mp4": true},
		"video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true, "ac3": true}, "hls": true})

	type answer struct {
		URL, HLS   string
		PositionMs int64
		DurationMs int64
		Subtitles  []subtitleView
	}
	play := func(as string) (p answer) {
		t.Helper()
		w := e.do("POST", "/api/items/"+film+"/play", remote, device[as], nil)
		if w.Code != 200 {
			t.Fatalf("play %s: %d %s", as, w.Code, w.Body)
		}
		decode(t, w, &p)
		if (as == "repackaged") != (p.HLS != "") {
			t.Fatalf("on the device that should get the clip %s, Play answered %+v", as, p)
		}
		return p
	}
	listed := func() (it itemView) {
		t.Helper()
		var l struct{ Items []itemView }
		decode(t, e.do("GET", "/api/movies", remote, device["as it is"], nil), &l)
		for _, it := range l.Items {
			if it.ID == film {
				return it
			}
		}
		t.Fatal("the clip is not listed")
		return it
	}
	save := func(as string, positionMs, durationMs int64) {
		t.Helper()
		if w := e.do("PUT", "/api/items/"+film+"/progress", remote, device[as], map[string]int64{"positionMs": positionMs, "durationMs": durationMs}); w.Code != 200 {
			t.Fatalf("saving the place: %d %s", w.Code, w.Body)
		}
	}

	// How far ahead the repackaged clip's clock runs is what its list of
	// chunks tells the player to skip at the start.
	p := play("repackaged")
	list := e.do("GET", p.URL, remote, nil, nil).Body.String()
	m := regexp.MustCompile(`#EXT-X-START:TIME-OFFSET=([0-9.]+)`).FindStringSubmatch(list)
	if m == nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("the clip's list of chunks does not start late, so this ffmpeg did not keep the frames from before the cut:\n%s", list)
		}
		t.Skip("this ffmpeg cuts a clip out without keeping frames from before the cut")
	}
	seconds, _ := strconv.ParseFloat(m[1], 64)
	ahead := int64(seconds * 1000)
	long := listed().DurationMs // the film's own length
	if ahead != 5000 || long < 42000 || long > 44000 || p.PositionMs != 0 {
		t.Fatalf("the clip runs %d ms ahead, the list says it is %d ms long, and Play starts it at %d; want 5000, about 43000 and 0", ahead, long, p.PositionMs)
	}
	// A player is told the length of its own timeline, as it is told the
	// place on its own clock: the repackaged clip's timeline is longer than
	// the film by what its clock runs ahead.
	if got := play("as it is").DurationMs; got != long {
		t.Errorf("the device that plays the file as it is is told the film is %d ms long, want %d", got, long)
	}
	if p.DurationMs != long+ahead {
		t.Errorf("the repackaged clip's player is told its timeline is %d ms long, want %d: the film's %d and the %d its clock runs ahead", p.DurationMs, long+ahead, long, ahead)
	}

	// Twenty seconds into the film, the repackaged clip's player reads 25
	// seconds, and its timeline is five seconds longer than the film.
	save("repackaged", 20000+ahead, long+ahead)
	if got := listed().PositionMs; got != 20000 {
		t.Errorf("stopped 20 seconds into the film on the repackaged clip: the list says %d ms", got)
	}
	if got := play("as it is").PositionMs; got != 20000 {
		t.Errorf("stopped 20 seconds into the film on the repackaged clip: played as it is, it carries on at %d ms, want 20000", got)
	}
	if got := play("repackaged").PositionMs; got != 20000+ahead {
		t.Errorf("stopped 20 seconds into the film: repackaged, its player is sent to %d ms, want %d on its own clock", got, 20000+ahead)
	}

	// The other way round: thirty seconds in, on the file as it is.
	save("as it is", 30000, long)
	if got := listed().PositionMs; got != 30000 {
		t.Errorf("stopped 30 seconds in on the file as it is: the list says %d ms", got)
	}
	p = play("repackaged")
	if p.PositionMs != 30000+ahead {
		t.Errorf("stopped 30 seconds in on the file as it is: repackaged, its player is sent to %d ms, want %d on its own clock", p.PositionMs, 30000+ahead)
	}
	// Its subtitles are moved by the same amount, as they were before.
	for _, s := range p.Subtitles {
		if !strings.HasSuffix(s.URL, "?shift=5000") {
			t.Errorf("a subtitle of the repackaged clip is asked for at %s", s.URL)
		}
	}

	// The page's player for chunks carries on from the place it is given
	// unless that is within the last five seconds of the length it is given,
	// and the page plays a file sent as it is by the same rule. (This is the
	// page's own arithmetic, repeated here; -1 is "from the beginning".) So a
	// place and a length on different clocks go wrong near the end: stopped
	// nine to five seconds before the end on one device, the clip started
	// from the beginning on the device that gets it repackaged. Wherever it
	// was stopped, both devices must do the same.
	carriesOn := func(p answer) int64 {
		if p.PositionMs > 0 && p.PositionMs < p.DurationMs-5000 {
			return p.PositionMs
		}
		return -1
	}
	for _, c := range []struct {
		film    int64 // where it was stopped, in the film's own time
		resumes bool
	}{{long - 12000, true}, {long - 9000, true}, {long - 7000, true}, {long - 5500, true}, {long - 4500, false}} {
		for _, savedOn := range []string{"as it is", "repackaged"} {
			runs := map[string]int64{"as it is": 0, "repackaged": ahead}
			play(savedOn)
			save(savedOn, c.film+runs[savedOn], long+runs[savedOn])
			if it := listed(); it.PositionMs != c.film || it.Played {
				t.Fatalf("stopped %d ms before the end on the device that plays the clip %s: the list says %d ms, watched %v", long-c.film, savedOn, it.PositionMs, it.Played)
			}
			for as, clock := range runs {
				want := int64(-1)
				if c.resumes {
					want = c.film + clock
				}
				if got := carriesOn(play(as)); got != want {
					t.Errorf("stopped %d ms before the end on the device that plays the clip %s: the device that plays it %s carries on at %d, want %d", long-c.film, savedOn, as, got, want)
				}
			}
		}
	}

	// The last thing a page does when its player is closed is to save the
	// place and to tell the PC to stop converting, and the two can arrive
	// in either order. The place must be read rightly after the stop too.
	p = play("repackaged")
	if w := e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, device["repackaged"], nil); w.Code != 200 {
		t.Fatalf("stop: %d", w.Code)
	}
	save("repackaged", 12000+ahead, long+ahead)
	if got := listed().PositionMs; got != 12000 {
		t.Errorf("a place saved just after the player was closed: the list says %d ms, want 12000", got)
	}

	// Near the end it counts as watched, by the film's own length.
	save("repackaged", 40000+ahead, long+ahead)
	if it := listed(); !it.Played {
		t.Errorf("40 of 43 seconds watched on the repackaged clip does not count as watched: %+v", it)
	}
	// A device that then plays the file as it is has a clock of its own:
	// what it saves is taken as it comes.
	play("as it is")
	save("as it is", 9000, long)
	if got := listed().PositionMs; got != 9000 {
		t.Errorf("a place saved by the device that plays the file as it is: the list says %d ms, want 9000", got)
	}
}

// The note of how far a player's clock runs ahead belongs to one sign-in
// and one film, goes when that film is next given out as it is, and the
// notes cannot pile up without end.
func TestClockNotesAreKeptBySignInAndFilm(t *testing.T) {
	e := newEnv(t)
	e.s.noteClock("phone", "film", 5000)
	e.s.noteClock("phone", "other film", 0)
	for who, want := range map[[2]string]int64{{"phone", "film"}: 5000, {"phone", "other film"}: 0, {"tv", "film"}: 0} {
		if got := e.s.clockAhead(who[0], who[1]); got != want {
			t.Errorf("%s playing %s: %d ms ahead, want %d", who[0], who[1], got, want)
		}
	}
	e.s.noteClock("phone", "film", 0)
	if got := e.s.clockAhead("phone", "film"); got != 0 {
		t.Errorf("after the film was given out as it is: still %d ms ahead", got)
	}
	for i := 0; i < 3*maxClockNotes; i++ {
		e.s.noteClock("phone", strconv.Itoa(i), 1000)
	}
	if n := len(e.s.helpers.clocks.ahead); n > maxClockNotes {
		t.Errorf("%d notes are kept, more than the %d allowed", n, maxClockNotes)
	}
	if got := e.s.clockAhead("phone", strconv.Itoa(3*maxClockNotes-1)); got != 1000 {
		t.Errorf("the newest note reads %d ms, want 1000", got)
	}
}
