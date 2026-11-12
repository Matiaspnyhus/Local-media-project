package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This test updates the real program, three times, the way an installed
// copy would be updated, with a stand-in for the release page:
//
//	v1.0.0 -> v1.1.0   a good update: watch progress survives
//	v1.1.0 -> v1.0.0   the owner goes back
//	v1.0.0 -> v1.1.0   and forward again
//	v1.1.0 -> v1.2.0   a version that cannot start: it is undone by itself

func exeName() string {
	if runtime.GOOS == "windows" {
		return "localmedia.exe"
	}
	return "localmedia"
}

func build(t *testing.T, out, ver string, broken bool) {
	t.Helper()
	flags := "-X main.version=" + ver
	if broken {
		flags += " -X main.failStart=yes"
	}
	cmd := exec.Command("go", "build", "-ldflags", flags, "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", ver, err, b)
	}
}

func zipOf(t *testing.T, exe string) []byte {
	t.Helper()
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "LocalMedia/" + exeName(), Method: zip.Deflate}
	h.SetMode(0o755)
	w, _ := zw.CreateHeader(h)
	w.Write(b)
	zw.Close()
	return buf.Bytes()
}

type site struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (s *site) call(method, path string, body any, out any) int {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.base+path, rd)
	if method != "GET" {
		req.Header.Set("X-Requested-With", "localmedia")
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.c.Do(req)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func (s *site) waitVersion(want string, limit time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(limit)
	got := ""
	for time.Now().Before(deadline) {
		var h struct{ Version string }
		if s.call("GET", "/health", nil, &h) == 200 {
			if got = h.Version; got == want {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("waited %v for version %s; last answer was %q", limit, want, got)
}

type updateStatus struct {
	Supported                          bool
	Available, LastResult, LastProblem string
	Current, LastTo, WhyNot, Problem   string
	CanUndo, Installing                bool
}

func TestUpdateGoBackAndABrokenUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the program several times")
	}
	work := t.TempDir()
	install, data, media, builds := filepath.Join(work, "install"), filepath.Join(work, "data"), filepath.Join(work, "media"), filepath.Join(work, "builds")
	for _, d := range []string{install, media, builds} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(media, "A Film (2001).mp4"), []byte("not really a film"), 0o644)
	exe := filepath.Join(install, exeName())
	build(t, exe, "v1.0.0", false)
	good, bad := filepath.Join(builds, "good"+exeName()), filepath.Join(builds, "bad"+exeName())
	build(t, good, "v1.1.0", false)
	build(t, bad, "v1.2.0", true)
	packages := map[string][]byte{"v1.1.0": zipOf(t, good), "v1.2.0": zipOf(t, bad)}

	// The stand-in release page.
	tag := "v1.1.0"
	var feed *httptest.Server
	feed = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": []map[string]string{
				{"name": packageName(), "browser_download_url": feed.URL + "/zip"},
				{"name": "SHA256SUMS.txt", "browser_download_url": feed.URL + "/sums"}}})
		case "/sums":
			sum := sha256.Sum256(packages[tag])
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), packageName())
		case "/zip":
			w.Write(packages[tag])
		}
	}))
	defer feed.Close()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cmd := exec.Command(exe, "--data", data, "--port", strconv.Itoa(port), "--no-browser", "--no-tray")
	cmd.Env = append(os.Environ(), "LOCALMEDIA_UPDATE_FEED="+feed.URL+"/latest")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	// Whichever version is running at the end, stop it.
	t.Cleanup(func() {
		if b, err := os.ReadFile(filepath.Join(data, "localmedia.pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					p.Kill()
				}
			}
		}
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		time.Sleep(300 * time.Millisecond)
	})

	jar, _ := cookiejar.New(nil)
	s := &site{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), c: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	s.waitVersion("v1.0.0", 30*time.Second)
	if code := s.call("POST", "/api/setup", map[string]string{"name": "owner", "password": "a good password", "folder": media}, nil); code != 200 {
		t.Fatalf("setup: %d\n%s", code, out.String())
	}
	var films struct {
		Items []struct {
			ID         string
			PositionMs int64
		}
	}
	s.call("GET", "/api/movies", nil, &films)
	if len(films.Items) != 1 {
		t.Fatalf("%+v", films)
	}
	film := films.Items[0].ID
	if code := s.call("PUT", "/api/items/"+film+"/progress", map[string]int64{"positionMs": 61000, "durationMs": 600000}, nil); code != 200 {
		t.Fatalf("progress: %d", code)
	}
	stillWatched := func(when string) {
		t.Helper()
		films.Items = nil
		if code := s.call("GET", "/api/movies", nil, &films); code != 200 || len(films.Items) != 1 || films.Items[0].PositionMs != 61000 {
			t.Fatalf("%s: sign-in or watch progress was lost (answer %d, %+v)", when, code, films.Items)
		}
	}
	update := func(want string) {
		t.Helper()
		var st updateStatus
		if code := s.call("POST", "/api/update/check", nil, &st); code != 200 || !st.Supported || st.Available != want {
			t.Fatalf("check for %s: %d %+v", want, code, st)
		}
		if code := s.call("POST", "/api/update/install", nil, nil); code != 202 {
			t.Fatalf("install: %d", code)
		}
	}
	status := func() updateStatus {
		var st updateStatus
		s.call("GET", "/api/update", nil, &st)
		return st
	}

	// A good update.
	update("v1.1.0")
	s.waitVersion("v1.1.0", 90*time.Second)
	stillWatched("after updating to v1.1.0")
	// The new version answers a moment before it has noted that the update
	// went well, so "pending" is waited out rather than taken for the answer.
	st := status()
	for end := time.Now().Add(30 * time.Second); st.LastResult == "pending" && time.Now().Before(end); st = status() {
		time.Sleep(50 * time.Millisecond)
	}
	if st.LastResult != "ok" || st.LastTo != "v1.1.0" || !st.CanUndo || st.Available != "" {
		t.Fatalf("after a good update: %+v", st)
	}
	if m, _ := filepath.Glob(filepath.Join(data, "backups", "*.db")); len(m) != 1 {
		t.Fatalf("want one database backup from before the update, found %v", m)
	}
	if _, err := os.Stat(exe + ".previous"); err != nil {
		t.Fatal("the earlier version was not kept")
	}

	// The owner goes back.
	if code := s.call("POST", "/api/update/undo", nil, nil); code != 202 {
		t.Fatalf("undo: %d", code)
	}
	s.waitVersion("v1.0.0", 90*time.Second)
	stillWatched("after going back to v1.0.0")
	if st := status(); st.LastResult != "undone" || st.CanUndo {
		t.Fatalf("after going back: %+v", st)
	}

	// Forward again, then a version that cannot start.
	update("v1.1.0")
	s.waitVersion("v1.1.0", 90*time.Second)
	tag = "v1.2.0"
	update("v1.2.0")
	// The server goes away, the broken version fails, v1.1.0 comes back.
	deadline := time.Now().Add(120 * time.Second)
	for {
		if st := status(); st.LastTo == "v1.2.0" && st.LastResult == "failed" {
			if !strings.Contains(st.LastProblem, "did not start") {
				t.Fatalf("the reason is not said plainly: %q", st.LastProblem)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the broken update was never reported as undone: %+v\n%s", status(), out.String())
		}
		time.Sleep(300 * time.Millisecond)
	}
	s.waitVersion("v1.1.0", 30*time.Second)
	stillWatched("after a broken update was undone")
	var h struct{ Version string }
	s.call("GET", "/health", nil, &h)
	if h.Version != "v1.1.0" {
		t.Fatalf("running %s after the broken update", h.Version)
	}
}

// A build that is not a plain release never touches itself.
func TestATestBuildDoesNotUpdate(t *testing.T) {
	u := newUpdater(&app{opts: options{dataDir: t.TempDir()}}, nil)
	if u.enabled || !strings.Contains(u.why, "test build") {
		t.Fatalf("enabled=%v why=%q", u.enabled, u.why)
	}
	if err := u.Install("", true); err == nil {
		t.Fatal("a test build tried to install an update")
	}
	if st := u.Status(); st.Supported || st.CanUndo {
		t.Fatalf("%+v", st)
	}
}
