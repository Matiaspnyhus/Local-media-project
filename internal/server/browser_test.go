package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/auth"
	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
)

// How some browsers describe themselves.
const (
	chromeOnWindows = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	safariOnIPhone  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1"
	firefoxOnLinux  = "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0"
)

// browser is one browser on one device. It keeps the cookies the server
// sets and sends them back, as a browser does, so that a test can follow a
// device across sign-ins.
type browser struct {
	e       *env
	ua      string
	from    string
	cookies map[string]*http.Cookie
	// set is the cookies the last answer set, by name: what a test looks at
	// to see how a cookie was handed out.
	set map[string]*http.Cookie
}

func (e *env) browser(userAgent string) *browser {
	return &browser{e: e, ua: userAgent, from: remote, cookies: map[string]*http.Cookie{}}
}

func (b *browser) do(method, target string, body any) *httptest.ResponseRecorder {
	b.e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, target, rd)
	r.RemoteAddr = b.from
	r.Host = "192.168.1.10:8177"
	r.Header.Set("User-Agent", b.ua)
	if method != "GET" && method != "HEAD" {
		r.Header.Set("X-Requested-With", "localmedia")
		r.Header.Set("Content-Type", "application/json")
	}
	for _, c := range b.cookies {
		r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	w := httptest.NewRecorder()
	b.e.s.ServeHTTP(w, r)
	b.set = map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		b.set[c.Name] = c
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return w
}

// setup finishes the server's setup from this browser, which signs it in
// as the owner. Setup is only allowed from the PC itself.
func (b *browser) setup() {
	b.e.t.Helper()
	remoteFrom := b.from
	b.from = local
	w := b.do("POST", "/api/setup", map[string]string{"name": "owner", "password": "a good password", "folder": b.e.media})
	b.from = remoteFrom
	if w.Code != 200 || b.cookies[cookieName] == nil {
		b.e.t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
}

func (b *browser) signIn(name, password string) {
	b.e.t.Helper()
	if w := b.do("POST", "/api/login", map[string]string{"name": name, "password": password}); w.Code != 200 || b.cookies[cookieName] == nil {
		b.e.t.Fatalf("sign-in as %s: %d %s", name, w.Code, w.Body)
	}
}

// signedIn is a browser signed in as the owner (setup must have run).
func (e *env) signedIn(userAgent string) *browser {
	e.t.Helper()
	b := e.browser(userAgent)
	b.signIn("owner", "a good password")
	return b
}

// device is the id of the device the browser's cookie names, "" when it
// has none.
func (b *browser) device() string {
	c := b.cookies[deviceCookie]
	if c == nil {
		return ""
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	return b.e.s.cookieDevice(r)
}

// session is the browser's sign-in, for the tests' older helpers that take
// one.
func (b *browser) session() *http.Cookie { return b.cookies[cookieName] }

// member adds a second user who is not the owner.
func (e *env) member(name, password string) {
	e.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.s.db.Exec(`INSERT INTO users(id,name,password_hash,role,created_at) VALUES(?,?,?,?,0)`, ids.New(), name, hash, "member"); err != nil {
		e.t.Fatal(err)
	}
}

// testClock is a clock a test moves by hand, so that a lesson can run out
// without anyone waiting a month. It also counts how often it is read.
type testClock struct {
	mu    sync.Mutex
	at    time.Time
	reads int
}

// clock puts the server's devices, lessons and trouble on a clock the test
// moves. It starts at the real time, because sign-ins go by the real clock.
func (e *env) clock() *testClock {
	c := &testClock{at: time.Now()}
	e.s.now = func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.reads++
		return c.at
	}
	return c
}

func (c *testClock) forward(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func (c *testClock) read() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// count asks the database for one number.
func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.s.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// must runs a statement that has to work.
func (e *env) must(q string, args ...any) {
	e.t.Helper()
	if _, err := e.s.db.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}
