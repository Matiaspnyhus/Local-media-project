package server

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// These tests cover what happens when a page reports trouble in a film it
// is playing (trouble.go), through the routes a page uses. Most of them
// need no ffmpeg: the server is told it has a converter, the films are
// entered as the inspector would have found them, and a play is opened
// without a chunk ever being asked for. The ones that convert a real film
// are in trouble_real_test.go.

// played is the part of a Play answer these tests look at.
type played struct {
	URL     string
	HLS     string
	Play    string
	Lighter int
	Plan    struct {
		Playable, Method string
		Reasons          []string
		Video            playback.Video
		Picture          playback.PictureKind
	}
}

func (b *browser) play(item string, body map[string]any) played {
	b.e.t.Helper()
	var p played
	w := b.do("POST", "/api/items/"+item+"/play", body)
	if w.Code != 200 {
		b.e.t.Fatalf("play: %d %s", w.Code, w.Body)
	}
	decode(b.e.t, w, &p)
	return p
}

// replay plays an item again in place of the play named by from.
func (b *browser) replay(item, from string) played {
	b.e.t.Helper()
	return b.play(item, map[string]any{"from": from})
}

func (b *browser) trouble(token, evidence string) (int, troubleAnswer) {
	b.e.t.Helper()
	var a troubleAnswer
	w := b.do("POST", "/api/hls/"+token+"/trouble", map[string]string{"evidence": evidence})
	if w.Code == 200 {
		decode(b.e.t, w, &a)
	}
	return w.Code, a
}

// mustTrouble reports trouble that has to be accepted.
func (b *browser) mustTrouble(token, evidence string) troubleAnswer {
	b.e.t.Helper()
	code, a := b.trouble(token, evidence)
	if code != 200 {
		b.e.t.Fatalf("reporting %s: %d", evidence, code)
	}
	return a
}

func (b *browser) state(token string) playState {
	b.e.t.Helper()
	var st playState
	w := b.do("GET", "/api/hls/"+token+"/state", nil)
	if w.Code != 200 {
		b.e.t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	decode(b.e.t, w, &st)
	return st
}

// What a device that plays nearly everything says about itself: MP4 with
// H.264 or HEVC, at any size.
var playsMost = map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true, "hevc": true, "hevc10": true},
	"audio": map[string]bool{"aac": true}, "hls": true, "tall": map[string]bool{"h264": true, "hevc": true, "hevc10": true}, "pageFullscreen": true}

// troubleEnv is a server that believes it can convert pictures, with five
// films entered as inspected: three large HEVC ones that the device plays
// as they are, a small H.264 one, and an old one that has to be converted.
type troubleEnv struct {
	*env
	clock                           *testClock
	pc                              *browser
	big, big2, big3, small, oldFilm string // items
}

func newTroubleEnv(t *testing.T) *troubleEnv {
	t.Helper()
	e := newPretendEnv(t, "9.9")
	for _, name := range []string{"Big Film (2020).mp4", "Second Big Film (2021).mp4", "Third Big Film (2022).mp4", "Small Film (2019).mp4", "Old Film (1999).mp4"} {
		if err := os.WriteFile(filepath.Join(e.media, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	te := &troubleEnv{env: e, clock: e.clock(), pc: e.browser(chromeOnWindows)}
	te.pc.setup()
	// From here on a look at a film's picture finds what was noted below,
	// and the inspector is never started.
	e.s.ffprobe = filepath.Join(t.TempDir(), "no-such-ffprobe")
	hevc := playback.Track{Codec: "hevc", BitDepth: 10, Width: 3840, Height: 2160}
	te.big = e.inspectedAs(te.pc, "Big Film", hevc, 20_000_000)
	te.big2 = e.inspectedAs(te.pc, "Second Big Film", hevc, 20_000_000)
	te.big3 = e.inspectedAs(te.pc, "Third Big Film", hevc, 20_000_000)
	te.small = e.inspectedAs(te.pc, "Small Film", playback.Track{Codec: "h264", BitDepth: 8, Width: 1280, Height: 720}, 3_000_000)
	te.oldFilm = e.inspectedAs(te.pc, "Old Film", playback.Track{Codec: "mpeg4", BitDepth: 8, Width: 1920, Height: 1080}, 6_000_000)
	if w := te.pc.do("POST", "/api/capabilities", playsMost); w.Code != 200 {
		t.Fatal(w.Body)
	}
	return te
}

// inspectedAs enters a film as the inspector would have found it: an MP4
// with the given picture and stereo AAC sound, ten minutes long. It also
// notes what a look at the picture would find, so that Play can open the
// film for conversion without ffprobe. It returns the film's item.
func (e *env) inspectedAs(b *browser, title string, v playback.Track, bitRate int) string {
	e.t.Helper()
	item := e.firstItem(b.session(), title)
	file := e.fileOf(item)
	var size, mtime int64
	if err := e.s.db.QueryRow(`SELECT size, mtime FROM files WHERE id=?`, file).Scan(&size, &mtime); err != nil {
		e.t.Fatal(err)
	}
	e.must(`UPDATE files SET probed=1, container='mp4', duration_ms=600000, bit_rate=? WHERE id=?`, bitRate, file)
	e.must(`INSERT INTO streams(file_id,idx,kind,codec,codec_tag,bit_depth,width,height,frame_rate,channels,is_default) VALUES(?,0,'video',?,?,?,?,?,'25',0,1),(?,1,'audio','aac','',0,0,0,'',2,1)`,
		file, v.Codec, map[string]string{"hevc": "hvc1"}[v.Codec], v.BitDepth, v.Width, v.Height, file)
	facts, err := json.Marshal(hls.PictureFacts{LastMs: 599960, EndMs: 600000, RateNum: 25, RateDen: 1, Width: v.Width, Height: v.Height, SarNum: 1, SarDen: 1, Format: "mov,mp4,m4a,3gp,3g2,mj2"})
	if err != nil {
		e.t.Fatal(err)
	}
	e.must(`INSERT INTO picture_facts(file_id,stamp,facts) VALUES(?,?,?)`, file, fmt.Sprintf("%d-%d-%s", size, mtime, factsForm), string(facts))
	return item
}

// listed is how the list of films says each film reaches this browser.
func (e *env) methods(b *browser) map[string]string {
	e.t.Helper()
	var out struct{ Items []itemView }
	w := b.do("GET", "/api/movies", nil)
	if w.Code != 200 {
		e.t.Fatalf("the list: %d %s", w.Code, w.Body)
	}
	decode(e.t, w, &out)
	how := map[string]string{}
	for _, it := range out.Items {
		how[it.ID] = it.Method
	}
	return how
}

// slow and healthy stand in for the converter's own measure of its pace.
func slow(string) (float64, int)    { return 0.5, 3 }
func healthy(string) (float64, int) { return 3, 3 }

// A device that says it plays a film and then cannot show it: the film is
// started again converted, and the device is not sent that film's own
// picture again. That holds for the one film until a second film of the
// same kind gives the same trouble; from then on for every such film. The
// viewer sees what was learned and can have it forgotten.
func TestADeviceThatCannotShowAFilmsOwnPicture(t *testing.T) {
	e := newTroubleEnv(t)
	first := e.pc.play(e.big, nil)
	if first.Plan.Method != "direct" || first.HLS != "" || !ids.Valid(first.Play) || first.Lighter != 1 {
		t.Fatalf("the large film should be sent as it is, with a token to report trouble under: %+v", first)
	}
	a := e.pc.mustTrouble(first.Play, "decode")
	if a.Action != "replay" || a.Notice != "This device had trouble showing the picture, so the PC now converts the picture to a smaller one (720p)." || a.Lower != 0 || len(a.Lessons) != 1 {
		t.Fatalf("the answer to trouble showing the picture: %+v", a)
	}
	again := e.pc.replay(e.big, first.Play)
	if again.Plan.Method != "convert" || again.Plan.Video.Rung != 3 || again.HLS == "" || again.Play != again.HLS {
		t.Fatalf("started again, the film is played as: %+v", again)
	}
	if why := strings.Join(again.Plan.Reasons, " "); !strings.Contains(why, "which this device has had trouble showing") {
		t.Errorf("the reasons do not say why it is converted: %q", again.Plan.Reasons)
	}
	// What was learned is about that one film so far.
	seen := e.lessons(e.pc)
	if len(seen) != 1 || seen[0].ID != a.Lessons[0] || seen[0].Kind != "picture" ||
		seen[0].Text != "This device had trouble showing one 4K 10-bit HEVC (H.265) film, so that film is converted for it." {
		t.Errorf("the device's lessons: %+v", seen)
	}
	if how := e.methods(e.pc); how[e.big] != "convert" || how[e.big2] != "direct" || how[e.big3] != "direct" || how[e.small] != "direct" {
		t.Errorf("after one film gave trouble the list says: %v", how)
	}
	// The next play of that film starts converted, without anyone asking.
	if next := e.pc.play(e.big, nil); next.Plan.Method != "convert" || next.Plan.Video.Rung != 3 {
		t.Errorf("the next play of the film: %+v", next.Plan)
	}

	// A second film of the same kind: now it is about the kind.
	second := e.pc.play(e.big2, nil)
	b := e.pc.mustTrouble(second.Play, "decode")
	if b.Action != "replay" || len(b.Lessons) != 1 || b.Lessons[0] != a.Lessons[0] {
		t.Fatalf("the second film: %+v", b)
	}
	seen = e.lessons(e.pc)
	if len(seen) != 1 || seen[0].Text != "This device had trouble showing 4K 10-bit HEVC (H.265) films, so they are converted for it." {
		t.Errorf("the device's lessons after a second film: %+v", seen)
	}
	if how := e.methods(e.pc); how[e.big3] != "convert" || how[e.small] != "direct" {
		t.Errorf("after two films gave trouble the list says: %v", how)
	}
	// Another device is not affected, whoever uses it.
	phone := e.signedIn(safariOnIPhone)
	phone.do("POST", "/api/capabilities", playsMost)
	if how := e.methods(phone); how[e.big] != "direct" || how[e.big3] != "direct" {
		t.Errorf("another device's list changed: %v", how)
	}
	if len(e.lessons(phone)) != 0 {
		t.Error("another device is shown this device's lessons")
	}

	// Forgetting puts everything back.
	if w := e.pc.do("DELETE", "/api/device/lessons/"+a.Lessons[0], nil); w.Code != 200 {
		t.Fatalf("forgetting: %d %s", w.Code, w.Body)
	}
	if how := e.methods(e.pc); how[e.big] != "direct" || how[e.big2] != "direct" || how[e.big3] != "direct" {
		t.Errorf("after forgetting the list says: %v", how)
	}
	if p := e.pc.play(e.big, nil); p.Plan.Method != "direct" {
		t.Errorf("after forgetting the film is played as: %+v", p.Plan)
	}
}

// lessons is what GET /api/device lists for a browser.
func (e *env) lessons(b *browser) []lessonView {
	e.t.Helper()
	var d deviceView
	w := b.do("GET", "/api/device", nil)
	if w.Code != 200 {
		e.t.Fatalf("GET /api/device: %d %s", w.Code, w.Body)
	}
	decode(e.t, w, &d)
	return d.Lessons
}

// A player that runs dry although the PC keeps up has a slow connection:
// for a day, heavy films are converted for that device, and light ones are
// left alone. If the lighter picture runs dry too, an even lighter one is
// made.
func TestAConnectionThatCannotKeepUp(t *testing.T) {
	e := newTroubleEnv(t)
	e.s.testPace = healthy
	first := e.pc.play(e.big, nil)
	a := e.pc.mustTrouble(first.Play, "starved")
	if a.Action != "replay" || a.Notice != "The film was reaching this device too slowly, so the PC now converts the picture to a smaller one (720p)." || len(a.Lessons) != 1 {
		t.Fatalf("the answer to a player that ran dry: %+v", a)
	}
	seen := e.lessons(e.pc)
	if len(seen) != 1 || seen[0].Kind != "rate" || !strings.Contains(seen[0].Text, "more than 5 megabits a second") {
		t.Errorf("the device's lessons: %+v", seen)
	}
	if learnedAt, _ := time.Parse(time.RFC3339, seen[0].LearnedAt); !learnedAt.Equal(e.clock.at.Truncate(time.Second)) || seen[0].ExpiresAt != stamp(e.clock.at.Add(24*time.Hour).Unix()) {
		t.Errorf("learned %s, runs out %s; want now and a day from now", seen[0].LearnedAt, seen[0].ExpiresAt)
	}
	// It holds for every heavy film at once, and not for a light one.
	if how := e.methods(e.pc); how[e.big] != "convert" || how[e.big2] != "convert" || how[e.small] != "direct" {
		t.Errorf("with a slow connection the list says: %v", how)
	}
	second := e.pc.replay(e.big, first.Play)
	if second.Plan.Method != "convert" || second.Plan.Video.Rung != 3 {
		t.Fatalf("started again: %+v", second.Plan)
	}
	// The 720p picture runs dry as well.
	b := e.pc.mustTrouble(second.Play, "starved")
	if b.Action != "replay" || !strings.Contains(b.Notice, "480p") || len(b.Lessons) != 1 || b.Lessons[0] == a.Lessons[0] {
		t.Fatalf("the answer when the lighter picture ran dry too: %+v", b)
	}
	third := e.pc.replay(e.big, second.Play)
	if third.Plan.Video.Rung != playback.NumRungs {
		t.Fatalf("started again a second time: %+v", third.Plan)
	}
	// Nothing lighter is made: the film is left playing, and nothing is
	// said, because nothing was learned.
	if c := e.pc.mustTrouble(third.Play, "starved"); c.Action != "wait" || c.Notice != "" || len(c.Lessons) != 0 {
		t.Errorf("at the lightest picture: %+v", c)
	}
	// A day later the connection gets another chance.
	e.clock.forward(24*time.Hour + time.Second)
	if how := e.methods(e.pc); how[e.big] != "direct" || how[e.big2] != "direct" {
		t.Errorf("a day later the list says: %v", how)
	}
	if len(e.lessons(e.pc)) != 0 {
		t.Error("a day later the lesson is still listed")
	}
}

// A film that this PC converts slower than it plays is started again at a
// lighter setting, and the speed that was seen lowers the PC's table, so
// that the next film starts lighter for everyone. There are at most two
// such steps in one play, counted by the server however the page behaves.
func TestAPCThatConvertsTooSlowly(t *testing.T) {
	e := newTroubleEnv(t)
	e.s.testPace = slow
	first := e.pc.play(e.oldFilm, nil)
	if first.Plan.Method != "convert" || first.Plan.Video.Rung != 1 || first.Lighter != 2 {
		t.Fatalf("the old film: %+v", first)
	}
	a := e.pc.mustTrouble(first.Play, "starved")
	if a.Action != "replay" || a.Lower != 2 || len(a.Lessons) != 0 || a.Notice != "This PC could not convert the film fast enough, so the PC now makes the picture at a lighter setting." {
		t.Fatalf("the answer when the PC did not keep up: %+v", a)
	}
	if got := e.s.speeds("")[playback.ClassHD]; got != [playback.NumRungs]float64{0.5, 0, 0, 0, 0} {
		t.Errorf("the speed table after the first step: %v", got)
	}
	second := e.pc.replay(e.oldFilm, first.Play)
	if second.Plan.Video.Rung != 2 {
		t.Fatalf("started again: %+v", second.Plan)
	}
	b := e.pc.mustTrouble(second.Play, "starved")
	if b.Action != "replay" || b.Lower != 3 || !strings.Contains(b.Notice, "720p") {
		t.Fatalf("the second step: %+v", b)
	}
	third := e.pc.replay(e.oldFilm, second.Play)
	if third.Plan.Video.Rung != 3 {
		t.Fatalf("started again a second time: %+v", third.Plan)
	}
	// A third step is not taken, although a lighter setting exists.
	c := e.pc.mustTrouble(third.Play, "starved")
	if c.Action != "wait" || c.Lower != 0 || c.Notice != "This film has already been changed twice to make it play better and is still having trouble, so nothing more was changed." {
		t.Errorf("a third step: %+v", c)
	}
	if got := e.s.speeds("")[playback.ClassHD]; got != [playback.NumRungs]float64{0.5, 0.5, 0, 0, 0} {
		t.Errorf("the speed table after two steps and a refused third: %v", got)
	}

	// A page that asks again about a play that was told to start again is
	// told the same, and nothing more comes of it.
	before := e.troubleLines()
	for i := 0; i < 3; i++ {
		if again := e.pc.mustTrouble(first.Play, "starved"); again.Action != "replay" || again.Lower != 2 {
			t.Errorf("asked again about the first play: %+v", again)
		}
		if again := e.pc.mustTrouble(third.Play, "starved"); again.Action != "wait" || again.Notice != c.Notice {
			t.Errorf("asked again about the third play: %+v", again)
		}
	}
	if after := e.troubleLines(); after != before {
		t.Errorf("asking again wrote %d more lines to the log", after-before)
	}
	// A page that leaves out "from" starts a new play, with a count of its
	// own: that is the viewer pressing Play again. It starts where the
	// speed table now says, for this viewer and for any other.
	if fresh := e.pc.play(e.oldFilm, nil); fresh.Plan.Video.Rung != 3 {
		t.Errorf("a new play of the film: %+v", fresh.Plan)
	}
	phone := e.signedIn(safariOnIPhone)
	phone.do("POST", "/api/capabilities", playsMost)
	if other := phone.play(e.oldFilm, nil); other.Plan.Video.Rung != 3 {
		t.Errorf("the film on another device: %+v", other.Plan)
	}
	// The owner sees both speeds among what was learned about the PC.
	var list struct {
		PC struct{ Lessons []lessonView }
	}
	decode(t, e.pc.do("GET", "/api/devices", nil), &list)
	if len(list.PC.Lessons) != 2 || list.PC.Lessons[0].ID != "x264.hd.1" || list.PC.Lessons[1].ID != "x264.hd.2" {
		t.Errorf("what was learned about the PC: %+v", list.PC.Lessons)
	}
	// What was counted: two steps and one refusal, each once.
	if got := e.troubleCount("starved", cameReplay); got != 2 {
		t.Errorf("%d steps counted, want 2", got)
	}
	if got := e.troubleCount("starved", cameLimit); got != 1 {
		t.Errorf("%d refusals counted, want 1", got)
	}
}

// stepsTaken is how many steps the server has counted for a play, the
// plays it took over from included.
func (e *env) stepsTaken(token string) int {
	e.t.Helper()
	o := &e.s.helpers.open
	o.mu.Lock()
	defer o.mu.Unlock()
	f := o.films[token]
	if f == nil {
		e.t.Fatalf("the server remembers no film under %s", token)
	}
	return len(f.taken)
}

// troubleLines is how many lines about trouble the log holds.
func (e *env) troubleLines() int {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.s.dataDir, "logs", "localmedia.log"))
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.Count(string(raw), "trouble while playing")
}

// troubleCount is how often a kind of trouble came to an outcome.
func (e *env) troubleCount(evidence, came string) int {
	e.t.Helper()
	var counts troubleCounts
	json.Unmarshal([]byte(e.s.setting("trouble_counts")), &counts)
	return counts.Counts[evidence+" "+came]
}

// A speed is only taken from a film that was the one picture being
// converted. With another running beside it, each gets a share of the PC
// and the figure says little.
func TestASpeedSeenBesideAnotherConversionIsNotKept(t *testing.T) {
	e := newTroubleEnv(t)
	e.s.testPace = slow
	p := e.pc.play(e.oldFilm, nil)
	// As the server remembers a film that was planned while another was
	// being converted.
	o := &e.s.helpers.open
	o.mu.Lock()
	o.films[p.Play].limits.Busy = 1
	o.mu.Unlock()
	if a := e.pc.mustTrouble(p.Play, "starved"); a.Action != "replay" || a.Lower == 0 {
		t.Fatalf("the film is still started again lighter: %+v", a)
	}
	if got := e.s.speeds(""); len(got) != 0 {
		t.Errorf("a speed seen beside another conversion was kept: %v", got)
	}
	// The lighter setting still holds for the rest of this play, through
	// "from", although nothing was kept.
	if next := e.pc.replay(e.oldFilm, p.Play); next.Plan.Video.Rung <= p.Plan.Video.Rung {
		t.Errorf("started again at setting %d after setting %d did not keep up", next.Plan.Video.Rung, p.Plan.Video.Rung)
	}
	if fresh := e.pc.play(e.oldFilm, nil); fresh.Plan.Video.Rung != 1 {
		t.Errorf("a new play starts at setting %d although nothing was learned about the PC", fresh.Plan.Video.Rung)
	}
}

// Only the sign-in a film was opened for may report trouble in it or ask
// how it is doing. Everyone else is told what a stranger to the token is
// told, so that a token cannot be tested for.
func TestOnlyTheSignInThatOpenedAFilmReportsTroubleInIt(t *testing.T) {
	e := newTroubleEnv(t)
	e.s.testPace = slow
	// A play that has taken both its steps: whoever took over from it
	// would be allowed no more.
	first := e.pc.play(e.oldFilm, nil)
	e.pc.mustTrouble(first.Play, "starved")
	p := e.pc.replay(e.oldFilm, first.Play)
	e.pc.mustTrouble(p.Play, "starved")
	if n := e.stepsTaken(p.Play); n != 2 {
		t.Fatalf("the play has taken %d steps, want 2", n)
	}

	sameUser := e.signedIn(firefoxOnLinux)
	e.member("kid", "member password")
	kid := e.browser(safariOnIPhone)
	kid.signIn("kid", "member password")
	nobody := e.browser(chromeOnWindows)
	for who, b := range map[string]*browser{"another sign-in of the same user": sameUser, "another user": kid} {
		if code, _ := b.trouble(p.Play, "starved"); code != 403 {
			t.Errorf("%s reporting trouble in the film: %d", who, code)
		}
		if st := b.state(p.Play); st != (playState{}) {
			t.Errorf("%s asking how the film is doing: %+v", who, st)
		}
		// Stopping it does nothing to it either.
		if w := b.do("POST", "/api/hls/"+p.Play+"/stop", nil); w.Code != 200 || !e.pc.state(p.Play).Alive {
			t.Errorf("%s stopped the film: %d", who, w.Code)
		}
		// Nor can they start a play of their own that takes over from it.
		b.do("POST", "/api/capabilities", playsMost)
		if theirs := b.replay(e.oldFilm, p.Play); theirs.Plan.Method != "convert" || e.stepsTaken(theirs.Play) != 0 {
			t.Errorf("%s took over the steps of someone else's play: %d", who, e.stepsTaken(theirs.Play))
		}
	}
	if got := e.pc.mustTrouble(p.Play, "starved"); got.Action != "replay" {
		t.Errorf("the sign-in that opened the film, asking again: %+v", got)
	}
	if code, _ := nobody.trouble(p.Play, "starved"); code != 401 {
		t.Errorf("reporting trouble without a sign-in: %d", code)
	}
	if w := nobody.do("GET", "/api/hls/"+p.Play+"/state", nil); w.Code != 401 {
		t.Errorf("asking about a film without a sign-in: %d", w.Code)
	}
	// A token that was never handed out gets the same answers.
	if code, _ := e.pc.trouble(ids.New(), "starved"); code != 403 {
		t.Errorf("trouble in a film that does not exist: %d", code)
	}
	if st := e.pc.state(ids.New()); st != (playState{}) {
		t.Errorf("the state of a film that does not exist: %+v", st)
	}
	// What is not a report is refused before anything is looked up.
	for name, body := range map[string]any{
		"no evidence":       map[string]string{},
		"unknown evidence":  map[string]string{"evidence": "boredom"},
		"something besides": map[string]string{"evidence": "starved", "title": "x"},
		"not an object":     []string{"starved"},
	} {
		if w := e.pc.do("POST", "/api/hls/"+p.Play+"/trouble", body); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// A write needs the page's own header, as everywhere.
	if w := e.ask("POST", "/api/hls/"+p.Play+"/trouble", remote, "192.168.1.10:8177", e.pc.session(), map[string]string{"Content-Type": "application/json"}, `{"evidence":"starved"}`); w.Code != 403 {
		t.Errorf("a report without the page's header: %d", w.Code)
	}
}

// A film that has been closed takes no more reports, but the play that
// replaces it can still take over its count of steps for a few minutes,
// whichever of "stop the old one" and "start the new one" comes first.
func TestAClosedFilmStillHandsOverItsSteps(t *testing.T) {
	e := newTroubleEnv(t)
	e.s.testPace = slow
	first := e.pc.play(e.oldFilm, nil)
	if a := e.pc.mustTrouble(first.Play, "starved"); a.Action != "replay" {
		t.Fatalf("%+v", a)
	}
	// The page stops the old play before it starts the new one.
	if w := e.pc.do("POST", "/api/hls/"+first.Play+"/stop", nil); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if st := e.pc.state(first.Play); st.Alive {
		t.Error("a film that was stopped is still called alive")
	}
	if code, _ := e.pc.trouble(first.Play, "starved"); code != 403 {
		t.Errorf("trouble reported in a film that was stopped: %d", code)
	}
	second := e.pc.replay(e.oldFilm, first.Play)
	if a := e.pc.mustTrouble(second.Play, "starved"); a.Action != "replay" {
		t.Fatalf("the second step: %+v", a)
	}
	third := e.pc.replay(e.oldFilm, second.Play)
	if a := e.pc.mustTrouble(third.Play, "starved"); a.Action != "wait" {
		t.Errorf("the play that took over from a stopped one was allowed a third step: %+v", a)
	}

	// The same for a film that is sent as it is, which has no conversion
	// to stop: stopping it closes it all the same.
	direct := e.pc.play(e.small, nil)
	if st := e.pc.state(direct.Play); !st.Alive || st.Method != "direct" || st.Rung != 0 || st.Chunks != 0 || st.Advice != "" {
		t.Errorf("the state of a film sent as it is: %+v", st)
	}
	e.pc.do("POST", "/api/hls/"+direct.Play+"/stop", nil)
	if st := e.pc.state(direct.Play); st.Alive {
		t.Error("a film sent as it is was stopped and is still called alive")
	}

	// Ten minutes after a film was closed it hands nothing over any more:
	// a play that names it starts with a count of its own.
	e.clock.forward(handover + time.Second)
	late := e.pc.replay(e.oldFilm, third.Play) // third is still open; first and second are closed
	e.pc.do("POST", "/api/hls/"+late.Play+"/stop", nil)
	e.pc.do("POST", "/api/hls/"+third.Play+"/stop", nil)
	e.clock.forward(handover + time.Second)
	fresh := e.pc.replay(e.oldFilm, third.Play)
	if a := e.pc.mustTrouble(fresh.Play, "starved"); a.Action != "replay" {
		t.Errorf("a play that named a film closed long ago was refused a first step: %+v", a)
	}
	// Signing out forgets the sign-in's films at once.
	e.pc.do("POST", "/api/logout", nil)
	if n := len(e.s.helpers.open.films); n != 0 {
		t.Errorf("%d films are remembered for a sign-in that signed out", n)
	}
}

// "from" is taken only from a film of the same sign-in and the same item.
// Anything else in it is the same as leaving it out.
func TestWhatAPlayMayTakeOverFrom(t *testing.T) {
	e := newTroubleEnv(t)
	e.s.testPace = slow
	first := e.pc.play(e.oldFilm, nil)
	e.pc.mustTrouble(first.Play, "starved")
	second := e.pc.replay(e.oldFilm, first.Play)
	e.pc.mustTrouble(second.Play, "starved") // two steps taken: a play that takes over gets no third

	third := e.pc.replay(e.oldFilm, second.Play)
	if n := e.stepsTaken(third.Play); n != 2 || third.Plan.Video.Rung != 3 {
		t.Fatalf("the play that took over has %d steps and plays at setting %d", n, third.Plan.Video.Rung)
	}
	if a := e.pc.mustTrouble(third.Play, "starved"); a.Action != "wait" {
		t.Errorf("a play that took over from the second step was allowed a third: %+v", a)
	}
	// A play may take over from one that took over: the count goes along.
	if p := e.pc.replay(e.oldFilm, third.Play); e.stepsTaken(p.Play) != 2 {
		t.Error("the steps were lost at the second handing over")
	}
	// Another item: nothing is carried over.
	big := e.pc.play(e.big, map[string]any{"from": second.Play})
	if big.Plan.Method != "direct" || e.stepsTaken(big.Play) != 0 {
		t.Errorf("a play of another film took over the old film's steps: %+v", big.Plan)
	}
	for name, from := range map[string]any{
		"another film's play": big.Play, "a token never handed out": ids.New(), "nonsense": "../../etc/passwd", "a number": 7, "an empty text": "",
	} {
		p := e.pc.play(e.oldFilm, map[string]any{"from": from})
		// The speed table was lowered by the two steps, so the film starts
		// at the third setting by itself: but as a new play.
		if p.Plan.Method != "convert" || p.Plan.Video.Rung != 3 || e.stepsTaken(p.Play) != 0 {
			t.Errorf("from %s: the play is %+v with %d steps, and should have a count of its own", name, p.Plan, e.stepsTaken(p.Play))
		}
	}
}

// A page that reports trouble in a loop is refused after a dozen reports
// in a minute, and whatever it reports again and again is kept, counted
// and logged once.
func TestAPageThatReportsTroubleInALoop(t *testing.T) {
	e := newTroubleEnv(t)
	p := e.pc.play(e.big, nil)
	codes := map[int]int{}
	for i := 0; i < 100; i++ {
		code, _ := e.pc.trouble(p.Play, "decode")
		codes[code]++
	}
	if codes[200] != troublePerMinute || codes[429] != 100-troublePerMinute {
		t.Errorf("100 reports in a moment were answered: %v", codes)
	}
	if hits := e.count(`SELECT sum(hits) FROM lessons`); hits != 1 || e.count(`SELECT count(*) FROM lessons`) != 1 {
		t.Errorf("100 reports of the same trouble count as %d", hits)
	}
	if n := e.troubleLines(); n != 1 {
		t.Errorf("100 reports wrote %d lines to the log", n)
	}
	if n := e.troubleCount("decode", cameReplay); n != 1 {
		t.Errorf("100 reports were counted as %d", n)
	}
	w := e.pc.do("POST", "/api/hls/"+p.Play+"/trouble", map[string]string{"evidence": "decode"})
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Errorf("the refusal: %d %v", w.Code, w.Header())
	}
	// Another sign-in is not held back by it.
	phone := e.signedIn(safariOnIPhone)
	phone.do("POST", "/api/capabilities", playsMost)
	if code, _ := phone.trouble(phone.play(e.big, nil).Play, "decode"); code != 200 {
		t.Errorf("another sign-in was refused: %d", code)
	}
	// A minute later the first one may report again.
	e.clock.forward(time.Minute + time.Second)
	if code, _ := e.pc.trouble(p.Play, "decode"); code != 200 {
		t.Errorf("a minute later: %d", code)
	}
	// Reports that are refused, or about nothing, do not fill the memory.
	for i := 0; i < 3000; i++ {
		e.s.helpers.trouble.limiter.allow(ids.New(), e.clock.at)
	}
	if n := len(e.s.helpers.trouble.limiter.recent); n > 2001 {
		t.Errorf("the limiter remembers %d sign-ins", n)
	}
}

// What is remembered about open films is bounded: a sign-in that opens
// film after film without closing any keeps only the newest dozen.
func TestOpenFilmsAreBounded(t *testing.T) {
	e := newTroubleEnv(t)
	var tokens []string
	for i := 0; i < maxOpenPerSignIn+8; i++ {
		tokens = append(tokens, e.pc.play(e.small, nil).Play)
	}
	if n := len(e.s.helpers.open.films); n != maxOpenPerSignIn {
		t.Errorf("%d films are remembered for one sign-in, want %d", n, maxOpenPerSignIn)
	}
	if st := e.pc.state(tokens[0]); st.Alive {
		t.Error("the film opened first is still remembered")
	}
	if st := e.pc.state(tokens[len(tokens)-1]); !st.Alive {
		t.Error("the film opened last was forgotten")
	}
	// A link to a film sent as it is lasts twelve hours, and so does what
	// is remembered about it.
	e.clock.forward(linkLife + time.Second)
	if st := e.pc.state(tokens[len(tokens)-1]); st.Alive {
		t.Error("a film sent as it is outlived its link")
	}
	e.pc.play(e.small, nil)
	if n := len(e.s.helpers.open.films); n != 1 {
		t.Errorf("%d films are remembered after the others' links ran out", n)
	}
}

// How a film is doing: whether it is still open, how it reaches the
// player, how fast it is being converted, and whether a lighter picture
// would help.
func TestHowAnOpenFilmIsDoing(t *testing.T) {
	e := newTroubleEnv(t)
	p := e.pc.play(e.oldFilm, nil)
	for name, tc := range map[string]struct {
		pace   func(string) (float64, int)
		want   playState
		advice string
	}{
		"nothing converted yet":         {func(string) (float64, int) { return 0, 0 }, playState{Alive: true, Method: "convert", Rung: 1}, ""},
		"keeping up":                    {healthy, playState{Alive: true, Method: "convert", Rung: 1, Pace: 3, Chunks: 3}, ""},
		"too slow over three chunks":    {slow, playState{Alive: true, Method: "convert", Rung: 1, Pace: 0.5, Chunks: 3, Advice: "lower"}, "lower"},
		"too slow, but only two so far": {func(string) (float64, int) { return 0.5, 2 }, playState{Alive: true, Method: "convert", Rung: 1, Pace: 0.5, Chunks: 2}, ""},
		"a little slow":                 {func(string) (float64, int) { return 0.97, 3 }, playState{Alive: true, Method: "convert", Rung: 1, Pace: 0.97, Chunks: 3}, ""},
		"a pace that is no number":      {func(string) (float64, int) { return math.NaN(), 3 }, playState{Alive: true, Method: "convert", Rung: 1}, ""},
		"a pace without end":            {func(string) (float64, int) { return math.Inf(1), 3 }, playState{Alive: true, Method: "convert", Rung: 1}, ""},
	} {
		e.s.testPace = tc.pace
		if got := e.pc.state(p.Play); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
	// At the lightest setting there is nothing lighter to advise.
	e.s.testPace = slow
	lightest := e.pc.play(e.oldFilm, map[string]any{"lower": playback.NumRungs})
	if st := e.pc.state(lightest.Play); st.Rung != playback.NumRungs || st.Advice != "" || lightest.Lighter != 0 {
		t.Errorf("at the lightest setting: %+v, and Play offers setting %d as lighter", st, lightest.Lighter)
	}
	// Once the page has been told to start the film again, it is not
	// advised to report the same again.
	e.pc.mustTrouble(p.Play, "starved")
	if st := e.pc.state(p.Play); st.Advice != "" || !st.Alive {
		t.Errorf("after the film was told to start again: %+v", st)
	}
	// Asking is not watching: it does not keep a conversion alive, so it
	// must not count as a request for the film.
	if n := e.s.hls.Running(); n != 0 {
		t.Errorf("%d conversions were started by asking how a film is doing", n)
	}
}

// On a PC that cannot convert, trouble still teaches something, but the
// film cannot be made easier to show: it is left playing, and the viewer
// is told that nothing was changed. The sentence about the lesson does not
// promise a conversion that does not happen.
func TestTroubleOnAPCThatCannotConvert(t *testing.T) {
	e := newEnv(t) // no converter at all
	pc := e.browser(chromeOnWindows)
	pc.setup()
	film := e.inspectedAs(pc, "clip", playback.Track{Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080}, 8_000_000)
	pc.do("POST", "/api/capabilities", playsMost)
	p := pc.play(film, nil)
	if p.Plan.Method != "direct" || p.HLS != "" || p.Play == "" || p.Lighter != 3 {
		t.Fatalf("play: %+v", p)
	}
	a := pc.mustTrouble(p.Play, "decode")
	if a.Action != "wait" || a.Notice != "This device had trouble showing the picture, but nothing easier to show can be made of this film here, so nothing was changed." || len(a.Lessons) != 1 {
		t.Fatalf("the answer: %+v", a)
	}
	seen := e.lessons(pc)
	if len(seen) != 1 || seen[0].Text != "This device had trouble showing one 1080p H.264 film, but this PC cannot convert pictures, so nothing is changed for now." {
		t.Errorf("the lesson: %+v", seen)
	}
	// Reported again, it is answered the same and counted once.
	if again := pc.mustTrouble(p.Play, "decode"); again.Notice != a.Notice || e.count(`SELECT hits FROM lessons`) != 1 {
		t.Errorf("reported again: %+v, counted %d times", again, e.count(`SELECT hits FROM lessons`))
	}
	// A slow connection cannot be helped here either.
	if b := pc.mustTrouble(p.Play, "starved"); b.Action != "wait" || !strings.Contains(b.Notice, "nothing lighter can be made of it here") {
		t.Errorf("a player that ran dry: %+v", b)
	}
	// Nothing draws subtitles yet, so a report about that teaches nothing.
	if c := pc.mustTrouble(p.Play, "renderer"); c.Action != "wait" || c.Notice != "" || len(c.Lessons) != 0 {
		t.Errorf("a report about drawing subtitles: %+v", c)
	}
	if e.troubleCount("decode", cameUnchanged) != 1 || e.troubleCount("starved", cameUnchanged) != 1 || e.troubleCount("renderer", cameNothing) != 1 {
		t.Errorf("counted: %s", e.s.setting("trouble_counts"))
	}

	// A film that has not been inspected: nobody knows what kind of
	// picture it has, so there is no kind to learn about.
	other := pc.play(e.firstItem(pc.session(), "Some Movie"), nil)
	if other.Plan.Playable != "unknown" || other.Play == "" {
		t.Fatalf("a film that was not inspected: %+v", other)
	}
	if d := pc.mustTrouble(other.Play, "decode"); d.Action != "wait" || d.Notice != "" || len(d.Lessons) != 0 || e.count(`SELECT count(*) FROM lessons WHERE kind='picture'`) != 1 {
		t.Errorf("trouble in a film that was not inspected: %+v", d)
	}
}

// Nothing a page puts into the words it sends along reaches the log as
// more than a short line.
func TestWhatThePageSaysAboutTroubleIsCutShortForTheLog(t *testing.T) {
	e := newTroubleEnv(t)
	p := e.pc.play(e.big, nil)
	said := "3 stalls\nin 60 s\r\n2026-01-01T00:00:00Z a forged line\t" + strings.Repeat("x", 500)
	if w := e.pc.do("POST", "/api/hls/"+p.Play+"/trouble", map[string]string{"evidence": "decode", "detail": said}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	raw, _ := os.ReadFile(filepath.Join(e.s.dataDir, "logs", "localmedia.log"))
	var line string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, "trouble while playing") {
			line = l
		}
	}
	if !strings.Contains(line, "the page said: 3 stalls in 60 s 2026-01-01T00:00:00Z a forged line x") || strings.Count(line, "x") > 150 || strings.Contains(line, "\r") {
		t.Errorf("the line in the log: %q", line)
	}
	if !strings.Contains(line, "decode in a film sent by direct (picture hevc/10/4k)") || !strings.Contains(line, "the film is started again by convert at setting 3") {
		t.Errorf("the line does not say what happened: %q", line)
	}
	for _, name := range []string{"Big Film", "(2020)", ".mp4"} {
		if strings.Contains(line, name) {
			t.Errorf("the line names the film (%q): %q", name, line)
		}
	}
}
