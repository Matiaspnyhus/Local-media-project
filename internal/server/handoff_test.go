package server

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
)

// No test may start a real video player on the machine it runs on. A test
// that wants to see what would be opened puts a recorder here (recordOpens).
func init() {
	openInPlayer = func(string) error { return errors.New("a test reached the real video player") }
}

// recordOpens notes every file the server asks to have opened, and opens
// none.
func recordOpens(t *testing.T) *[]string {
	t.Helper()
	opened := []string{}
	before := openInPlayer
	openInPlayer = func(path string) error {
		opened = append(opened, path)
		return nil
	}
	t.Cleanup(func() { openInPlayer = before })
	return &opened
}

// homeAt gives the server these home addresses, whatever network the
// machine running the test is on.
func homeAt(t *testing.T, urls ...string) {
	t.Helper()
	before := homeAddresses
	homeAddresses = func(*Server) []string { return urls }
	t.Cleanup(func() { homeAddresses = before })
}

const (
	pcHost    = "localhost:8177"    // what the browser on the PC itself calls the server
	phoneHost = "192.168.1.10:8177" // what another device at home calls it
)

// ask sends one request with the headers a test chooses. From the page, a
// write always carries X-Requested-With and the page's own Origin.
func (e *env) ask(method, target, from, host string, cookie *http.Cookie, headers map[string]string, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.RemoteAddr, r.Host = from, host
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

// fromPage is what the page's own script sends with a write.
func fromPage(host string) map[string]string {
	return map[string]string{"X-Requested-With": "localmedia", "Content-Type": "application/json", "Origin": "http://" + host}
}

func (e *env) fileOf(itemID string) string {
	e.t.Helper()
	var id string
	if err := e.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, itemID).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestTheNewWaysOutNeedASignIn(t *testing.T) {
	e := newEnv(t)
	e.setup()
	want := map[string]bool{"POST /api/items/{id}/open-local": true, "GET /api/items/{id}/handoff": true, "GET /api/items/{id}/handoff.xspf": true}
	for _, rt := range e.s.Routes() {
		key := rt.Method + " " + rt.Pattern
		if !want[key] {
			continue
		}
		delete(want, key)
		if rt.Policy != Session {
			t.Errorf("%s has policy %s, want session", key, rt.Policy)
		}
		target := strings.Replace(rt.Pattern, "{id}", ids.New(), 1)
		for _, from := range []string{remote, local} {
			if w := e.ask(rt.Method, target, from, pcHost, nil, fromPage(pcHost), "{}"); w.Code != 401 {
				t.Errorf("%s from %s without sign-in answered %d: %s", key, from, w.Code, w.Body)
			}
		}
	}
	for key := range want {
		t.Errorf("route %s is gone", key)
	}
}

func TestOpenLocalOnlyFromTheBrowserOnThePC(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	opened := recordOpens(t)
	id := e.firstItem(c, "clip")
	target := "/api/items/" + id + "/open-local"

	// Another device at home, signed in as the same person.
	w := e.ask("POST", target, remote, phoneHost, c, fromPage(phoneHost), "{}")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "from the browser on that PC") {
		t.Errorf("from another device: %d %s", w.Code, w.Body)
	}
	// It cannot talk its way in: only the real connection counts.
	claims := fromPage(pcHost)
	claims["X-Forwarded-For"], claims["X-Real-IP"], claims["Forwarded"] = "127.0.0.1", "127.0.0.1", "for=127.0.0.1"
	if w := e.ask("POST", target, remote, pcHost, c, claims, "{}"); w.Code != 403 {
		t.Errorf("another device claiming to be the PC: %d %s", w.Code, w.Body)
	}
	// On the PC, but sent by another website open in its browser.
	foreign := fromPage(pcHost)
	foreign["Origin"] = "http://evil.example"
	if w := e.ask("POST", target, local, pcHost, c, foreign, "{}"); w.Code != 403 {
		t.Errorf("from another website on the PC: %d %s", w.Code, w.Body)
	}
	// On the PC, but not sent by the page's own script.
	if w := e.ask("POST", target, local, pcHost, c, map[string]string{"Content-Type": "application/json"}, "{}"); w.Code != 403 {
		t.Errorf("without the page's header: %d %s", w.Code, w.Body)
	}
	// Reading does not open anything either.
	if w := e.ask("GET", target, local, pcHost, c, nil, ""); w.Code == 200 {
		t.Errorf("a GET was answered: %d %s", w.Code, w.Body)
	}
	if len(*opened) != 0 {
		t.Fatalf("a refused request opened %q", *opened)
	}

	// The browser on the PC, showing the server's own page.
	if w := e.ask("POST", target, local, pcHost, c, fromPage(pcHost), "{}"); w.Code != 200 {
		t.Fatalf("from the PC: %d %s", w.Code, w.Body)
	}
	want := filepath.Join(e.media, "clip.mp4")
	if len(*opened) != 1 || (*opened)[0] != want {
		t.Fatalf("opened %q, want exactly %q", *opened, want)
	}

	// Whatever the request says, the file is the one the database names
	// for the item in the address.
	*opened = (*opened)[:0]
	other := e.firstItem(c, "Some Movie")
	said := `{"path":"/etc/passwd","file":"..\\..\\secret.mkv","id":"` + id + `","args":["--fullscreen","; rm -rf /"],"player":"cmd.exe"}`
	w = e.ask("POST", "/api/items/"+other+"/open-local?path=/etc/passwd&file=clip.mp4&id="+id, local, pcHost, c, fromPage(pcHost), said)
	if w.Code != 200 {
		t.Fatalf("from the PC, with a body: %d %s", w.Code, w.Body)
	}
	want = filepath.Join(e.media, "Films", "Some.Movie.mkv")
	if len(*opened) != 1 || (*opened)[0] != want {
		t.Fatalf("opened %q, want exactly %q", *opened, want)
	}
}

// "The PC's own browser" is a connection from the PC, to the PC's address
// for itself, that nobody passed on. A page under a foreign name that
// points at the PC arrives from the PC too; so does everything a tunnel or
// a proxy on the PC lets in from outside.
func TestOnlyThePCsOwnAddressCountsAsThePC(t *testing.T) {
	namedPC(t, "MediaPC")
	e := newEnv(t)
	c := e.setup()
	homeAt(t, "http://"+phoneHost)
	opened := recordOpens(t)
	id := e.firstItem(c, "clip")
	target := "/api/items/" + id + "/open-local"
	with := func(host string, more map[string]string) map[string]string {
		h := fromPage(host)
		for k, v := range more {
			h[k] = v
		}
		return h
	}

	// A foreign name that points at this computer. Its page cannot even
	// sign in, and the owner's own sign-in would not help it.
	const evil = "evil.example:8177"
	w := e.ask("POST", "/api/login", local, evil, nil, fromPage(evil), `{"name":"owner","password":"a good password"}`)
	if w.Code != 403 || sessionCookie(w) != nil {
		t.Errorf("sign-in under a foreign name, from the PC: %d %s", w.Code, w.Body)
	}
	if w := e.ask("POST", target, local, evil, c, fromPage(evil), "{}"); w.Code != 403 {
		t.Errorf("open-local under a foreign name, from the PC: %d %s", w.Code, w.Body)
	}
	if w := e.ask("GET", "/api/items/"+id+"/handoff", local, evil, c, map[string]string{"Origin": "http://" + evil}, ""); w.Code != 403 || strings.Contains(w.Body.String(), "thisPC") || strings.Contains(w.Body.String(), "/stream/") {
		t.Errorf("handoff under a foreign name, from the PC: %d %s", w.Code, w.Body)
	}

	// From the PC, to one of the server's own addresses, but not the PC's
	// address for itself; or passed on by something.
	for _, k := range []struct {
		name, host string
		more       map[string]string
	}{
		{"the PC's home address (a port passed on by a program on the PC)", phoneHost, nil},
		{"the PC's own name", "mediapc.local:8177", nil},
		{"the PC's own short name", "mediapc:8177", nil},
		{"passed on: X-Forwarded-For", pcHost, map[string]string{"X-Forwarded-For": "203.0.113.9"}},
		{"passed on: X-Forwarded-For that claims to be the PC", pcHost, map[string]string{"X-Forwarded-For": "127.0.0.1"}},
		{"passed on: Forwarded", pcHost, map[string]string{"Forwarded": "for=203.0.113.9;host=localhost"}},
		{"passed on: X-Forwarded-Host", pcHost, map[string]string{"X-Forwarded-Host": "films.example.com"}},
		{"passed on: X-Forwarded-Proto", pcHost, map[string]string{"X-Forwarded-Proto": "https"}},
		{"passed on: X-Real-IP", pcHost, map[string]string{"X-Real-IP": "203.0.113.9"}},
		{"passed on: Via", pcHost, map[string]string{"Via": "1.1 tunnel"}},
		{"passed on: any other X-Forwarded header", pcHost, map[string]string{"x-forwarded-port": "443"}},
		{"passed on: a tunnel's own header", pcHost, map[string]string{"CF-Connecting-IP": "203.0.113.9"}},
		{"passed on: True-Client-IP", pcHost, map[string]string{"True-Client-IP": "203.0.113.9"}},
		{"passed on: an empty X-Forwarded-For", pcHost, map[string]string{"X-Forwarded-For": ""}},
	} {
		if w := e.ask("POST", target, local, k.host, c, with(k.host, k.more), "{}"); w.Code != 403 || !strings.Contains(w.Body.String(), "from the browser on that PC") {
			t.Errorf("%s: open-local answered %d %s", k.name, w.Code, w.Body)
		}
		get := map[string]string{"Origin": "http://" + k.host}
		for key, v := range k.more {
			get[key] = v
		}
		w := e.ask("GET", "/api/items/"+id+"/handoff", local, k.host, c, get, "")
		var h handoffAnswer
		decode(t, w, &h)
		if w.Code != 200 || h.ThisPC || h.URL == "" {
			t.Errorf("%s: handoff answered %d %+v, want the link and the file for VLC, as for any other device", k.name, w.Code, h)
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("a request that was not the PC's own browser opened %q", *opened)
	}

	// The PC's address for itself, in every spelling a browser uses.
	for _, k := range []struct{ from, host string }{
		{local, "localhost:8177"}, {local, "LOCALHOST:8177"}, {local, "127.0.0.1:8177"}, {"[::1]:50000", "[::1]:8177"},
		{"[::1]:50000", "localhost:8177"}, {local, "localhost"}, {"127.0.0.2:50000", "127.0.0.2:8177"},
	} {
		*opened = (*opened)[:0]
		var h handoffAnswer
		decode(t, e.ask("GET", "/api/items/"+id+"/handoff", k.from, k.host, c, nil, ""), &h)
		w := e.ask("POST", target, k.from, k.host, c, fromPage(k.host), "{}")
		if !h.ThisPC || w.Code != 200 || len(*opened) != 1 {
			t.Errorf("from %s to %s: handoff says this PC %v; open-local %d %s; opened %q", k.from, k.host, h.ThisPC, w.Code, w.Body, *opened)
		}
		later(e, time.Minute)
	}
}

func TestOpenLocalOpensOnlyVideosInsideTheLibrary(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	opened := recordOpens(t)
	id := e.firstItem(c, "clip")
	open := func(item string) *httptest.ResponseRecorder {
		return e.ask("POST", "/api/items/"+item+"/open-local", local, pcHost, c, fromPage(pcHost), "{}")
	}
	if w := open(ids.New()); w.Code != 404 {
		t.Errorf("an item that does not exist: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"nope", "..%2F..%2Fetc%2Fpasswd", strings.Repeat("a", 31), strings.ToUpper(id)} {
		if w := open(bad); w.Code != 404 {
			t.Errorf("item %q: %d %s", bad, w.Code, w.Body)
		}
	}

	// Things a file's row could come to point at that must never be handed
	// to the operating system to "open".
	outside := filepath.Join(filepath.Dir(e.media), "outside-"+ids.New()+".mkv")
	os.WriteFile(outside, []byte("private"), 0o644)
	defer os.Remove(outside)
	os.WriteFile(filepath.Join(e.media, "notes.txt"), []byte("text"), 0o644)
	os.WriteFile(filepath.Join(e.media, "run.bat"), []byte("echo"), 0o644)
	os.WriteFile(filepath.Join(e.media, "film.mkv.exe"), []byte("MZ"), 0o644)
	os.MkdirAll(filepath.Join(e.media, "folder.mkv"), 0o755)
	rels := []string{"notes.txt", "run.bat", "film.mkv.exe", "folder.mkv", "gone.mkv", "../" + filepath.Base(outside), outside, "Films/../../" + filepath.Base(outside), ""}
	if os.Symlink(outside, filepath.Join(e.media, "link.mkv")) == nil { // not everywhere: Windows wants a special right for it
		rels = append(rels, "link.mkv")
	}
	for _, rel := range rels {
		if _, err := e.s.db.Exec(`UPDATE files SET rel_path=? WHERE version_id IN (SELECT id FROM versions WHERE item_id=?)`, rel, id); err != nil {
			t.Fatal(err)
		}
		if w := open(id); w.Code != 404 {
			t.Errorf("a file row pointing at %q: %d %s", rel, w.Code, w.Body)
		}
		for _, way := range []string{"/handoff", "/handoff.xspf"} {
			if w := e.do("GET", "/api/items/"+id+way, remote, c, nil); w.Code != 404 {
				t.Errorf("%s for a file row pointing at %q: %d %s", way, rel, w.Code, w.Body)
			}
		}
	}
	// A link put where a film was. The scan lists no links, so this takes
	// somebody who can write into the library after the scan. Inside the
	// library a link's bytes may be sent like any film's, but the link is
	// never handed to the operating system: what "open" does then is
	// decided by what it points at.
	os.WriteFile(filepath.Join(e.media, "run.command"), []byte("#!/bin/sh\necho run\n"), 0o755)
	os.MkdirAll(filepath.Join(e.media, "Real"), 0o755)
	os.WriteFile(filepath.Join(e.media, "Real", "inside.mkv"), []byte("mkvdata"), 0o644)
	links := map[string]string{ // the link's place in the library: what it points at
		"script.mkv":        "run.command",
		"batch.mp4":         "run.bat",
		"other-film.mkv":    filepath.Join("Films", "Some.Movie.mkv"),
		"full-path.mkv":     filepath.Join(e.media, "Films", "Some.Movie.mkv"),
		"Linked":            "Real", // a folder that is a link: the film in it is real
		"link-to-link.mkv":  "script.mkv",
		"Films/climbs.mkv":  filepath.Join("..", "run.command"),
		"Films/dangles.mkv": "nothing-there.mkv",
	}
	linked := []string{}
	for at, to := range links {
		if os.Symlink(to, filepath.Join(e.media, filepath.FromSlash(at))) == nil { // not everywhere, see above
			if at == "Linked" {
				at = "Linked/inside.mkv"
			}
			linked = append(linked, at)
		}
	}
	for _, rel := range linked {
		if _, err := e.s.db.Exec(`UPDATE files SET rel_path=? WHERE version_id IN (SELECT id FROM versions WHERE item_id=?)`, rel, id); err != nil {
			t.Fatal(err)
		}
		if w := open(id); w.Code != 404 {
			t.Errorf("a film that is reached through a link (%q): %d %s", rel, w.Code, w.Body)
		}
	}
	// The same film by its real place is opened (and a name with ".." in
	// it, which no scan writes, is not).
	e.s.db.Exec(`UPDATE files SET rel_path='Films/../clip.mp4' WHERE version_id IN (SELECT id FROM versions WHERE item_id=?)`, id)
	if w := open(id); w.Code != 404 {
		t.Errorf("a file row with .. in it: %d %s", w.Code, w.Body)
	}
	if len(*opened) != 0 {
		t.Fatalf("opened %q", *opened)
	}
	e.s.db.Exec(`UPDATE files SET rel_path='Real/inside.mkv' WHERE version_id IN (SELECT id FROM versions WHERE item_id=?)`, id)
	if w := open(id); w.Code != 200 || len(*opened) != 1 || (*opened)[0] != filepath.Join(e.media, "Real", "inside.mkv") {
		t.Fatalf("the film in its real folder: %d %s, opened %q", w.Code, w.Body, *opened)
	}
	*opened = (*opened)[:0]
	later(e, time.Minute)

	// A file the library has marked as gone.
	e.s.db.Exec(`UPDATE files SET rel_path='clip.mp4', missing=1 WHERE version_id IN (SELECT id FROM versions WHERE item_id=?)`, id)
	if w := open(id); w.Code != 404 {
		t.Errorf("a file marked as missing: %d %s", w.Code, w.Body)
	}
	if len(*opened) != 0 {
		t.Fatalf("opened %q", *opened)
	}
	e.s.db.Exec(`UPDATE files SET missing=0 WHERE version_id IN (SELECT id FROM versions WHERE item_id=?)`, id)
	if w := open(id); w.Code != 200 || len(*opened) != 1 {
		t.Fatalf("the film itself, put back: %d %s, opened %q", w.Code, w.Body, *opened)
	}
}

// later moves the clock by which the server limits how often the PC's
// video player is started.
func later(e *env, d time.Duration) {
	e.s.opens.mu.Lock()
	defer e.s.opens.mu.Unlock()
	now := e.s.opens.now
	e.s.opens.now = func() time.Time { return now().Add(d) }
}

// stopClock makes that clock stand still, so that a test of the limits
// gives the same answers on a slow machine; later still moves it.
func stopClock(e *env) {
	e.s.opens.mu.Lock()
	defer e.s.opens.mu.Unlock()
	at := time.Now()
	e.s.opens.now = func() time.Time { return at }
}

// One press of the button starts the PC's video player once. A key held
// down, an impatient finger or a script must not start it again and again.
func TestThePlayerIsNotStartedOverAndOver(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"Second.Film.mkv", "Third.Film.mkv", "Fourth.Film.mkv"} {
		os.WriteFile(filepath.Join(e.media, "Films", name), []byte("mkvdata"), 0o644)
	}
	c := e.setup()
	stopClock(e)
	var mu sync.Mutex
	var started []string
	hold := make(chan struct{}) // closed: the operating system answers at once
	close(hold)
	before := openInPlayer
	openInPlayer = func(path string) error {
		mu.Lock()
		started = append(started, filepath.Base(path))
		wait := hold
		mu.Unlock()
		<-wait
		return nil
	}
	t.Cleanup(func() { openInPlayer = before })
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(started)
	}
	open := func(item string) *httptest.ResponseRecorder {
		return e.ask("POST", "/api/items/"+item+"/open-local", local, pcHost, c, fromPage(pcHost), "{}")
	}
	held := func(w *httptest.ResponseRecorder, words string) bool {
		t.Helper()
		var said map[string]string
		decode(t, w, &said)
		wait, err := strconv.Atoi(w.Header().Get("Retry-After"))
		return w.Code == 429 && strings.Contains(said["error"], words) && err == nil && wait >= 1 && wait <= 10
	}
	clip, second, third, fourth := e.firstItem(c, "clip"), e.firstItem(c, "Second Film"), e.firstItem(c, "Third Film"), e.firstItem(c, "Fourth Film")

	// A hundred requests for one film at the same moment, as a held key or
	// a script sends them.
	answers := together(100, func(int) *httptest.ResponseRecorder { return open(clip) })
	ok := 0
	for _, w := range answers {
		if w.Code == 200 {
			ok++
		} else if !held(w, "was opened in this PC's video player a moment ago") {
			t.Errorf("a request that was held back answered %d %q %s", w.Code, w.Header().Get("Retry-After"), w.Body)
		}
	}
	if ok != 1 || count() != 1 {
		t.Fatalf("100 requests at once: %d were told the film was opened, and the player was started %d times; want once", ok, count())
	}
	// A second press a moment later, and a third.
	for i := 0; i < 2; i++ {
		if w := open(clip); !held(w, "a moment ago") || count() != 1 {
			t.Fatalf("pressed again at once: %d %s, player started %d times", w.Code, w.Body, count())
		}
	}
	// Refused requests cost nothing: they are not openings.
	for i := 0; i < 20; i++ {
		open(ids.New())
		e.ask("POST", "/api/items/"+second+"/open-local", remote, phoneHost, c, fromPage(phoneHost), "{}")
	}
	// Other films may follow, but only a few in a short while.
	for i, item := range []string{second, third} {
		if w := open(item); w.Code != 200 || count() != i+2 {
			t.Fatalf("film %d within the same few seconds: %d %s, player started %d times", i+2, w.Code, w.Body, count())
		}
	}
	if w := open(fourth); !held(w, "asked to open films several times") || count() != 3 {
		t.Fatalf("a fourth film within the same few seconds: %d %s, player started %d times", w.Code, w.Body, count())
	}
	// A hundred requests across all the films change nothing either.
	together(100, func(i int) *httptest.ResponseRecorder { return open([]string{clip, second, third, fourth}[i%4]) })
	if count() != 3 {
		t.Fatalf("a burst across four films started the player %d times in all, want 3", count())
	}

	// Once the same-film wait has passed, the film may be opened again
	// (the viewer closed the player and wants it back); the few-in-a-
	// short-while limit still stands until its own time has passed.
	later(e, sameFilmWait)
	if w := open(clip); !held(w, "asked to open films several times") {
		t.Fatalf("after %s, with three films just opened: %d %s", sameFilmWait, w.Code, w.Body)
	}
	later(e, openWindow)
	if w := open(clip); w.Code != 200 || count() != 4 {
		t.Fatalf("after the wait: %d %s, player started %d times", w.Code, w.Body, count())
	}

	// While the operating system has not yet answered for one film, no
	// other is handed to it.
	later(e, time.Minute)
	mu.Lock()
	hold = make(chan struct{})
	mu.Unlock()
	slow := make(chan *httptest.ResponseRecorder, 1)
	go func() { slow <- open(second) }()
	for i := 0; count() != 5; i++ { // until the first one is with the operating system
		if i > 6000 {
			t.Fatal("the film was never handed to the operating system")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w := open(third); !held(w, "still opening another film") || count() != 5 {
		t.Errorf("a second film while the first is still being opened: %d %s, player started %d times", w.Code, w.Body, count())
	}
	close(hold)
	if w := <-slow; w.Code != 200 {
		t.Errorf("the film that was being opened: %d %s", w.Code, w.Body)
	}
	if w := open(third); w.Code != 200 || count() != 6 {
		t.Errorf("once the first has been opened: %d %s, player started %d times", w.Code, w.Body, count())
	}

	// Every start is in the log, with who asked and which film, and
	// without the film's folder. What was held back is not there as a start.
	written, _ := os.ReadFile(e.s.log.path)
	log := string(written)
	if n := strings.Count(log, "owner is opening "); n != 6 {
		t.Errorf("the log has %d lines for 6 starts:\n%s", n, log)
	}
	for _, line := range []string{"owner is opening clip.mp4 in this PC's video player", "owner is opening Second.Film.mkv in this PC's video player", "owner is opening Third.Film.mkv in this PC's video player"} {
		if !strings.Contains(log, line) {
			t.Errorf("the log lacks %q:\n%s", line, log)
		}
	}
	if strings.Contains(log, e.media) || strings.Contains(log, "Fourth.Film") {
		t.Errorf("the log names the films' folder, or a film that was never opened:\n%s", log)
	}
}

func TestAPlayerThatWillNotStartIsSaidInPlainWords(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	id := e.firstItem(c, "clip")
	path := filepath.Join(e.media, "clip.mp4")
	before := openInPlayer
	openInPlayer = func(p string) error { return errors.New(`exec: "xdg-open" ` + p + `: exit status 3`) }
	t.Cleanup(func() { openInPlayer = before })
	w := e.ask("POST", "/api/items/"+id+"/open-local", local, pcHost, c, fromPage(pcHost), "{}")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "could not open the film in a video player") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), e.media) || strings.Contains(w.Body.String(), "xdg-open") {
		t.Errorf("the answer quotes the system: %s", w.Body)
	}
	// The log file itself, not the tidied copy the support bundle gets.
	written, _ := os.ReadFile(e.s.log.path)
	log := string(written)
	if !strings.Contains(log, "could not open clip.mp4") || !strings.Contains(log, "exit status 3") {
		t.Errorf("the log does not say what happened: %s", log)
	}
	if strings.Contains(log, path) || strings.Contains(log, e.media) {
		t.Errorf("the log names the film's folder: %s", log)
	}
	// A try that failed does not hold the film back: the viewer may press
	// again as soon as they have put right what was wrong. It is still one
	// of the few tries the PC allows in a short while.
	stopClock(e)
	later(e, time.Minute)
	for i := 1; i <= openMost; i++ {
		if w := e.ask("POST", "/api/items/"+id+"/open-local", local, pcHost, c, fromPage(pcHost), "{}"); w.Code != 500 {
			t.Fatalf("try %d after a failure: %d %s", i, w.Code, w.Body)
		}
	}
	if w := e.ask("POST", "/api/items/"+id+"/open-local", local, pcHost, c, fromPage(pcHost), "{}"); w.Code != 429 || !strings.Contains(w.Body.String(), "several times just now") {
		t.Fatalf("one try too many in a short while: %d %s", w.Code, w.Body)
	}
}

// With the test switch set, the program writes down what it would open
// and opens nothing. The browser test relies on this.
func TestTheSwitchThatOnlyRecords(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	id := e.firstItem(c, "clip")
	record := filepath.Join(t.TempDir(), "opened.txt")
	t.Setenv(openRecordEnv, record)
	before := openInPlayer
	openInPlayer = openOrRecord // what the program itself uses
	t.Cleanup(func() { openInPlayer = before })
	for i := 0; i < 2; i++ {
		if w := e.ask("POST", "/api/items/"+id+"/open-local", local, pcHost, c, fromPage(pcHost), "{}"); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		// The same film is not opened twice in a row (see
		// TestThePlayerIsNotStartedOverAndOver), so time passes in between.
		later(e, time.Minute)
	}
	got, err := os.ReadFile(record)
	line := filepath.Join(e.media, "clip.mp4") + "\n"
	if err != nil || string(got) != line+line {
		t.Fatalf("recorded %q (%v), want the film's path on a line, twice", got, err)
	}
	// A switch nobody can see leaves a trace where it acts.
	if log, _ := os.ReadFile(e.s.log.path); !strings.Contains(string(log), openRecordEnv+" is set") || strings.Contains(string(log), e.media) {
		t.Errorf("the log should say the switch was on, without the film's folder: %s", log)
	}
}

type handoffAnswer struct {
	URL          string `json:"url"`
	ExpiresAt    int64  `json:"expiresAt"`
	ValidSeconds int64  `json:"validSeconds"`
	ThisPC       bool   `json:"thisPC"`
}

// fetchLink asks for the bytes at a complete link the way a player outside
// the browser would: no cookie, no special header.
func (e *env) fetchLink(link string) *httptest.ResponseRecorder {
	e.t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		e.t.Fatalf("not a complete link: %q (%v)", link, err)
	}
	return e.ask("GET", u.RequestURI(), "192.168.1.77:40000", u.Host, nil, map[string]string{"Range": "bytes=10-14"}, "")
}

func TestHandoffLinkWorksUntilItsSignInEnds(t *testing.T) {
	e := newEnv(t)
	owner := e.setup()
	// A PC with two network connections; the phone uses the second.
	homeAt(t, "http://10.0.0.5:8177", "http://"+phoneHost)
	signIn := func() (*http.Cookie, string) {
		c := sessionCookie(e.do("POST", "/api/login", remote, nil, map[string]string{"name": "owner", "password": "a good password"}))
		if c == nil {
			t.Fatal("could not sign in")
		}
		var sid string
		e.s.db.QueryRow(`SELECT id FROM sessions ORDER BY rowid DESC LIMIT 1`).Scan(&sid)
		return c, sid
	}
	phone, phoneSession := signIn()
	id := e.firstItem(phone, "clip")
	ask := func(c *http.Cookie, from, host string) handoffAnswer {
		t.Helper()
		w := e.ask("GET", "/api/items/"+id+"/handoff", from, host, c, nil, "")
		if w.Code != 200 {
			t.Fatalf("handoff: %d %s", w.Code, w.Body)
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("a link that needs no sign-in may be kept by the browser: Cache-Control %q", cc)
		}
		var h handoffAnswer
		decode(t, w, &h)
		return h
	}

	now := time.Now().Unix()
	h := ask(phone, remote, phoneHost)
	// The address is the one this device reached the PC at, and the link
	// is the kind the player in the page gets: signed just now, for the
	// usual time, for this sign-in.
	if !strings.HasPrefix(h.URL, "http://"+phoneHost+"/stream/"+e.fileOf(id)+"?") {
		t.Fatalf("the link is %q", h.URL)
	}
	life := int64(linkLife / time.Second)
	if h.ThisPC || h.ValidSeconds != life || h.ExpiresAt < now+life || h.ExpiresAt > now+life+60 {
		t.Fatalf("signed at %d for %d seconds: %+v", now, life, h)
	}
	u, _ := url.Parse(h.URL)
	if u.Query().Get("s") != phoneSession {
		t.Fatalf("the link belongs to sign-in %q, not to the one that asked (%q)", u.Query().Get("s"), phoneSession)
	}
	if w := e.fetchLink(h.URL); w.Code != 206 || w.Body.String() != "abcde" {
		t.Fatalf("the link, fetched with no sign-in: %d %q", w.Code, w.Body)
	}

	// The browser on the PC is told so, and is given the first home
	// address rather than "localhost", which means the PC only to itself.
	pc := ask(owner, local, pcHost)
	if !pc.ThisPC || !strings.HasPrefix(pc.URL, "http://10.0.0.5:8177/stream/") {
		t.Fatalf("on the PC: %+v", pc)
	}
	// A page on the PC that another website made the request for is not
	// "the PC" (browsers add this header to such requests).
	w := e.ask("GET", "/api/items/"+id+"/handoff", local, pcHost, owner, map[string]string{"Origin": "http://evil.example"}, "")
	var viaSite handoffAnswer
	decode(t, w, &viaSite)
	if viaSite.ThisPC {
		t.Errorf("a request from another website counted as the PC's own page")
	}

	// Signing out ends the link, and only that sign-in's links.
	if w := e.do("POST", "/api/logout", remote, phone, nil); w.Code != 200 {
		t.Fatalf("sign-out: %d", w.Code)
	}
	if w := e.fetchLink(h.URL); w.Code != 403 {
		t.Errorf("the link still works after its sign-in was signed out: %d", w.Code)
	}
	if w := e.fetchLink(pc.URL); w.Code != 206 {
		t.Errorf("signing out on the phone ended the PC's link: %d", w.Code)
	}
	if w := e.ask("GET", "/api/items/"+id+"/handoff", remote, phoneHost, phone, nil, ""); w.Code != 401 {
		t.Errorf("a signed-out cookie was given a new link: %d %s", w.Code, w.Body)
	}

	// So does the sign-in running out.
	tablet, tabletSession := signIn()
	h2 := ask(tablet, remote, phoneHost)
	if w := e.fetchLink(h2.URL); w.Code != 206 {
		t.Fatalf("a second sign-in's link: %d", w.Code)
	}
	longAgo := time.Now().Add(-sessionLife - time.Hour).Unix()
	if _, err := e.s.db.Exec(`UPDATE sessions SET created_at=? WHERE id=?`, longAgo, tabletSession); err != nil {
		t.Fatal(err)
	}
	if w := e.fetchLink(h2.URL); w.Code != 403 {
		t.Errorf("the link still works after its sign-in ran out: %d", w.Code)
	}
}

func TestHandoffAddressIsAlwaysOneOfTheServersOwn(t *testing.T) {
	e := newEnv(t)
	req := func(from, host string, arrivedAt net.Addr) *http.Request {
		r := httptest.NewRequest("GET", "/api/items/x/handoff", nil)
		r.RemoteAddr, r.Host = from, host
		if arrivedAt != nil {
			r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, arrivedAt))
		}
		return r
	}
	two := []string{"http://10.0.0.5:8177", "http://192.168.1.10:8177"}
	for _, c := range []struct {
		name      string
		home      []string
		r         *http.Request
		want      string
		wantFound bool
	}{
		{"the address the device is using", two, req(remote, "192.168.1.10:8177", nil), "http://192.168.1.10:8177", true},
		{"a name the server does not know itself by", two, req(remote, "mediapc.local:8177", nil), "http://10.0.0.5:8177", true},
		{"a made-up Host is never repeated", two, req(remote, "evil.example", nil), "http://10.0.0.5:8177", true},
		{"the right address on another port", two, req(remote, "192.168.1.10:9999", nil), "http://10.0.0.5:8177", true},
		{"from the PC itself", two, req(local, "localhost:8177", nil), "http://10.0.0.5:8177", true},
		{"no home address: where the connection arrived", nil, req("169.254.7.7:40000", "169.254.1.2:8177", &net.TCPAddr{IP: net.ParseIP("169.254.1.2"), Port: 8177}), "http://169.254.1.2:8177", true},
		{"no home address, an IPv6 connection", nil, req("[fd00::7]:40000", "[fd00::1]:8177", &net.TCPAddr{IP: net.ParseIP("fd00::1"), Port: 8177}), "http://[fd00::1]:8177", true},
		{"no home address, on the PC", nil, req(local, "localhost:8177", nil), "http://127.0.0.1:8177", true},
		{"no home address and nothing known", nil, req(remote, "evil.example", nil), "", false},
	} {
		homeAt(t, c.home...)
		got, found := e.s.handoffBase(c.r)
		if got != c.want || found != c.wantFound {
			t.Errorf("%s: %q %v, want %q %v", c.name, got, found, c.want, c.wantFound)
		}
	}
	// When there is nothing to build a link from, that is said.
	e.setup()
	homeAt(t)
	c := sessionCookie(e.do("POST", "/api/login", remote, nil, map[string]string{"name": "owner", "password": "a good password"}))
	id := e.firstItem(c, "clip")
	for _, way := range []string{"/handoff", "/handoff.xspf"} {
		if w := e.do("GET", "/api/items/"+id+way, remote, c, nil); w.Code != 404 || !strings.Contains(w.Body.String(), "No home network address") {
			t.Errorf("%s with no address to give: %d %s", way, w.Code, w.Body)
		}
	}
}

// What a player reads out of the playlist.
type xspfFile struct {
	XMLName xml.Name `xml:"http://xspf.org/ns/0/ playlist"`
	Version string   `xml:"version,attr"`
	Title   string   `xml:"http://xspf.org/ns/0/ title"`
	Tracks  []struct {
		Location  string `xml:"http://xspf.org/ns/0/ location"`
		Title     string `xml:"http://xspf.org/ns/0/ title"`
		Duration  string `xml:"http://xspf.org/ns/0/ duration"`
		Extension []struct {
			Application string   `xml:"application,attr"`
			Options     []string `xml:"http://www.videolan.org/vlc/playlist/ns/0/ option"`
		} `xml:"http://xspf.org/ns/0/ extension"`
	} `xml:"http://xspf.org/ns/0/ trackList>track"`
}

// readXSPF fails the test unless body is well-formed XML from its first
// byte to its last.
func readXSPF(t *testing.T, body []byte) xspfFile {
	t.Helper()
	if !utf8.Valid(body) {
		t.Fatalf("not UTF-8: %q", body)
	}
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = true
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("not well-formed XML: %v\n%s", err, body)
		}
	}
	var p xspfFile
	if err := xml.Unmarshal(body, &p); err != nil {
		t.Fatalf("not an XSPF playlist: %v\n%s", err, body)
	}
	return p
}

func TestThePlaylistForVLC(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	homeAt(t, "http://"+phoneHost)
	id := e.firstItem(c, "clip")
	// A title with every character that means something in XML, on a film
	// an hour and a half long.
	const title = `Fish & Chips <The "Director's" Cut> ]]> &amp; <!--`
	if _, err := e.s.db.Exec(`UPDATE items SET title=?, year=1999 WHERE id=?`, title, id); err != nil {
		t.Fatal(err)
	}
	e.s.db.Exec(`UPDATE files SET duration_ms=5400000 WHERE id=?`, e.fileOf(id))
	shown := title + " (1999)" // as the player in the page shows it

	w := e.do("GET", "/api/items/"+id+"/handoff.xspf?at=754.6", remote, c, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/xspf+xml; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control %q", cc)
	}
	kind, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || kind != "attachment" || params["filename"] != `Fish & Chips The Director's Cut ]] &amp; !-- (1999).xspf` {
		t.Errorf("Content-Disposition %q: %q %q %v", w.Header().Get("Content-Disposition"), kind, params["filename"], err)
	}
	raw := w.Body.String()
	for _, bare := range []string{title, "<The", `"Director`, "Director's", "]]>", "<!--", "& Chips", "&s=", "&g="} {
		if strings.Contains(raw, bare) {
			t.Errorf("%q is in the file as it is, not escaped:\n%s", bare, raw)
		}
	}
	p := readXSPF(t, w.Body.Bytes())
	if p.Version != "1" || p.Title != shown || len(p.Tracks) != 1 {
		t.Fatalf("playlist: %+v", p)
	}
	tr := p.Tracks[0]
	if tr.Title != shown || tr.Duration != "5400000" {
		t.Errorf("track: %+v", tr)
	}
	if len(tr.Extension) != 1 || tr.Extension[0].Application != "http://www.videolan.org/vlc/playlist/0" ||
		len(tr.Extension[0].Options) != 1 || tr.Extension[0].Options[0] != "start-time=754" {
		t.Errorf("the place to start from: %+v", tr.Extension)
	}
	// The link inside is complete, freshly signed, and plays with no sign-in.
	if !strings.HasPrefix(tr.Location, "http://"+phoneHost+"/stream/"+e.fileOf(id)+"?e=") {
		t.Fatalf("location %q", tr.Location)
	}
	if w := e.fetchLink(tr.Location); w.Code != 206 || w.Body.String() != "abcde" {
		t.Fatalf("the link in the playlist: %d %q", w.Code, w.Body)
	}

	// The place to start from is only written when there is one worth
	// starting from, and only when it can be read.
	for at, want := range map[string]string{"": "", "?at=0": "", "?at=0.9": "", "?at=1": "start-time=1", "?at=5394": "start-time=5394",
		"?at=5396": "", "?at=5400": "", "?at=99999": ""} {
		w := e.do("GET", "/api/items/"+id+"/handoff.xspf"+at, remote, c, nil)
		if w.Code != 200 {
			t.Errorf("%q: %d %s", at, w.Code, w.Body)
			continue
		}
		got := ""
		if x := readXSPF(t, w.Body.Bytes()).Tracks[0].Extension; len(x) == 1 && len(x[0].Options) == 1 {
			got = x[0].Options[0]
		} else if len(x) != 0 {
			t.Errorf("%q: %+v", at, x)
		}
		if got != want {
			t.Errorf("%q: option %q, want %q", at, got, want)
		}
	}
	for _, bad := range []string{"abc", "-1", "NaN", "Inf", "1e99", "12%3Bstop-time%3D1", "12%0Astop-time%3D1", "12%20", "0x10"} {
		if w := e.do("GET", "/api/items/"+id+"/handoff.xspf?at="+bad, remote, c, nil); w.Code != 400 {
			t.Errorf("at=%s: %d %s", bad, w.Code, w.Body)
		}
	}
	// (A bare semicolon makes the whole "at" unreadable to the server's
	// address parser, so it may count as not given. Either way nothing of
	// it is written.)
	if w := e.do("GET", "/api/items/"+id+"/handoff.xspf?at=12;stop-time=1", remote, c, nil); (w.Code != 200 && w.Code != 400) || strings.Contains(w.Body.String(), "stop-time") {
		t.Errorf("at=12;stop-time=1: %d %s", w.Code, w.Body)
	}
	// A film whose length is not known still gets its place.
	e.s.db.Exec(`UPDATE files SET duration_ms=0 WHERE id=?`, e.fileOf(id))
	x := readXSPF(t, e.do("GET", "/api/items/"+id+"/handoff.xspf?at=99999", remote, c, nil).Body.Bytes())
	if x.Tracks[0].Duration != "" || len(x.Tracks[0].Extension) != 1 || x.Tracks[0].Extension[0].Options[0] != "start-time=99999" {
		t.Errorf("length unknown: %+v", x.Tracks[0])
	}

	// Signing out ends the link in a playlist already downloaded.
	e.do("POST", "/api/logout", remote, c, nil)
	if w := e.fetchLink(tr.Location); w.Code != 403 {
		t.Errorf("the playlist's link still works after sign-out: %d", w.Code)
	}
	if w := e.do("GET", "/api/items/"+id+"/handoff.xspf", remote, c, nil); w.Code != 401 {
		t.Errorf("a playlist without sign-in: %d", w.Code)
	}
}

// Whatever a title or a link holds, the playlist stays one well-formed
// document that says the same thing.
func TestThePlaylistStaysWellFormed(t *testing.T) {
	for _, title := range []string{
		"", "plain", "<", ">", "&", `"`, "'", "</title></track></trackList></playlist>", "<![CDATA[", "]]>", "<?xml ?>", "&#0;", "&lt;",
		"<vlc:option>stop-time=1</vlc:option>", "line\nbreak\r\n\ttab", "Blåbærsyltetøy 日本語 🎬",
	} {
		link := "http://192.168.1.10:8177/stream/x?e=1&s=" + title + "&g=<>\"'"
		p := readXSPF(t, xspf(title, link, 1000, 5))
		if p.Title != title || len(p.Tracks) != 1 || p.Tracks[0].Title != title || p.Tracks[0].Location != link {
			t.Errorf("title %q read back as %+v", title, p)
		}
		if x := p.Tracks[0].Extension; len(x) != 1 || len(x[0].Options) != 1 || x[0].Options[0] != "start-time=5" {
			t.Errorf("title %q: options %+v", title, x)
		}
	}
	// What XML cannot hold at all (a NUL, half a letter) is replaced, never
	// passed on.
	for _, title := range []string{"nul\x00here", "bell\x07", "bad \xff\xfe bytes", "\x1b[31mred"} {
		p := readXSPF(t, xspf(title, "http://192.168.1.10:8177/stream/x", 0, 0))
		if len(p.Tracks) != 1 || !strings.ContainsRune(p.Title, utf8.RuneError) {
			t.Errorf("title %q read back as %q", title, p.Title)
		}
	}
}

func TestDownloadNamesAreSafeOnEverySystem(t *testing.T) {
	long := strings.Repeat("Å", 200)
	for title, want := range map[string]string{
		"Film (2020)":                 "Film (2020)",
		`a/b\c:d*e?f"g<h>i|j`:         "a b c d e f g h i j",
		"  spaced   out  ":            "spaced out",
		"..\\..\\Windows\\win.ini":    "Windows win.ini",
		"../../etc/passwd":            "etc passwd",
		"ends with dots...":           "ends with dots",
		"line\nbreak\ttab\x00nul\x7f": "linebreaktabnul",
		"hidden" + string(rune(0x202E)) + "gnp.exe" + string(rune(0x200B)): "hiddengnp.exe",
		"bad \xff\xfe bytes":               "bad bytes",
		"Blåbærsyltetøy 日本語 🎬":             "Blåbærsyltetøy 日本語 🎬",
		"Good Show · S1 E2 · What's Next?": "Good Show · S1 E2 · What's Next",
		"CON":                              "film",
		"nul":                              "film",
		"Lpt1":                             "film",
		"Nul.2019":                         "film",
		"aux .cut":                         "film",
		"Con Air (1997)":                   "Con Air (1997)",
		"Console.Wars":                     "Console.Wars",
		"":                                 "film",
		`<>:"/\|?*`:                        "film",
		" . ":                              "film",
		long:                               strings.Repeat("Å", 80),
	} {
		got := downloadName(title)
		if got != want {
			t.Errorf("%q became %q, want %q", title, got, want)
		}
		// Whatever the title, the header that carries the name can be
		// written and read back.
		h := mime.FormatMediaType("attachment", map[string]string{"filename": got + ".xspf"})
		if _, params, err := mime.ParseMediaType(h); err != nil || params["filename"] != got+".xspf" {
			t.Errorf("%q: header %q reads back as %q (%v)", title, h, params["filename"], err)
		}
	}
}
