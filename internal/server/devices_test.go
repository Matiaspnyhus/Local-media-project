package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
)

// These tests cover how devices are told apart (devices.go): the cookie,
// the row behind it, and that neither lets anyone do anything.

// Signing in gives the browser a device of its own. The cookie is handed
// out like the sign-in cookie, lasts 400 days, and is not taken back when
// the viewer signs out: the next sign-in on that browser is the same
// device.
func TestASignInIsMadeOnADeviceThatOutlivesIt(t *testing.T) {
	e := newEnv(t)
	pc := e.browser(chromeOnWindows)
	pc.setup()
	signIn, device := pc.set[cookieName], pc.set[deviceCookie]
	if device == nil || device.Value == "" {
		t.Fatal("signing in set no device cookie")
	}
	if !device.HttpOnly || device.Secure || device.SameSite != signIn.SameSite || device.Path != signIn.Path || device.Domain != signIn.Domain {
		t.Errorf("the device cookie is not handed out like the sign-in cookie: %+v against %+v", device, signIn)
	}
	if device.MaxAge != 400*24*3600 {
		t.Errorf("the device cookie lasts %d seconds, want 400 days", device.MaxAge)
	}
	first := pc.device()
	var label, onDevice string
	if err := e.s.db.QueryRow(`SELECT d.label, s.device_id FROM sessions s JOIN devices d ON d.id = s.device_id`).Scan(&label, &onDevice); err != nil {
		t.Fatalf("the sign-in does not point at a device: %v", err)
	}
	if label != "Windows PC, Chrome" || onDevice != first || !ids.Valid(first) {
		t.Errorf("the device is %q with id %q, and the cookie names %q", label, onDevice, first)
	}

	// Signing out takes the sign-in away and leaves the device.
	if w := pc.do("POST", "/api/logout", nil); w.Code != 200 {
		t.Fatalf("sign-out: %d %s", w.Code, w.Body)
	}
	if pc.cookies[cookieName] != nil || pc.set[deviceCookie] != nil || pc.device() != first {
		t.Errorf("after signing out the browser holds %v, and the answer touched the device cookie: %v", pc.cookies, pc.set[deviceCookie])
	}
	// The next sign-in on this browser, by anyone, is on the same device,
	// and sets the cookie afresh so that it lasts from today.
	e.member("kid", "member password")
	pc.signIn("kid", "member password")
	if pc.device() != first || pc.set[deviceCookie] == nil || pc.set[deviceCookie].MaxAge != 400*24*3600 {
		t.Errorf("signing in again: device %q (was %q), cookie set afresh: %v", pc.device(), first, pc.set[deviceCookie])
	}
	if n := e.count(`SELECT count(*) FROM devices`); n != 1 {
		t.Errorf("%d devices after two sign-ins on one browser", n)
	}
	if n := e.count(`SELECT count(*) FROM sessions WHERE device_id=?`, first); n != 2 {
		t.Errorf("%d sign-ins point at the device, want both", n)
	}

	// Another browser is another device.
	phone := e.signedIn(safariOnIPhone)
	if phone.device() == "" || phone.device() == first {
		t.Errorf("a second browser got device %q, the first has %q", phone.device(), first)
	}
	if n := e.count(`SELECT count(*) FROM devices WHERE label='iPhone'`); n != 1 {
		t.Errorf("%d devices are called iPhone", n)
	}
}

// The device cookie lets nobody in: with it and no sign-in, every route
// answers as it does to a stranger.
func TestTheDeviceCookieGrantsNothing(t *testing.T) {
	e := newEnv(t)
	pc := e.browser(chromeOnWindows)
	pc.setup()
	film := e.firstItem(pc.session(), "clip")
	pc.do("POST", "/api/logout", nil)
	if pc.cookies[deviceCookie] == nil || pc.cookies[cookieName] != nil {
		t.Fatalf("the browser should hold the device cookie and nothing else: %v", pc.cookies)
	}
	fill := strings.NewReplacer("{id}", film, "{fileID}", ids.New(), "{token}", ids.New(), "{lesson}", ids.New())
	for _, rt := range e.s.Routes() {
		if rt.Policy == Public {
			continue
		}
		w := pc.do(rt.Method, fill.Replace(rt.Pattern), map[string]string{})
		if w.Code != 401 && w.Code != 403 && w.Code != 409 {
			t.Errorf("%s %s answered %d to a device cookie without a sign-in: %s", rt.Method, rt.Pattern, w.Code, w.Body)
		}
	}
	var state struct{ User any }
	decode(t, pc.do("GET", "/api/state", nil), &state)
	if state.User != nil {
		t.Errorf("the device cookie alone is taken for a user: %v", state.User)
	}
	// Nor does it make the PC do anything: no row was written for it.
	if n := e.count(`SELECT count(*) FROM devices`); n != 1 {
		t.Errorf("%d devices", n)
	}
}

// A device cookie the server did not make is replaced, not believed: not
// nonsense, not another device's id without its signature, not a signature
// from another device, and not a device that has since been removed.
func TestACookieTheServerDidNotMakeIsReplaced(t *testing.T) {
	e := newEnv(t)
	pc := e.browser(chromeOnWindows)
	pc.setup()
	victim := pc.device()
	e.must(`INSERT INTO lessons(id,device_id,kind,value,learned_at,expires_at) VALUES(?,?,'hdr','',?,?)`,
		ids.New(), victim, time.Now().Unix(), time.Now().Add(time.Hour).Unix())
	// A device that was known once and is gone.
	gone := e.browser(firefoxOnLinux)
	gone.signIn("owner", "a good password")
	goneCookie := gone.cookies[deviceCookie].Value
	e.must(`DELETE FROM devices WHERE id=?`, gone.device())

	forged := map[string]string{
		"nonsense":                        "hello",
		"empty":                           "",
		"very long":                       strings.Repeat("a", 5000),
		"another device's id":             victim,
		"its id with a made-up signature": victim + "." + strings.Repeat("A", 43),
		"its id with another's signature": victim + "." + strings.SplitN(goneCookie, ".", 2)[1],
		"a device that has been removed":  goneCookie,
		"something for the database":      `x' OR '1'='1`,
	}
	for name, value := range forged {
		for _, how := range []string{"at sign-in", "on a signed-in request"} {
			b := e.browser(chromeOnWindows)
			if how == "at sign-in" {
				b.cookies[deviceCookie] = &http.Cookie{Name: deviceCookie, Value: value}
				b.signIn("owner", "a good password")
			} else {
				b.signIn("owner", "a good password")
				// The sign-in's own device is taken away too, so that only
				// the cookie could say which device this is.
				e.must(`UPDATE sessions SET device_id=NULL WHERE device_id=?`, b.device())
				b.cookies[deviceCookie] = &http.Cookie{Name: deviceCookie, Value: value}
				if w := b.do("GET", "/api/device", nil); w.Code != 200 {
					t.Fatalf("%s, %s: %d %s", name, how, w.Code, w.Body)
				}
			}
			got := b.device()
			if got == "" || got == victim || b.set[deviceCookie] == nil || b.set[deviceCookie].Value == value {
				t.Errorf("%s, %s: the browser now has device %q (the other device is %q), cookie replaced: %v", name, how, got, victim, b.set[deviceCookie] != nil)
				continue
			}
			var seen struct{ Lessons []lessonView }
			decode(t, b.do("GET", "/api/device", nil), &seen)
			if len(seen.Lessons) != 0 {
				t.Errorf("%s, %s: the browser was shown another device's lessons: %+v", name, how, seen.Lessons)
			}
		}
	}
}

// A sign-in from before there were devices gets one on the first thing its
// browser asks for. A page asks for several things at once when it opens:
// all of them must end up on the same device.
func TestAnOlderSignInGetsItsDeviceOnTheFirstRequest(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	// As the upgrade leaves it: signed in, no device, no cookie.
	e.must(`UPDATE sessions SET device_id=NULL`)
	e.must(`DELETE FROM devices`)

	answers := together(12, func(int) *httptest.ResponseRecorder {
		return e.ask("GET", "/api/movies", remote, "192.168.1.10:8177", c, map[string]string{"User-Agent": safariOnIPhone}, "")
	})
	values := map[string]int{}
	for i, w := range answers {
		if w.Code != 200 {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
		for _, set := range w.Result().Cookies() {
			if set.Name == deviceCookie {
				values[set.Value]++
			}
		}
	}
	if len(values) != 1 {
		t.Errorf("12 requests at once were handed %d different devices: %v", len(values), values)
	}
	var label string
	if n := e.count(`SELECT count(*) FROM devices`); n != 1 {
		t.Errorf("%d devices were made for one browser", n)
	}
	if err := e.s.db.QueryRow(`SELECT d.label FROM sessions s JOIN devices d ON d.id=s.device_id`).Scan(&label); err != nil || label != "iPhone" {
		t.Errorf("the sign-in's device: %q %v", label, err)
	}
}

// A browser that lost only the device cookie is handed it again, for the
// same device, and one that has it is not sent it on every request.
func TestTheDeviceCookieIsHandedOutAgainOnlyWhenItIsMissing(t *testing.T) {
	e := newEnv(t)
	pc := e.browser(chromeOnWindows)
	pc.setup()
	first := pc.device()
	if w := pc.do("GET", "/api/movies", nil); w.Code != 200 || pc.set[deviceCookie] != nil {
		t.Errorf("a request that had the cookie was sent it again: %d %v", w.Code, pc.set[deviceCookie])
	}
	delete(pc.cookies, deviceCookie)
	if w := pc.do("GET", "/api/movies", nil); w.Code != 200 || pc.device() != first {
		t.Errorf("after losing the cookie the browser is device %q, was %q (%d)", pc.device(), first, w.Code)
	}
	if n := e.count(`SELECT count(*) FROM devices`); n != 1 {
		t.Errorf("%d devices", n)
	}
	// A sign-in carried to a browser that is a device already (it has the
	// cookie from someone else's sign-in) is noted as being on that device.
	other := e.signedIn(firefoxOnLinux)
	second := other.device()
	other.cookies[cookieName] = pc.cookies[cookieName]
	var seen deviceView
	decode(t, other.do("GET", "/api/device", nil), &seen)
	if other.device() != second || seen.Label != "Linux PC, Firefox" {
		t.Errorf("the browser changed device to %q (%q) because of the sign-in it was given", other.device(), seen.Label)
	}
}

// When a device was last seen is written down at most once an hour, not on
// every request, and a device not seen for longer than its cookie lasts is
// removed with what was learned about it.
func TestWhenADeviceWasLastSeen(t *testing.T) {
	e := newEnv(t)
	clock := e.clock()
	pc := e.browser(chromeOnWindows)
	pc.setup()
	seen := func() int { return e.count(`SELECT last_seen_at FROM devices WHERE id=?`, pc.device()) }
	start := seen()
	clock.forward(20 * time.Minute)
	pc.do("GET", "/api/movies", nil)
	if seen() != start {
		t.Error("a request 20 minutes later wrote the device's row again")
	}
	clock.forward(50 * time.Minute)
	pc.do("GET", "/api/movies", nil)
	if got := seen(); got-start != 70*60 {
		t.Errorf("after 70 minutes the device was last seen %d seconds after the start", got-start)
	}
	// A browser that is updated describes itself differently, and one that
	// stops describing itself keeps the kind it had.
	clock.forward(2 * time.Hour)
	pc.ua = strings.Replace(chromeOnWindows, "Chrome/130.0.0.0", "Chrome/130.0.0.0 Edg/130.0.0.0", 1)
	pc.do("GET", "/api/movies", nil)
	clock.forward(2 * time.Hour)
	pc.ua = "curl/8"
	var d deviceView
	decode(t, pc.do("GET", "/api/device", nil), &d)
	if d.Label != "Windows PC, Edge" {
		t.Errorf("the device is now called %q", d.Label)
	}

	// 401 days on, a phone that has not been back is gone when the next
	// device is made. The PC, which has been back, stays.
	phone := e.signedIn(safariOnIPhone)
	e.must(`INSERT INTO lessons(id,device_id,kind,value,learned_at,expires_at) VALUES(?,?,'hdr','',?,?)`,
		ids.New(), phone.device(), clock.at.Unix(), clock.at.Add(500*day).Unix())
	clock.forward(401 * day)
	pc.do("GET", "/api/movies", nil)
	e.signedIn(firefoxOnLinux)
	if n := e.count(`SELECT count(*) FROM devices WHERE id=?`, phone.device()) + e.count(`SELECT count(*) FROM lessons WHERE device_id=?`, phone.device()); n != 0 {
		t.Errorf("%d rows are left of a device whose cookie ran out", n)
	}
	if n := e.count(`SELECT count(*) FROM devices WHERE id=?`, pc.device()); n != 1 {
		t.Error("a device that was seen yesterday was removed")
	}
}

// What kind of device a browser is. The answer is always one of the kinds
// made here: nothing from the browser's description is copied into it.
func TestTheKindOfDeviceABrowserIs(t *testing.T) {
	for userAgent, want := range map[string]string{
		chromeOnWindows: "Windows PC, Chrome",
		safariOnIPhone:  "iPhone",
		firefoxOnLinux:  "Linux PC, Firefox",
		"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1":   "iPad",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15":            "Mac, Safari",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36":            "Mac, Chrome",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0":    "Windows PC, Edge",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0":                                                 "Windows PC, Firefox",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8 Build/AP2A) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36": "Android phone",
		"Mozilla/5.0 (Linux; Android 13; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36":                   "Android tablet",
		"Mozilla/5.0 (SMART-TV; Linux; Tizen 7.0) AppleWebKit/537.36 (KHTML, like Gecko) 94.0.4606.31/7.0 TV Safari/537.36":                "TV browser",
		"Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/108.0.5359.211 Safari/537.36 WebAppManager":      "TV browser",
		"Mozilla/5.0 (Linux; Android 11; BRAVIA 4K VH2 Build/RTT2) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36":  "TV browser",
		"Mozilla/5.0 (Linux; Android 9; AFTKA) AppleWebKit/537.36 (KHTML, like Gecko) Silk/128.3.1 like Chrome/128.0.0.0 Safari/537.36":    "TV browser",
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36":                   "Chromebook, Chrome",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/130.0.0.0 Safari/537.36":                    "Linux PC, Chrome",
		"VLC/3.0.20 LibVLC/3.0.20": "Browser",
		"":                         "Browser",
		"Mozilla/5.0 (Windows NT 10.0) Living Room PC of Some Owner": "Windows PC",
	} {
		if got := deviceLabel(userAgent); got != want {
			t.Errorf("%q is called %q, want %q", userAgent, got, want)
		}
	}
}
