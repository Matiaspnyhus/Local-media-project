package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
)

// testClock is a clock the test moves by hand, in place of the PC's.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) pass(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func handClock(e *env) *testClock {
	c := &testClock{t: time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)}
	e.s.watch.now = c.now
	return c
}

// signIn signs the owner in from a device that calls itself userAgent.
func (e *env) signIn(userAgent string) *http.Cookie {
	e.t.Helper()
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"name":"owner","password":"a good password"}`))
	r.RemoteAddr, r.Host = remote, "192.168.1.10:8177"
	r.Header.Set("X-Requested-With", "localmedia")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", userAgent)
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	c := sessionCookie(w)
	if w.Code != 200 || c == nil {
		e.t.Fatalf("sign-in: %d %s", w.Code, w.Body)
	}
	return c
}

func (e *env) watching() []string {
	who := []string{}
	for _, w := range e.s.Watching() {
		who = append(who, w.Who)
	}
	return who
}

func (e *env) wantWatching(when string, want ...string) {
	e.t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := e.watching(); !reflect.DeepEqual(got, want) {
		e.t.Errorf("%s: watching = %q, want %q", when, got, want)
	}
}

const (
	iPhone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1"
	tv     = "Mozilla/5.0 (SMART-TV; Linux; Tizen 7.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/94.0 TV Safari/537.36"
	laptop = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

// What counts as watching, for a film that is sent as it is. (The rules are
// written out at the top of watching.go.)
func TestWhoIsWatching(t *testing.T) {
	e := newEnv(t)
	clock := handClock(e)
	owner := e.setup()
	e.wantWatching("straight after setup")

	phone := e.signIn(iPhone)
	film := e.firstItem(phone, "clip")
	e.do("GET", "/api/home", remote, phone, nil)
	e.wantWatching("a phone has signed in and looked through the library")

	// Opening a film is the first sign.
	var p playOut
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, phone, nil), &p)
	e.wantWatching("a film was opened on the phone", "owner on an iPhone")
	clock.pass(14 * time.Minute)
	e.wantWatching("14 minutes after the film was opened", "owner on an iPhone")
	clock.pass(2 * time.Minute)
	e.wantWatching("16 minutes after the film was opened, with nothing since")

	// A piece of the file, fetched with the link alone as a player does.
	r := httptest.NewRequest("GET", p.URL, nil)
	r.RemoteAddr = remote
	r.Header.Set("Range", "bytes=10-14")
	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, r)
	if rec.Code != 206 || rec.Body.String() != "abcde" {
		t.Fatalf("range request: %d %q", rec.Code, rec.Body)
	}
	e.wantWatching("a piece of the film was sent", "owner on an iPhone")
	clock.pass(16 * time.Minute)
	e.wantWatching("16 minutes after the last piece")

	// The player saving how far it has got: every ten seconds while playing.
	if w := e.do("PUT", "/api/items/"+film+"/progress", remote, phone, map[string]int64{"positionMs": 42000, "durationMs": 100000}); w.Code != 200 {
		t.Fatalf("progress: %d %s", w.Code, w.Body)
	}
	e.wantWatching("the player saved its position", "owner on an iPhone")

	// A second device; they are listed in a steady order, and each can be
	// left out of the list it asks for itself.
	big := e.signIn(tv)
	e.do("POST", "/api/items/"+film+"/play", remote, big, nil)
	if got := e.watching(); len(got) != 2 || !strings.Contains(strings.Join(got, "|"), "owner on a TV") || !strings.Contains(strings.Join(got, "|"), "owner on an iPhone") {
		t.Errorf("two devices are watching, but watching = %q", got)
	}
	var tvSignIn string
	for _, w := range e.s.Watching() {
		if w.Who == "owner on a TV" {
			tvSignIn = w.SignIn
		}
	}
	if got := e.s.watchingBesides(tvSignIn); !reflect.DeepEqual(got, []string{"owner on an iPhone"}) {
		t.Errorf("who is watching besides the TV = %q", got)
	}

	// Signing out ends it at once.
	e.do("POST", "/api/logout", remote, big, nil)
	e.wantWatching("the TV signed out", "owner on an iPhone")
	clock.pass(16 * time.Minute)
	e.wantWatching("16 minutes later")

	// What is not a sign: a link that is refused, and everything that is
	// not a film.
	if w := e.do("GET", p.URL+"x", remote, nil, nil); w.Code != 403 {
		t.Fatalf("a spoiled link answered %d", w.Code)
	}
	if w := e.do("GET", "/stream/"+strings.Repeat("0", 26)+"?e=99999999999&s="+tvSignIn+"&g=abc", remote, nil, nil); w.Code != 403 {
		t.Fatalf("a made-up link answered %d", w.Code)
	}
	e.do("GET", "/api/movies", remote, phone, nil)
	e.do("GET", "/api/update", local, owner, nil)
	e.do("GET", "/hls/"+strings.Repeat("ab", 16)+"/index.m3u8", remote, nil, nil)
	e.wantWatching("refused links and looking through the library")

	// A clock that is set back does not make a sign last for ever.
	e.do("PUT", "/api/items/"+film+"/progress", remote, phone, map[string]int64{"positionMs": 43000, "durationMs": 100000})
	clock.pass(-3 * time.Hour)
	e.wantWatching("the PC's clock was set back three hours", "owner on an iPhone")
	clock.pass(16 * time.Minute)
	e.wantWatching("16 minutes after the clock was set back")
}

func TestDevicesAreNamedInAWordOrTwo(t *testing.T) {
	for ua, want := range map[string]string{
		iPhone: "an iPhone",
		tv:     "a TV",
		laptop: "a Windows PC",
		"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15":                          "an iPad",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/130.0 Mobile Safari/537":  "an Android phone",
		"Mozilla/5.0 (Linux; Android 13; SM-X700) AppleWebKit/537.36 Chrome/130.0 Safari/537.36":      "an Android tablet",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.1 Safari":    "a Mac",
		"Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0":                      "a Linux PC",
		"Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 Chrome/79 Safari/537.36 WebAppManager": "a TV",
		"":                  "another device",
		"VLC/3.0.21 LibVLC": "another device",
	} {
		if got := deviceKind(ua); got != want {
			t.Errorf("%q is called %q, want %q", ua, got, want)
		}
	}
	if got := inWords([]string{"A"}) + "; " + inWords([]string{"A", "B"}) + "; " + inWords([]string{"A", "B", "C"}); got != "A; A and B; A, B and C" {
		t.Errorf("names in a sentence: %q", got)
	}
}

// Every route a player fetches film from must count, or a film could be
// playing with the server none the wiser. Players use the routes that need
// no cookie; if one is added, it has to be added to filmRoutes too.
func TestEveryRouteThatSendsFilmCounts(t *testing.T) {
	e := newEnv(t)
	inTable := map[string]bool{}
	for _, rt := range e.s.Routes() {
		inTable[rt.Pattern] = true
		if rt.Policy == SignedLink && !filmRoutes[rt.Pattern] {
			t.Errorf("%s is fetched by players, but using it does not count as watching", rt.Pattern)
		}
	}
	for pattern := range filmRoutes {
		if !inTable[pattern] {
			t.Errorf("%s counts as watching but is not in the route table (was it renamed?)", pattern)
		}
	}
}

// fileSender stands in for the real writer of an answer, which can be
// handed a file to send by the quickest way the system has.
type fileSender struct {
	*httptest.ResponseRecorder
	source io.Reader // what every piece must be read from, directly
	pieces []int64
	shape  bool // set when something other than a limit on source arrived
	fail   error
}

func (f *fileSender) ReadFrom(src io.Reader) (int64, error) {
	lr, ok := src.(*io.LimitedReader)
	if !ok || lr.R != f.source {
		f.shape = true
		return 0, errors.New("not in the form that can be sent quickly")
	}
	f.pieces = append(f.pieces, lr.N)
	n, err := io.Copy(f.ResponseRecorder.Body, src)
	if err == nil {
		err = f.fail
	}
	return n, err
}

// A film sent as it is, is one long answer. It must be handed on in pieces
// so that each piece that goes out counts as a sign, in the form the quick
// way of sending a file recognises, and without a byte going astray.
func TestAFilmIsSentInPiecesThatEachCount(t *testing.T) {
	old := streamPiece
	streamPiece = 1000
	defer func() { streamPiece = old }()
	film := bytes.Repeat([]byte("0123456789abcdefghijklmnopqrstuvwxyz"), 300) // 10,800 bytes

	send := func(status int, fail error) (*fileSender, int, int64, error) {
		source := bytes.NewReader(film)
		f := &fileSender{ResponseRecorder: httptest.NewRecorder(), source: source, fail: fail}
		signs := 0
		w := &watchedWriter{ResponseWriter: f, sign: func() { signs++ }}
		w.WriteHeader(status)
		// This is the call http.ServeContent makes.
		n, err := io.CopyN(w, source, int64(len(film)))
		return f, signs, n, err
	}

	f, signs, n, err := send(http.StatusPartialContent, nil)
	if err != nil || n != int64(len(film)) || !bytes.Equal(f.Body.Bytes(), film) {
		t.Fatalf("sent %d of %d bytes (%v), or not the same bytes", n, len(film), err)
	}
	if f.shape || len(f.pieces) != 11 || f.pieces[0] != 1000 || f.pieces[10] != 800 {
		t.Errorf("the film was handed on as pieces of %v (wrong form: %v); want ten of 1000 and one of 800, each a limit on the file itself", f.pieces, f.shape)
	}
	if signs != 12 {
		t.Errorf("%d signs of watching; want one for the answer beginning and one for each of the 11 pieces", signs)
	}

	// An answer that is a refusal is never a sign, whatever is written.
	if _, signs, _, _ := send(http.StatusForbidden, nil); signs != 0 {
		t.Errorf("a refused request gave %d signs of watching", signs)
	}

	// A connection that breaks stops the sending, and says how far it got.
	broken := errors.New("the viewer went away")
	f, _, n, err = send(http.StatusOK, broken)
	if !errors.Is(err, broken) || n != 1000 || len(f.pieces) != 1 {
		t.Errorf("after the connection broke: sent %d in pieces %v, error %v", n, f.pieces, err)
	}

	// A file that turns out shorter than promised ends the answer; it must
	// not be asked for more for ever.
	short := bytes.NewReader(film[:2500])
	f = &fileSender{ResponseRecorder: httptest.NewRecorder(), source: short}
	w := &watchedWriter{ResponseWriter: f, sign: func() {}}
	if n, err := io.CopyN(w, short, 10000); n != 2500 || err != io.EOF {
		t.Errorf("a file cut short: sent %d, error %v; want 2500 and the end of the file", n, err)
	}

	// A writer that cannot be handed a file is written to in the usual way.
	rec := httptest.NewRecorder()
	signs = 0
	w = &watchedWriter{ResponseWriter: rec, sign: func() { signs++ }}
	if n, err := io.CopyN(w, bytes.NewReader(film), int64(len(film))); err != nil || n != int64(len(film)) || !bytes.Equal(rec.Body.Bytes(), film) || signs == 0 {
		t.Errorf("written the usual way: %d bytes, error %v, %d signs", n, err, signs)
	}
}

// The same over a real connection, where the system's quick way of sending
// a file is really used: the whole film and a part of it arrive exactly.
func TestAFilmArrivesWholeOverARealConnection(t *testing.T) {
	old := streamPiece
	streamPiece = 64 << 10
	defer func() { streamPiece = old }()
	e := newEnv(t)
	film := make([]byte, 1<<20+12345)
	for i := range film {
		film[i] = byte(i * 7 >> 3)
	}
	if err := os.WriteFile(filepath.Join(e.media, "Long Film (2019).mp4"), film, 0o644); err != nil {
		t.Fatal(err)
	}
	c := e.setup()
	clock := handClock(e)
	var p playOut
	decode(t, e.do("POST", "/api/items/"+e.firstItem(c, "Long Film")+"/play", remote, c, nil), &p)
	clock.pass(time.Hour)
	e.wantWatching("an hour after the film was opened, before any of it is fetched")

	srv := httptest.NewServer(e.s)
	defer srv.Close()
	get := func(byteRange string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+p.URL, nil)
		if byteRange != "" {
			req.Header.Set("Range", byteRange)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, b
	}
	if code, got := get(""); code != 200 || !bytes.Equal(got, film) {
		t.Fatalf("the whole film: answer %d, %d bytes of %d, same bytes: %v", code, len(got), len(film), bytes.Equal(got, film))
	}
	if code, got := get("bytes=700000-"); code != 206 || !bytes.Equal(got, film[700000:]) {
		t.Fatalf("the film from byte 700000: answer %d, %d bytes of %d, same bytes: %v", code, len(got), len(film)-700000, bytes.Equal(got, film[700000:]))
	}
	if code, got := get("bytes=5-70004"); code != 206 || !bytes.Equal(got, film[5:70005]) {
		t.Fatalf("a part in the middle: answer %d, %d bytes", code, len(got))
	}
	e.wantWatching("the film was fetched", "owner on another device")
}

// A repackaged film that is paused asks for nothing, and would not survive
// a restart. It counts as being watched for as long as it stays open, up
// to four hours after it last asked for a chunk.
func TestAPausedRepackagedFilmIsStillBeingWatched(t *testing.T) {
	e := newConvertEnv(t)
	clock := handClock(e)
	e.setup()
	e.s.InspectNow()
	phone := e.signIn(laptop)
	film := e.firstItem(phone, "Test Film")
	chrome := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true}, "hls": true}
	if w := e.do("POST", "/api/capabilities", remote, phone, chrome); w.Code != 200 {
		t.Fatal(w.Body)
	}
	open := func() playAnswer {
		t.Helper()
		var p playAnswer
		decode(t, e.do("POST", "/api/items/"+film+"/play", remote, phone, nil), &p)
		if p.HLS == "" || !strings.HasPrefix(p.URL, "/hls/") {
			t.Fatalf("the film was not repackaged: %+v", p)
		}
		base := strings.TrimSuffix(p.URL, "index.m3u8")
		for _, name := range []string{"index.m3u8", "init.mp4", "seg-0.m4s"} {
			if w := e.do("GET", base+name, remote, nil, nil); w.Code != 200 {
				t.Fatalf("%s: %d %s", name, w.Code, w.Body)
			}
		}
		return p
	}

	p := open()
	e.wantWatching("a repackaged film is playing", "owner on a Windows PC")
	clock.pass(16 * time.Minute)
	e.wantWatching("paused for 16 minutes with the page left open", "owner on a Windows PC")
	clock.pass(3*time.Hour + 40*time.Minute)
	e.wantWatching("paused for just under four hours", "owner on a Windows PC")
	clock.pass(10 * time.Minute)
	e.wantWatching("left alone for more than four hours (a tab forgotten on a finished film)")

	// Asking for a chunk again is watching again.
	base := strings.TrimSuffix(p.URL, "index.m3u8")
	if w := e.do("GET", base+"seg-1.m4s", remote, nil, nil); w.Code != 200 {
		t.Fatalf("seg-1: %d %s", w.Code, w.Body)
	}
	e.wantWatching("the film was carried on with", "owner on a Windows PC")

	// Closing the player ends the long protection: what is left is the 15
	// minutes that any film has.
	clock.pass(16 * time.Minute)
	if w := e.do("POST", "/api/hls/"+p.HLS+"/stop", remote, phone, nil); w.Code != 200 {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	e.wantWatching("the player was closed 16 minutes after its last chunk")

	// A paused film whose sign-in is signed out is nobody's.
	open()
	clock.pass(16 * time.Minute)
	e.wantWatching("a second film, paused for 16 minutes", "owner on a Windows PC")
	e.do("POST", "/api/logout", remote, phone, nil)
	e.wantWatching("its sign-in was signed out")
}

// fakeUpdater stands in for the part of the program that installs updates.
type fakeUpdater struct {
	status    UpdateStatus
	installed chan struct{}
}

func (f *fakeUpdater) Status() UpdateStatus               { return f.status }
func (f *fakeUpdater) Check(context.Context) UpdateStatus { return f.status }
func (f *fakeUpdater) Undo() error                        { return nil }
func (f *fakeUpdater) Install() error                     { f.installed <- struct{}{}; return nil }
func (f *fakeUpdater) wasAsked(within time.Duration) bool {
	select {
	case <-f.installed:
		return true
	case <-time.After(within):
		return false
	}
}

// The owner may install an update whenever they like, but is told first
// whose film it can stop, and has to say "anyway".
func TestInstallingNowNamesWhoIsWatching(t *testing.T) {
	data, media := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(media, "clip.mp4"), []byte("0123456789abcdefghij"), 0o644)
	d, err := db.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	upd := &fakeUpdater{status: UpdateStatus{Supported: true, Current: "v1.0.0", Available: "v1.1.0", Waiting: true}, installed: make(chan struct{}, 4)}
	s, err := New(Config{DB: d, DataDir: data, Version: "v1.0.0", Port: 8177, Assets: fstest.MapFS{}, Update: upd})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := &env{t: t, s: s, media: media}
	owner := e.setup()
	film := e.firstItem(owner, "clip")

	// The owner has been watching on this very device: nobody else is, so
	// there is nobody to name.
	e.do("PUT", "/api/items/"+film+"/progress", local, owner, map[string]int64{"positionMs": 1000, "durationMs": 100000})
	if w := e.do("POST", "/api/update/install", local, owner, nil); w.Code != 202 || !upd.wasAsked(30*time.Second) {
		t.Fatalf("with only the owner's own device watching, install answered %d %s", w.Code, w.Body)
	}

	// Now a phone and a TV are watching.
	phone, big := e.signIn(iPhone), e.signIn(tv)
	e.do("PUT", "/api/items/"+film+"/progress", remote, phone, map[string]int64{"positionMs": 1000, "durationMs": 100000})
	var st struct {
		Waiting  bool
		Watching []string
	}
	decode(t, e.do("GET", "/api/update", local, owner, nil), &st)
	if !st.Waiting || len(st.Watching) != 2 {
		t.Errorf("the settings page is told waiting=%v watching=%q; want the wait and both the owner's device and the phone", st.Waiting, st.Watching)
	}
	w := e.do("POST", "/api/update/install", local, owner, nil)
	var refusal struct{ Error string }
	decode(t, w, &refusal)
	if w.Code != 409 || refusal.Error != "owner on an iPhone is watching right now. Installing restarts Local Media, which can stop what they are watching." {
		t.Fatalf("install with a phone watching answered %d %q", w.Code, refusal.Error)
	}
	if upd.wasAsked(200 * time.Millisecond) {
		t.Fatal("the update was installed without the owner being told who is watching")
	}
	e.do("POST", "/api/items/"+film+"/play", remote, big, nil)
	w = e.do("POST", "/api/update/install", local, owner, nil)
	decode(t, w, &refusal)
	if w.Code != 409 || !strings.Contains(refusal.Error, " and ") || !strings.Contains(refusal.Error, " are watching right now.") {
		t.Fatalf("install with two devices watching answered %d %q", w.Code, refusal.Error)
	}

	// Told, the owner may go ahead.
	if w := e.do("POST", "/api/update/install?anyway=1", local, owner, nil); w.Code != 202 || !upd.wasAsked(30*time.Second) {
		t.Fatalf("install anyway answered %d %s", w.Code, w.Body)
	}
}
