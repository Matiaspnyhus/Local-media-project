package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/auth"
	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
)

const (
	local  = "127.0.0.1:50000"
	remote = "192.168.1.50:50000"
)

type env struct {
	t     *testing.T
	s     *Server
	media string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	data := t.TempDir()
	d, err := db.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	media := t.TempDir()
	os.WriteFile(filepath.Join(media, "clip.mp4"), []byte("0123456789abcdefghij"), 0o644)
	os.MkdirAll(filepath.Join(media, "Films"), 0o755)
	os.WriteFile(filepath.Join(media, "Films", "Some.Movie.mkv"), []byte("mkvdata"), 0o644)
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app.js": {Data: []byte("js")}}
	s, err := New(Config{DB: d, DataDir: data, Version: "test", Port: 8177, Assets: assets})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &env{t: t, s: s, media: media}
}

// do sends one request. cookie may be nil.
func (e *env) do(method, target, from string, cookie *http.Cookie, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, target, rd)
	r.RemoteAddr = from
	r.Host = "192.168.1.10:8177"
	if method != "GET" && method != "HEAD" {
		r.Header.Set("X-Requested-With", "localmedia")
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

func (e *env) setup() *http.Cookie {
	e.t.Helper()
	w := e.do("POST", "/api/setup", local, nil, map[string]string{"name": "owner", "password": "a good password", "folder": e.media})
	if w.Code != 200 {
		e.t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	c := sessionCookie(w)
	if c == nil {
		e.t.Fatal("setup set no session cookie")
	}
	return c
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("bad json %q: %v", w.Body, err)
	}
}

func TestRouteWithoutPolicyStopsTheServer(t *testing.T) {
	ok := func(http.ResponseWriter, *http.Request) {}
	if err := checkRoutes([]Route{{"GET", "/a", Public, ok}, {"GET", "/b", PolicyUnset, ok}}); err == nil {
		t.Fatal("a route without a policy was accepted")
	}
	if err := checkRoutes([]Route{{Method: "GET", Pattern: "/c", Handler: ok}}); err == nil {
		t.Fatal("a route with the zero policy was accepted")
	}
}

// The public surface is exactly this list. Adding a public route must be a
// deliberate change to this test.
func TestPublicRoutesAreExactlyTheAllowlist(t *testing.T) {
	want := map[string]bool{"GET /health": true, "GET /api/state": true, "POST /api/login": true}
	e := newEnv(t)
	for _, rt := range e.s.Routes() {
		key := rt.Method + " " + rt.Pattern
		if rt.Policy == Public {
			if !want[key] {
				t.Errorf("unexpected public route %s", key)
			}
			delete(want, key)
		}
	}
	for k := range want {
		t.Errorf("expected public route %s is gone", k)
	}
}

func TestNothingButSignInAnswersWithoutASession(t *testing.T) {
	e := newEnv(t)
	e.setup()
	fill := strings.NewReplacer("{id}", ids.New(), "{fileID}", ids.New())
	for _, rt := range e.s.Routes() {
		if rt.Policy == Public {
			continue
		}
		for _, from := range []string{remote, local} {
			w := e.do(rt.Method, fill.Replace(rt.Pattern), from, nil, map[string]string{})
			if w.Code != 401 && w.Code != 403 && w.Code != 409 {
				t.Errorf("%s %s from %s without sign-in answered %d: %s", rt.Method, rt.Pattern, from, w.Code, w.Body)
			}
		}
		// A made-up cookie is as good as none.
		w := e.do(rt.Method, fill.Replace(rt.Pattern), remote, &http.Cookie{Name: cookieName, Value: "made-up"}, map[string]string{})
		if w.Code < 400 {
			t.Errorf("%s %s accepted a made-up cookie: %d", rt.Method, rt.Pattern, w.Code)
		}
	}
	// The state route is public but must not say who anyone is.
	w := e.do("GET", "/api/state", remote, nil, nil)
	if strings.Contains(w.Body.String(), "owner") {
		t.Errorf("state leaks a user to a stranger: %s", w.Body)
	}
}

func TestBeforeSetupOnlyThePCIsAnswered(t *testing.T) {
	e := newEnv(t)
	for _, target := range []string{"/", "/api/state", "/api/setup/folders", "/api/items", "/assets/app.js"} {
		if w := e.do("GET", target, remote, nil, nil); w.Code != 403 {
			t.Errorf("before setup, %s answered a remote device with %d", target, w.Code)
		}
	}
	if w := e.do("GET", "/health", remote, nil, nil); w.Code != 200 {
		t.Errorf("health check: %d", w.Code)
	}
	w := e.do("POST", "/api/setup", remote, nil, map[string]string{"name": "evil", "password": "evil password", "folder": e.media})
	if w.Code != 403 {
		t.Fatalf("a remote device could run setup: %d", w.Code)
	}
	if w := e.do("GET", "/api/setup/folders", local, nil, nil); w.Code != 200 {
		t.Errorf("folder picker on the PC: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/setup", local, nil, map[string]string{"name": "o", "password": "short", "folder": e.media}); w.Code != 400 {
		t.Errorf("short password accepted: %d", w.Code)
	}
	if w := e.do("POST", "/api/setup", local, nil, map[string]string{"name": "o", "password": "long enough pw", "folder": filepath.Join(e.media, "nope")}); w.Code != 400 {
		t.Errorf("missing folder accepted: %d", w.Code)
	}
	if e.s.setupDone.Load() {
		t.Fatal("a failed setup marked setup as finished")
	}
	e.setup()
	// Setup can never be run twice, not even from the PC.
	w = e.do("POST", "/api/setup", local, nil, map[string]string{"name": "second", "password": "another password", "folder": e.media})
	if w.Code != 409 {
		t.Fatalf("setup ran twice: %d", w.Code)
	}
	if w := e.do("GET", "/api/setup/folders", local, nil, nil); w.Code != 409 {
		t.Errorf("setup folder picker still open after setup: %d", w.Code)
	}
}

type playOut struct {
	URL        string `json:"url"`
	PositionMs int64  `json:"positionMs"`
	MimeType   string `json:"mimeType"`
}

func (e *env) firstItem(c *http.Cookie, title string) string {
	e.t.Helper()
	var out struct{ Items []itemOut }
	decode(e.t, e.do("GET", "/api/items", remote, c, nil), &out)
	for _, it := range out.Items {
		if it.Title == title {
			return it.ID
		}
	}
	e.t.Fatalf("item %q not in %+v", title, out.Items)
	return ""
}

func TestPlayStreamResumeAndSignOut(t *testing.T) {
	e := newEnv(t)
	e.setup()
	// Sign in again from a phone, as the owner would.
	w := e.do("POST", "/api/login", remote, nil, map[string]string{"name": "Owner", "password": "a good password"})
	c := sessionCookie(w)
	if w.Code != 200 || c == nil {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	if c.Secure || !c.HttpOnly {
		t.Fatalf("cookie flags wrong (Secure would break plain HTTP at home): %+v", c)
	}
	id := e.firstItem(c, "clip")
	var p playOut
	decode(t, e.do("POST", "/api/items/"+id+"/play", remote, c, nil), &p)
	if p.MimeType != "video/mp4" || p.PositionMs != 0 {
		t.Fatalf("%+v", p)
	}

	// The link works with no cookie at all (AirPlay, external players)...
	r := httptest.NewRequest("GET", p.URL, nil)
	r.RemoteAddr = remote
	r.Header.Set("Range", "bytes=10-14")
	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, r)
	if rec.Code != 206 || rec.Body.String() != "abcde" || rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("range request: %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	// ...but not when any part of it is changed.
	u, _ := url.Parse(p.URL)
	q := u.Query()
	for name, change := range map[string]func(url.Values){
		"later expiry":  func(v url.Values) { v.Set("e", "99999999999") },
		"other session": func(v url.Values) { v.Set("s", ids.New()) },
		"cut signature": func(v url.Values) { v.Set("g", v.Get("g")[:10]) },
		"no signature":  func(v url.Values) { v.Del("g") },
	} {
		v := url.Values{}
		for k := range q {
			v.Set(k, q.Get(k))
		}
		change(v)
		if w := e.do("GET", u.Path+"?"+v.Encode(), remote, nil, nil); w.Code != 403 {
			t.Errorf("link with %s was accepted: %d", name, w.Code)
		}
	}
	other := e.firstItem(c, "Some Movie")
	var otherFile string
	e.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, other).Scan(&otherFile)
	if w := e.do("GET", "/stream/"+otherFile+"?"+q.Encode(), remote, nil, nil); w.Code != 403 {
		t.Errorf("a link for one file opened another: %d", w.Code)
	}
	// An expired link, correctly signed, is refused.
	var sid string
	e.s.db.QueryRow(`SELECT id FROM sessions ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&sid)
	fileID := strings.TrimPrefix(u.Path, "/stream/")
	past := time.Now().Add(-time.Minute).Unix()
	expired := u.Path + "?e=" + strconv.FormatInt(past, 10) + "&s=" + q.Get("s") + "&g=" + auth.SignLink(e.s.linkKey, fileID, past, q.Get("s"))
	if w := e.do("GET", expired, remote, nil, nil); w.Code != 403 {
		t.Errorf("expired link accepted: %d", w.Code)
	}

	// Progress is remembered and handed back.
	if w := e.do("PUT", "/api/items/"+id+"/progress", remote, c, map[string]int64{"positionMs": 42000, "durationMs": 100000}); w.Code != 200 {
		t.Fatalf("progress: %d %s", w.Code, w.Body)
	}
	decode(t, e.do("POST", "/api/items/"+id+"/play", remote, c, nil), &p)
	if p.PositionMs != 42000 {
		t.Fatalf("resume position %d", p.PositionMs)
	}
	// Near the end counts as watched, and the next play starts over.
	e.do("PUT", "/api/items/"+id+"/progress", remote, c, map[string]int64{"positionMs": 97000, "durationMs": 100000})
	decode(t, e.do("POST", "/api/items/"+id+"/play", remote, c, nil), &p)
	if p.PositionMs != 0 {
		t.Fatalf("a watched item resumed at %d", p.PositionMs)
	}

	// Signing out kills the cookie and every link handed to that session.
	if w := e.do("POST", "/api/logout", remote, c, nil); w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := e.do("GET", "/api/items", remote, c, nil); w.Code != 401 {
		t.Errorf("cookie still works after sign-out: %d", w.Code)
	}
	if w := e.do("GET", p.URL, remote, nil, nil); w.Code != 403 {
		t.Errorf("stream link still works after sign-out: %d", w.Code)
	}
}

func TestStreamCannotLeaveTheLibraryFolder(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	secret := filepath.Join(filepath.Dir(e.media), "secret.txt")
	os.WriteFile(secret, []byte("private"), 0o644)
	defer os.Remove(secret)
	os.Symlink(secret, filepath.Join(e.media, "trick.mp4"))

	id := e.firstItem(c, "clip")
	var sid string
	e.s.db.QueryRow(`SELECT id FROM sessions LIMIT 1`).Scan(&sid)
	for _, rel := range []string{"../secret.txt", "trick.mp4", "/etc/passwd", "Films/../../secret.txt"} {
		if _, err := e.s.db.Exec(`UPDATE files SET rel_path=? WHERE version_id IN (SELECT id FROM versions WHERE item_id=?)`, rel, id); err != nil {
			t.Fatal(err)
		}
		var p playOut
		decode(t, e.do("POST", "/api/items/"+id+"/play", remote, c, nil), &p)
		w := e.do("GET", p.URL, remote, nil, nil)
		if w.Code == 200 || strings.Contains(w.Body.String(), "private") {
			t.Errorf("rel_path %q escaped the library: %d %q", rel, w.Code, w.Body)
		}
	}
}

func TestWrongPasswordsAreSlowedDown(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for i := 0; i < loginMaxFails; i++ {
		if w := e.do("POST", "/api/login", remote, nil, map[string]string{"name": "owner", "password": "wrong"}); w.Code != 401 {
			t.Fatalf("try %d: %d", i, w.Code)
		}
	}
	w := e.do("POST", "/api/login", remote, nil, map[string]string{"name": "owner", "password": "a good password"})
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("after %d wrong tries the right password still got %d", loginMaxFails, w.Code)
	}
	// Another device is not punished for it.
	if w := e.do("POST", "/api/login", "192.168.1.77:1", nil, map[string]string{"name": "owner", "password": "a good password"}); w.Code != 200 {
		t.Fatalf("other device blocked: %d", w.Code)
	}
	e.s.limiter.now = func() time.Time { return time.Now().Add(loginWindow + time.Second) }
	if w := e.do("POST", "/api/login", remote, nil, map[string]string{"name": "owner", "password": "a good password"}); w.Code != 200 {
		t.Fatalf("still blocked after the wait: %d", w.Code)
	}
}

func TestOnlyTheOwnerReachesOwnerRoutes(t *testing.T) {
	e := newEnv(t)
	e.setup()
	hash, _ := auth.HashPassword("member password")
	e.s.db.Exec(`INSERT INTO users(id,name,password_hash,role,created_at) VALUES(?,?,?,?,0)`, ids.New(), "kid", hash, "member")
	c := sessionCookie(e.do("POST", "/api/login", remote, nil, map[string]string{"name": "kid", "password": "member password"}))
	if c == nil {
		t.Fatal("member could not sign in")
	}
	for _, rt := range e.s.Routes() {
		if rt.Policy != Owner {
			continue
		}
		if w := e.do(rt.Method, rt.Pattern, remote, c, map[string]string{"folder": "/"}); w.Code != 403 {
			t.Errorf("member reached owner route %s %s: %d", rt.Method, rt.Pattern, w.Code)
		}
	}
	if w := e.do("GET", "/api/items", remote, c, nil); w.Code != 200 {
		t.Errorf("member cannot list items: %d", w.Code)
	}
}

func TestWritesFromAnotherWebsiteAreRefused(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	r := httptest.NewRequest("POST", "/api/logout", nil)
	r.RemoteAddr, r.Host = remote, "192.168.1.10:8177"
	r.AddCookie(c)
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("write without the page's header: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/api/logout", nil)
	r.RemoteAddr, r.Host = remote, "192.168.1.10:8177"
	r.Header.Set("X-Requested-With", "localmedia")
	r.Header.Set("Origin", "http://evil.example")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("write from another origin: %d", w.Code)
	}
	if w := e.do("GET", "/api/items", remote, c, nil); w.Code != 200 {
		t.Errorf("session was signed out by a refused request: %d", w.Code)
	}
}

func TestSupportBundleHoldsNoSecretsOrAddresses(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	id := e.firstItem(c, "clip")
	e.s.log.Printf("failed sign-in from 192.168.1.50")
	e.s.log.Printf("token %s", c.Value)
	e.do("POST", "/api/client-events", remote, c, map[string]string{"kind": "play-error", "itemId": id, "detail": "MEDIA_ERR_SRC_NOT_SUPPORTED"})
	w := e.do("GET", "/api/support-bundle", remote, c, nil)
	if w.Code != 200 {
		t.Fatalf("bundle: %d", w.Code)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	names := map[string]bool{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		all += string(b)
		names[f.Name] = true
	}
	for _, n := range []string{"info.json", "playback-problems.json", "log.txt", "README.txt"} {
		if !names[n] {
			t.Errorf("bundle lacks %s", n)
		}
	}
	if !strings.Contains(all, "clip.mp4") || !strings.Contains(all, "MEDIA_ERR_SRC_NOT_SUPPORTED") {
		t.Error("bundle lacks the file that failed to play")
	}
	for _, bad := range []string{c.Value, "192.168.1.50", "a good password", "argon2id", e.media, "Some.Movie"} {
		if strings.Contains(all, bad) {
			t.Errorf("bundle contains %q", bad)
		}
	}
}

func TestAppShellIsServedAndUnknownAPIIsNot(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for _, target := range []string{"/", "/watch/abc", "/index.html"} {
		if w := e.do("GET", target, remote, nil, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "app") {
			t.Errorf("%s: %d %q", target, w.Code, w.Body)
		}
	}
	if w := e.do("GET", "/assets/app.js", remote, nil, nil); w.Code != 200 || w.Body.String() != "js" {
		t.Errorf("asset: %d %q", w.Code, w.Body)
	}
	for _, target := range []string{"/api/nope", "/stream/", "/api/items/../../etc"} {
		if w := e.do("GET", target, remote, nil, nil); w.Code == 200 {
			t.Errorf("%s answered 200: %q", target, w.Body)
		}
	}
}
