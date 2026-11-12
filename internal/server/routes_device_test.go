package server

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// These tests cover the routes that show what was learned and let it be
// forgotten (routes_device.go), and the viewer's preferences.

const pixelPhone = "Mozilla/5.0 (Linux; Android 14; Pixel 8 Build/AP2A) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36"

// keys lists the names in a JSON object, sorted.
func keys(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	var names []string
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// The owner is shown every device, by its kind, with what was learned
// about each, and can forget any of it. A viewer is shown the device in
// their hand and nothing else.
func TestTheOwnerSeesEveryDeviceAndAViewerTheirOwn(t *testing.T) {
	l := newLessonEnv(t)
	l.member("kid", "member password")
	phone := l.browser(pixelPhone)
	l.pc.do("POST", "/api/device/hdr-wrong", map[string]bool{"on": true})
	l.clock.forward(2 * time.Hour)
	phone.signIn("kid", "member password")
	phoneSeen := l.clock.at
	// The owner looks half an hour later, which makes the PC the device
	// seen last.
	l.clock.forward(30 * time.Minute)
	onPhone, err := l.s.learn(phone.device(), l.a, trouble(hevc4K))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.s.learn(phone.device(), l.a, lessonsIn(playback.Step{Lesson: playback.LessonNetwork, Network: playback.NetworkLesson{MaxRate: 5_000_000, Hours: 24}})[0]); err != nil {
		t.Fatal(err)
	}

	// The owner's list, exactly as it is sent.
	w := l.pc.do("GET", "/api/devices", nil)
	var raw struct {
		Devices []json.RawMessage
		PC      json.RawMessage
	}
	decode(t, w, &raw)
	if w.Code != 200 || len(raw.Devices) != 2 {
		t.Fatalf("the owner's list: %d %s", w.Code, w.Body)
	}
	if got := keys(t, raw.Devices[0]); got != "hdrWrong id label lastSeenAt lessons thisDevice" {
		t.Errorf("the owner's own device in the list has the fields %q", got)
	}
	if got := keys(t, raw.Devices[1]); got != "hdrWrong id label lastSeenAt lessons" {
		t.Errorf("another device in the owner's list has the fields %q", got)
	}
	if got := keys(t, raw.PC); got != "lessons" || !strings.Contains(string(raw.PC), `"lessons":[]`) {
		t.Errorf("what was learned about the PC is sent as %s", raw.PC)
	}
	var list struct{ Devices []deviceView }
	decode(t, w, &list)
	// The device seen last comes first.
	second, first := list.Devices[0], list.Devices[1]
	if first.Label != "Android phone" || first.ID != phone.device() || first.ThisDevice || first.HDRWrong || len(first.Lessons) != 2 ||
		first.Lessons[0].ID != onPhone || first.Lessons[0].Kind != "picture" || first.Lessons[1].Kind != "rate" {
		t.Errorf("the phone in the owner's list: %+v", first)
	}
	if got := keys(t, mustJSON(t, first.Lessons[0])); got != "expiresAt id kind learnedAt text" {
		t.Errorf("a lesson has the fields %q", got)
	}
	if second.Label != "Windows PC, Chrome" || second.ID != l.pc.device() || !second.ThisDevice || !second.HDRWrong || len(second.Lessons) != 1 || second.Lessons[0].Kind != "hdr" {
		t.Errorf("the PC in the owner's list: %+v", second)
	}
	if seen, err := time.Parse(time.RFC3339, first.LastSeenAt); err != nil || !seen.Equal(phoneSeen.Truncate(time.Second)) {
		t.Errorf("the phone was last seen %q, %v", first.LastSeenAt, err)
	}

	// The viewer's own device: no id, and only its own lessons.
	w = phone.do("GET", "/api/device", nil)
	var own deviceView
	decode(t, w, &own)
	if got := keys(t, w.Body.Bytes()); got != "hdrWrong label lessons" {
		t.Errorf("a viewer's device has the fields %q", got)
	}
	if w.Code != 200 || own.Label != "Android phone" || len(own.Lessons) != 2 || strings.Contains(w.Body.String(), "HDR") {
		t.Errorf("the viewer's own device: %d %s", w.Code, w.Body)
	}
	for _, try := range [][2]string{{"GET", "/api/devices"}, {"DELETE", "/api/devices/" + phone.device() + "/lessons/" + onPhone}} {
		if w := phone.do(try[0], try[1], nil); w.Code != 403 {
			t.Errorf("%s %s by someone who is not the owner: %d", try[0], try[1], w.Code)
		}
	}

	// The owner forgets a lesson of the phone's. Naming the wrong device
	// forgets nothing.
	for name, path := range map[string]string{
		"the lesson under another device":   "/api/devices/" + l.pc.device() + "/lessons/" + onPhone,
		"a lesson that does not exist":      "/api/devices/" + phone.device() + "/lessons/" + ids.New(),
		"a device that does not exist":      "/api/devices/" + ids.New() + "/lessons/" + onPhone,
		"nonsense in the device's place":    "/api/devices/x/lessons/" + onPhone,
		"nonsense in the lesson's place":    "/api/devices/" + phone.device() + "/lessons/x",
		"the PC's place, a device's lesson": "/api/devices/pc/lessons/" + onPhone,
	} {
		if w := l.pc.do("DELETE", path, nil); w.Code != 404 {
			t.Errorf("forgetting %s: %d %s", name, w.Code, w.Body)
		}
	}
	if n := l.count(`SELECT count(*) FROM lessons WHERE id=?`, onPhone); n != 1 {
		t.Fatal("a request that should have forgotten nothing forgot the lesson")
	}
	if w := l.pc.do("DELETE", "/api/devices/"+phone.device()+"/lessons/"+onPhone, nil); w.Code != 200 {
		t.Fatalf("the owner forgetting a lesson of the phone's: %d %s", w.Code, w.Body)
	}
	if left := l.lessons(phone); len(left) != 1 || left[0].Kind != "rate" {
		t.Errorf("the phone's lessons after the owner forgot one: %+v", left)
	}
	// A device stays in the list when nobody is signed in on it.
	phone.do("POST", "/api/logout", nil)
	list.Devices = nil
	decode(t, l.pc.do("GET", "/api/devices", nil), &list)
	if len(list.Devices) != 2 {
		t.Errorf("%d devices listed after one signed out", len(list.Devices))
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// What a viewer wants done with subtitles that have their own design is
// kept with their other preferences, and a page that does not know of the
// choice yet does not undo it.
func TestTheChoiceForSubtitlesWithADesign(t *testing.T) {
	e := newEnv(t)
	pc := e.browser(chromeOnWindows)
	pc.setup()
	var p prefsView
	w := pc.do("GET", "/api/prefs", nil)
	decode(t, w, &p)
	if w.Code != 200 || p.Styled != "designed" || p.Subs != "auto" || !strings.Contains(w.Body.String(), `"styled":"designed"`) {
		t.Fatalf("the preferences of someone who never chose: %s", w.Body)
	}
	w = pc.do("PUT", "/api/prefs", map[string]string{"audioLang": "no", "subLang": "en", "subs": "always", "styled": "plain"})
	decode(t, w, &p)
	if w.Code != 200 || p.Styled != "plain" || p.AudioLang != "no" {
		t.Fatalf("choosing plain words: %d %s", w.Code, w.Body)
	}
	// An older page sends the three it knows.
	w = pc.do("PUT", "/api/prefs", map[string]string{"audioLang": "en", "subLang": "", "subs": "off"})
	decode(t, w, &p)
	if w.Code != 200 || p.Styled != "plain" || p.AudioLang != "en" || p.Subs != "off" {
		t.Fatalf("an older page saving: %d %s", w.Code, w.Body)
	}
	p = prefsView{}
	decode(t, pc.do("GET", "/api/prefs", nil), &p)
	if p.Styled != "plain" || p.AudioLang != "en" || p.Subs != "off" {
		t.Errorf("read back: %+v", p)
	}
	for _, bad := range []string{"", "fancy", "PLAIN"} {
		if w := pc.do("PUT", "/api/prefs", map[string]string{"audioLang": "en", "subLang": "", "subs": "off", "styled": bad}); w.Code != 400 {
			t.Errorf("styled=%q was accepted: %d", bad, w.Code)
		}
	}
	// It is each viewer's own.
	e.member("kid", "member password")
	kid := e.browser(safariOnIPhone)
	kid.signIn("kid", "member password")
	p = prefsView{}
	decode(t, kid.do("GET", "/api/prefs", nil), &p)
	if p.Styled != "designed" {
		t.Errorf("another viewer's choice: %+v", p)
	}
}

// What Play's answer adds for the page: the token to report trouble under,
// for every film that plays, and the setting to ask for a lighter picture.
func TestPlayNamesTheTokenForTroubleAndTheLighterSetting(t *testing.T) {
	e := newTroubleEnv(t)
	var raw map[string]json.RawMessage
	decode(t, e.pc.do("POST", "/api/items/"+e.small+"/play", nil), &raw)
	for _, name := range []string{"play", "lighter"} {
		if _, ok := raw[name]; !ok {
			t.Errorf("Play's answer has no %q", name)
		}
	}
	if _, ok := raw["hls"]; ok {
		t.Error("a film sent as it is has a token for chunks")
	}
	// The small film (720p, 3 megabits a second) is lighter than every
	// setting down to 480p, which makes it smaller.
	if p := e.pc.play(e.small, nil); !ids.Valid(p.Play) || p.Lighter != playback.NumRungs {
		t.Errorf("the small film: token %q, lighter %d", p.Play, p.Lighter)
	}
	// A film that cannot be played has no token and nothing lighter.
	phone := e.signedIn(safariOnIPhone)
	phone.do("POST", "/api/capabilities", map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}})
	if p := phone.play(e.oldFilm, nil); p.Plan.Playable != "no" || p.Play != "" || p.Lighter != 0 || p.HLS != "" {
		t.Errorf("a film that cannot be played here: %+v", p)
	}
	if n := len(e.s.helpers.open.films); n != 2 {
		t.Errorf("%d films are remembered as open, want the two that play", n)
	}
}
