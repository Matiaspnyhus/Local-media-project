package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/update"
)

// These tests run the whole program inside the test, as a plain release
// (v1.0.0) that finds a newer one (v1.1.0) on a stand-in release page, and
// watch what its updater does by itself when someone is watching a film.
// The restart itself is not carried out: the program stopping with the
// swap of versions lined up is as far as they go. (The swap and the new
// version coming up are what TestUpdateGoBackAndABrokenUpdate covers.)

// quiet is one program under test.
type quiet struct {
	t       *testing.T
	app     *app
	stopped chan error
	owner   *site
	install string // the folder the program takes for its own
	data    string
	// The stand-in release page answers only when the test lets it: it says
	// nothing about a newer version until the owner's account exists, and
	// hands the package out only when told to, so that things can be made
	// to happen during the download.
	setUp     *gate
	asked     chan struct{} // closed when the package is first asked for
	release   *gate         // opened to let the download finish
	downloads atomic.Int32
}

// gate holds something back until it is opened; opening twice is harmless.
type gate struct {
	c    chan struct{}
	once sync.Once
}

func newGate() *gate  { return &gate{c: make(chan struct{})} }
func (g *gate) open() { g.once.Do(func() { close(g.c) }) }

// passed waits for the gate to open; it is false if the caller gave up first.
func (g *gate) passed(r *http.Request) bool {
	select {
	case <-g.c:
		return true
	case <-r.Context().Done():
		return false
	}
}

func (q *quiet) letDownloadFinish() { q.release.open() }

// as sets the name a device gives for itself on every request.
type as struct {
	userAgent string
	next      http.RoundTripper
}

func (a as) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", a.userAgent)
	return a.next.RoundTrip(r)
}

// device is a browser of its own, with its own sign-in.
func (q *quiet) device(userAgent string) *site {
	jar, _ := cookiejar.New(nil)
	return &site{t: q.t, base: q.owner.base, c: &http.Client{Jar: jar, Timeout: 20 * time.Second, Transport: as{userAgent, http.DefaultTransport}}}
}

// startQuiet starts the program. before, if not nil, runs first, with the
// folder the program will take for its own.
func startQuiet(t *testing.T, before func(install string, pkg []byte)) *quiet {
	t.Helper()
	work := t.TempDir()
	q := &quiet{t: t, install: filepath.Join(work, "install"), data: filepath.Join(work, "data"),
		setUp: newGate(), asked: make(chan struct{}), release: newGate(), stopped: make(chan error, 1)}
	media := filepath.Join(work, "media")
	for _, d := range []string{q.install, media} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(media, "A Film (2001).mp4"), []byte("not really a film, but enough to send"), 0o644)
	exe := filepath.Join(q.install, exeName())
	os.WriteFile(exe, []byte("the program, version 1.0.0"), 0o755)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("LocalMedia/" + exeName())
	w.Write([]byte("the program, version 1.1.0"))
	zw.Close()
	pkg := buf.Bytes()
	if before != nil {
		before(q.install, pkg)
	}

	var feed *httptest.Server
	var once sync.Once
	feed = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			if !q.setUp.passed(r) {
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.1.0", "assets": []map[string]string{
				{"name": packageName(), "browser_download_url": feed.URL + "/zip"},
				{"name": "SHA256SUMS.txt", "browser_download_url": feed.URL + "/sums"}}})
		case "/sums":
			sum := sha256.Sum256(pkg)
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), packageName())
		case "/zip":
			q.downloads.Add(1)
			once.Do(func() { close(q.asked) })
			if !q.release.passed(r) {
				return
			}
			w.Write(pkg)
		}
	}))
	t.Cleanup(feed.Close)
	t.Cleanup(func() { q.setUp.open(); q.release.open() }) // or the page could not close

	// The program as a release that looks for updates, quickly.
	oldVersion, oldFirst, oldRetry, oldExe := version, firstCheck, retryEvery, executable
	version, firstCheck, retryEvery = "v1.0.0", 50*time.Millisecond, 50*time.Millisecond
	executable = func() (string, error) { return exe, nil }
	t.Setenv("LOCALMEDIA_UPDATE_FEED", feed.URL+"/latest")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	q.app = &app{opts: options{dataDir: q.data, port: port, noBrowser: true, noTray: true}, quit: make(chan struct{}), ready: make(chan struct{})}
	go func() { q.stopped <- q.app.run() }()
	t.Cleanup(func() {
		// Nothing of the program may be left running when its settings are
		// put back.
		q.setUp.open()
		q.release.open()
		q.app.stop()
		select {
		case err := <-q.stopped:
			q.stopped <- err
		case <-time.After(60 * time.Second):
			t.Error("the program did not stop in 60 seconds")
		}
		version, firstCheck, retryEvery, executable = oldVersion, oldFirst, oldRetry, oldExe
	})
	select {
	case <-q.app.ready:
	case err := <-q.stopped:
		t.Fatalf("the program did not start: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the program did not start in 60 seconds")
	}
	jar, _ := cookiejar.New(nil)
	q.owner = &site{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), c: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	if code := q.owner.call("POST", "/api/setup", map[string]string{"name": "owner", "password": "a good password", "folder": media}, nil); code != 200 {
		t.Fatalf("setup: %d", code)
	}
	q.setUp.open()
	return q
}

// watchAFilm signs a device in and has it do what a player does: open the
// film, fetch a piece of it and save how far it has got.
func (q *quiet) watchAFilm(userAgent string) *site {
	q.t.Helper()
	d := q.device(userAgent)
	if code := d.call("POST", "/api/login", map[string]string{"name": "owner", "password": "a good password"}, nil); code != 200 {
		q.t.Fatalf("sign-in: %d", code)
	}
	var films struct{ Items []struct{ ID string } }
	d.call("GET", "/api/movies", nil, &films)
	if len(films.Items) != 1 {
		q.t.Fatalf("the library holds %+v", films)
	}
	var play struct{ URL string }
	if code := d.call("POST", "/api/items/"+films.Items[0].ID+"/play", nil, &play); code != 200 || !strings.HasPrefix(play.URL, "/stream/") {
		q.t.Fatalf("play: %d %+v", code, play)
	}
	req, _ := http.NewRequest("GET", q.owner.base+play.URL, nil)
	req.Header.Set("Range", "bytes=0-9")
	res, err := http.DefaultClient.Do(req) // no cookie, as a player fetches
	if err != nil || res.StatusCode != 206 {
		q.t.Fatalf("fetching a piece of the film: %v %v", err, res)
	}
	res.Body.Close()
	if code := d.call("PUT", "/api/items/"+films.Items[0].ID+"/progress", map[string]int64{"positionMs": 5000, "durationMs": 600000}, nil); code != 200 {
		q.t.Fatalf("progress: %d", code)
	}
	return d
}

type waitStatus struct {
	Current, Available string
	Waiting            bool
	Watching           []string
	Problem            string
}

func (q *quiet) status() waitStatus {
	var st waitStatus
	q.owner.call("GET", "/api/update", nil, &st)
	return st
}

// untilWaiting waits for the settings page to be told that the update is
// waiting for a quiet moment. The program restarting instead is the fault
// these tests are about.
func (q *quiet) untilWaiting() waitStatus {
	q.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-q.stopped:
			q.stopped <- err
			q.t.Fatalf("the program stopped to install the update while a film was being watched (run ended with: %v)", err)
		default:
		}
		if st := q.status(); st.Waiting {
			return st
		}
		if time.Now().After(deadline) {
			q.t.Fatalf("after 60 seconds the update was neither installed nor said to be waiting: %+v", q.status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// untilRestart waits for the program to stop with the new version lined up.
func (q *quiet) untilRestart(why string) {
	q.t.Helper()
	select {
	case err := <-q.stopped:
		q.stopped <- err
		if err != nil {
			q.t.Fatalf("%s: the program stopped with %v", why, err)
		}
	case <-time.After(60 * time.Second):
		q.t.Fatalf("%s: the update was not installed within 60 seconds: %+v", why, q.status())
	}
	if q.app.next == nil {
		q.t.Fatalf("%s: the program stopped without lining up the new version", why)
	}
	if st := update.ReadState(q.data); st.Result != "pending" || st.From != "v1.0.0" || st.To != "v1.1.0" {
		q.t.Fatalf("%s: the record of the update reads %+v", why, st)
	}
	if m, _ := filepath.Glob(filepath.Join(q.data, "backups", "*.db")); len(m) != 1 {
		q.t.Fatalf("%s: want exactly one database backup, made at the restart; found %v", why, m)
	}
}

func (q *quiet) stillRunning(when string) {
	q.t.Helper()
	select {
	case err := <-q.stopped:
		q.stopped <- err
		q.t.Fatalf("%s: the program has stopped (%v)", when, err)
	default:
	}
	if q.app.next != nil {
		q.t.Fatalf("%s: a restart has been lined up", when)
	}
}

// The fault: "is anyone watching?" was asked once, before the download. A
// film started while the new version was downloading was cut off when the
// download finished. Now it is asked again at the moment of the restart;
// the update waits, says so, and goes in when the viewer has gone.
func TestAnUpdateWaitsWhileSomeoneIsWatching(t *testing.T) {
	q := startQuiet(t, nil)
	// Nobody is watching, so the program starts fetching the new version
	// by itself.
	select {
	case <-q.asked:
	case <-time.After(60 * time.Second):
		t.Fatalf("the program never began downloading the update: %+v", q.status())
	}
	// While it downloads, someone starts a film.
	viewer := q.watchAFilm("Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1")
	q.letDownloadFinish()

	st := q.untilWaiting()
	if st.Current != "v1.0.0" || st.Available != "v1.1.0" || len(st.Watching) != 1 || st.Watching[0] != "owner on an iPhone" {
		t.Errorf("the settings page is told %+v; want v1.1.0 waiting, and that the owner on an iPhone is watching", st)
	}
	// It keeps looking, and keeps finding the viewer. Nothing is thrown
	// away and nothing is fetched twice meanwhile.
	for i := 0; i < 10; i++ {
		time.Sleep(3 * retryEvery)
		q.stillRunning("while the film is being watched")
	}
	if !q.status().Waiting {
		t.Errorf("the wait is no longer shown: %+v", q.status())
	}
	if _, ok := update.Ready(q.install, "v1.1.0"); !ok {
		t.Error("the downloaded version was not kept for the quiet moment")
	}
	if n := q.downloads.Load(); n != 1 {
		t.Errorf("the new version was downloaded %d times; want once", n)
	}
	if m, _ := filepath.Glob(filepath.Join(q.data, "backups", "*.db")); len(m) != 0 {
		t.Errorf("database backups were left behind by tries that waited: %v", m)
	}
	if st := update.ReadState(q.data); st.Result != "" {
		t.Errorf("an update that has not happened is on record: %+v", st)
	}

	// The viewer leaves. The next look finds nobody, and the update goes in.
	if code := viewer.call("POST", "/api/logout", nil, nil); code != 200 {
		t.Fatalf("sign-out: %d", code)
	}
	q.untilRestart("after the viewer left")
	if n := q.downloads.Load(); n != 1 {
		t.Errorf("the new version was downloaded %d times; want once", n)
	}
}

// The question that counts is the last one, asked when everything else has
// been done. The database backup is the last thing done before it, so a
// film started while the backup is being made is the latest a viewer can
// arrive and still be seen. The update must wait for that viewer too, and
// the backup made for a restart that did not happen must not be left lying.
func TestAFilmStartedDuringTheBackupStillPutsTheUpdateOff(t *testing.T) {
	backingUp, goOn := make(chan struct{}), newGate()
	var first sync.Once
	real := backUp
	backUp = func(d *sql.DB, dataDir string, schema int) (string, error) {
		first.Do(func() { close(backingUp) })
		<-goOn.c
		return real(d, dataDir, schema)
	}
	// Put back last of all, when the program has stopped.
	t.Cleanup(func() { goOn.open(); backUp = real })
	q := startQuiet(t, nil)
	t.Cleanup(goOn.open) // first of all, so that the program can stop
	q.letDownloadFinish()
	select {
	case <-backingUp:
	case <-time.After(60 * time.Second):
		t.Fatalf("the program never got as far as backing up the database: %+v", q.status())
	}
	// Nobody was watching before the download or after it. Now someone is.
	viewer := q.watchAFilm("Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1")
	goOn.open()

	st := q.untilWaiting()
	if len(st.Watching) != 1 || st.Watching[0] != "owner on an iPhone" {
		t.Errorf("the settings page is told %+v; want that the owner on an iPhone is watching", st)
	}
	q.stillRunning("after a film was started during the backup")
	// The try that found the viewer removes its backup as it gives up, a
	// moment after the wait is shown. Later tries stop at their first
	// question, long before a backup, so none is made meanwhile.
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		m, _ := filepath.Glob(filepath.Join(q.data, "backups", "*.db"))
		if len(m) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the backup made for a restart that did not happen was left behind: %v", m)
		}
	}
	if st := update.ReadState(q.data); st.Result != "" {
		t.Errorf("an update that has not happened is on record: %+v", st)
	}

	if code := viewer.call("POST", "/api/logout", nil, nil); code != 200 {
		t.Fatalf("sign-out: %d", code)
	}
	q.untilRestart("after the viewer left")
	if n := q.downloads.Load(); n != 1 {
		t.Errorf("the new version was downloaded %d times; want once", n)
	}
}

// The owner's own "Install it now" still works while someone is watching:
// it says who, and goes ahead when the owner says so.
func TestTheOwnerCanInstallWhileSomeoneIsWatching(t *testing.T) {
	q := startQuiet(t, nil)
	<-q.asked
	q.watchAFilm("Mozilla/5.0 (SMART-TV; Linux; Tizen 7.0) AppleWebKit/537.36 TV Safari/537.36")
	q.letDownloadFinish()
	q.untilWaiting()

	var refusal struct{ Error string }
	if code := q.owner.call("POST", "/api/update/install", nil, &refusal); code != 409 || !strings.HasPrefix(refusal.Error, "owner on a TV is watching right now.") {
		t.Fatalf("install while a TV is watching answered %d %q; want 409 and who is watching", code, refusal.Error)
	}
	q.stillRunning("after the owner was told who is watching")
	if code := q.owner.call("POST", "/api/update/install?anyway=1", nil, nil); code != 202 {
		t.Fatalf("install anyway: %d", code)
	}
	q.untilRestart("after the owner said to install anyway")
}

// The owner pressing the button while the program is already fetching the
// version by itself must not be lost, and must not be made to wait for the
// viewer: the fetch under way carries on and installs.
func TestTheOwnerAskingDuringTheDownloadIsNotMadeToWait(t *testing.T) {
	q := startQuiet(t, nil)
	<-q.asked
	q.watchAFilm("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36")
	if code := q.owner.call("POST", "/api/update/install?anyway=1", nil, nil); code != 202 {
		t.Fatalf("install anyway, during the download: %d", code)
	}
	// The owner's word has to have arrived before the download ends; the
	// answer above comes back before it is acted on.
	deadline := time.Now().Add(60 * time.Second)
	for {
		q.app.upd.mu.Lock()
		byOwner := q.app.upd.byOwner
		q.app.upd.mu.Unlock()
		if byOwner {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the owner's request never reached the install that was under way")
		}
		time.Sleep(10 * time.Millisecond)
	}
	q.letDownloadFinish()
	q.untilRestart("after the owner asked during the download")
	if n := q.downloads.Load(); n != 1 {
		t.Errorf("the new version was downloaded %d times; want once", n)
	}
}

// A PC that is only switched on to watch films may never have a quiet
// moment long enough for a download. A version made ready on an earlier
// evening is therefore used as it is: at the next start it is installed
// without being fetched again.
func TestAVersionMadeReadyEarlierIsNotDownloadedAgain(t *testing.T) {
	q := startQuiet(t, func(install string, pkg []byte) {
		z := filepath.Join(install, "download-v1.1.0.zip")
		os.WriteFile(z, pkg, 0o600)
		staged, err := update.Unpack(z, install, "v1.1.0")
		os.Remove(z)
		if err != nil {
			t.Fatal(err)
		}
		if err := update.MarkReady(staged, "v1.1.0"); err != nil {
			t.Fatal(err)
		}
	})
	q.untilRestart("with the new version ready from an earlier run")
	if n := q.downloads.Load(); n != 0 {
		t.Errorf("the new version was downloaded %d times, although it was ready", n)
	}
}

// An update only installs by itself if the owner has left that switched
// on, and that too is asked at the moment of the restart: the owner may
// have switched it off while the version was downloading.
func TestNothingInstallsByItselfOnceTheOwnerSwitchedThatOff(t *testing.T) {
	q := startQuiet(t, nil)
	<-q.asked
	if code := q.owner.call("PUT", "/api/settings/auto-update", map[string]bool{"on": false}, nil); code != 200 {
		t.Fatalf("switching it off: %d", code)
	}
	q.letDownloadFinish()
	// The version is unpacked and kept...
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, ok := update.Ready(q.install, "v1.1.0"); ok {
			break
		}
		q.stillRunning("while the version was being unpacked")
		if time.Now().After(deadline) {
			t.Fatalf("the downloaded version was never made ready: %+v", q.status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// ...but not installed, now or at any later look, and not said to be
	// waiting for a quiet moment: it waits for the owner.
	for i := 0; i < 10; i++ {
		time.Sleep(3 * retryEvery)
		q.stillRunning("with installing by itself switched off and nobody watching")
	}
	if st := q.status(); st.Waiting || st.Available != "v1.1.0" {
		t.Errorf("the settings page is told %+v; want v1.1.0 on offer and no wait", st)
	}
	if code := q.owner.call("POST", "/api/update/install", nil, nil); code != 202 {
		t.Fatalf("the owner's own install: %d", code)
	}
	q.untilRestart("after the owner asked for the version that was kept")
	if n := q.downloads.Load(); n != 1 {
		t.Errorf("the new version was downloaded %d times; want once", n)
	}
}
