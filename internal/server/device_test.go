package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
)

// restartEnv is a server that can be stopped and started again over the
// same data folder, the way the program is when the PC restarts or an
// update installs itself. Pages left open keep their sign-in across that.
type restartEnv struct {
	*env
	data string
	stop func()
}

func newRestartEnv(t *testing.T) *restartEnv {
	t.Helper()
	media := t.TempDir()
	os.MkdirAll(filepath.Join(media, "Films"), 0o755)
	os.WriteFile(filepath.Join(media, "Films", "Some.Movie.mkv"), []byte("mkvdata"), 0o644)
	r := &restartEnv{env: &env{t: t, media: media}, data: t.TempDir()}
	r.start()
	t.Cleanup(func() { r.stop() })
	return r
}

func (r *restartEnv) start() {
	r.t.Helper()
	d, err := db.Open(r.data)
	if err != nil {
		r.t.Fatal(err)
	}
	s, err := New(Config{DB: d, DataDir: r.data, Version: "test", Port: 8177, Assets: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}}})
	if err != nil {
		r.t.Fatal(err)
	}
	r.s = s
	r.stop = func() { s.Close(); d.Close() }
}

func (r *restartEnv) restart() {
	r.t.Helper()
	r.stop()
	r.start()
}

// inspected records, as the inspector would, that the film is an MKV with
// an H.264 picture and Dolby sound.
func (r *restartEnv) inspected(c *http.Cookie) (film string) {
	r.t.Helper()
	film = r.firstItem(c, "Some Movie")
	var fileID string
	r.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, film).Scan(&fileID)
	r.s.db.Exec(`UPDATE files SET probed=1, container='matroska', duration_ms=5400000 WHERE id=?`, fileID)
	if _, err := r.s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,bit_depth,is_default) VALUES(?,0,'video','h264',8,1),(?,1,'audio','ac3',0,1)`, fileID, fileID); err != nil {
		r.t.Fatal(err)
	}
	return film
}

// What a device said it can play must survive a restart of the program:
// the page that said it is still open and still signed in, and will not
// say it again until it is reloaded.
func TestWhatADeviceCanPlayIsRememberedAcrossARestart(t *testing.T) {
	r := newRestartEnv(t)
	c := r.setup()
	film := r.inspected(c)
	// A phone: no MKV, no way to play repackaged video without a converter
	// on this PC, so the honest answer for this film is "no".
	phone := map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true, "ac3": true}, "hls": true}
	if w := r.do("POST", "/api/capabilities", remote, c, phone); w.Code != 200 {
		t.Fatal(w.Body)
	}
	check := func(when string) {
		t.Helper()
		var list struct{ Items []itemView }
		decode(t, r.do("GET", "/api/movies", remote, c, nil), &list)
		if len(list.Items) != 1 || list.Items[0].Playable != "no" || list.Items[0].Method != "none" {
			t.Fatalf("%s, the list says %+v; want playable=no, as the device's report implies", when, list.Items)
		}
		var p playAnswer
		decode(t, r.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
		if p.Plan.Playable != "no" || strings.Contains(p.Explain, "not been inspected") {
			t.Fatalf("%s, play says %+v", when, p)
		}
		var diag struct {
			Device *struct{ Containers map[string]bool }
		}
		decode(t, r.do("GET", "/api/diagnostics", remote, c, nil), &diag)
		if diag.Device == nil || !diag.Device.Containers["mp4"] {
			t.Fatalf("%s, diagnostics do not show the device's report: %+v", when, diag.Device)
		}
	}
	check("before the restart")
	r.restart()
	check("after the restart")
	// The support bundle still lists the device, too.
	if files := bundleFiles(t, r.do("GET", "/api/support-bundle", remote, c, nil)); !strings.Contains(files["devices.json"], `"canPlay"`) {
		t.Errorf("after the restart the support bundle lists no device: %q", files["devices.json"])
	}

	// The correction for a browser that claims too much is applied to a
	// remembered report as well.
	firefox := sessionCookie(r.do("POST", "/api/login", "192.168.1.60:1", nil, map[string]string{"name": "owner", "password": "a good password"}))
	// Firefox says it opens MKV; the server knows better (see applyOverrides).
	b, _ := json.Marshal(map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true, "ac3": true}, "hls": false})
	rq := httptest.NewRequest("POST", "/api/capabilities", bytes.NewReader(b))
	rq.RemoteAddr, rq.Host = remote, "192.168.1.10:8177"
	rq.Header.Set("X-Requested-With", "localmedia")
	rq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
	rq.AddCookie(firefox)
	w := httptest.NewRecorder()
	r.s.ServeHTTP(w, rq)
	if w.Code != 200 {
		t.Fatalf("capabilities from Firefox: %d %s", w.Code, w.Body)
	}
	r.restart()
	var list struct{ Items []itemView }
	decode(t, r.do("GET", "/api/movies", remote, firefox, nil), &list)
	if len(list.Items) != 1 || list.Items[0].Playable != "no" {
		t.Fatalf("after a restart Firefox's claim to open MKV was believed: %+v", list.Items)
	}

	// Signing out forgets the report with the sign-in.
	r.do("POST", "/api/logout", remote, c, nil)
	var n int
	r.s.db.QueryRow(`SELECT count(*) FROM device_reports`).Scan(&n)
	if n != 1 {
		t.Errorf("%d device reports kept after one of two sign-ins signed out, want 1", n)
	}
}

// When the report cannot be written to the database (the disk is full, the
// data folder cannot be written), it must still be used for as long as the
// program runs. Thrown away, every film would be "unknown" for that visit
// and sent as it is: Dolby films silent, MKV not opening on a phone.
func TestADeviceReportThatCannotBeStoredIsStillUsed(t *testing.T) {
	r := newRestartEnv(t)
	c := r.setup()
	r.inspected(c)
	// A stand-in for a disk that is full: every write to this table fails.
	if _, err := r.s.db.Exec(`CREATE TRIGGER disk_full BEFORE INSERT ON device_reports BEGIN SELECT RAISE(FAIL, 'database or disk is full'); END`); err != nil {
		t.Fatal(err)
	}
	// A phone, as in the test above: the honest answer for this film is "no".
	w := r.do("POST", "/api/capabilities", remote, c, map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true},
		"audio": map[string]bool{"aac": true, "ac3": true}, "hls": true})
	if w.Code != 200 {
		t.Errorf("the report answered %d %s; the page can do nothing about the PC's disk", w.Code, w.Body)
	}
	var list struct{ Items []itemView }
	decode(t, r.do("GET", "/api/movies", remote, c, nil), &list)
	if len(list.Items) != 1 || list.Items[0].Playable != "no" {
		t.Errorf("after a report that could not be stored the list says %+v; want playable=no, as the report implies", list.Items)
	}
	var n int
	r.s.db.QueryRow(`SELECT count(*) FROM device_reports`).Scan(&n)
	log, _ := os.ReadFile(filepath.Join(r.data, "logs", "localmedia.log"))
	if n != 0 || strings.Count(string(log), "could not be stored") != 1 {
		t.Errorf("%d reports stored, and the log says:\n%s\nwant none stored and one line saying so", n, log)
	}
}

// A sign-in whose device has not reported yet is told so, not that the
// file has not been inspected.
func TestPlayingBeforeTheDeviceHasReported(t *testing.T) {
	r := newRestartEnv(t)
	c := r.setup()
	film := r.inspected(c)
	var p struct {
		Explain string
		Plan    struct {
			Playable      string
			DeviceUnknown bool
		}
	}
	decode(t, r.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.Playable != "unknown" || !p.Plan.DeviceUnknown || strings.Contains(p.Explain, "not been inspected") || !strings.Contains(p.Explain, "device") {
		t.Fatalf("with no report from the device: %+v", p)
	}
}

// A sign-in lasts 180 days. When it runs out, everything handed out under
// it must stop with it: the cookie, links to original files, and the
// addresses of chunks, which need no cookie and used to go on working for
// as long as something kept fetching them.
func TestAnExpiredSignInEndsItsLinksAndChunkAddresses(t *testing.T) {
	e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`, `1,'audio','dts',1,0,0,0,'eng',''`}, nil)
	e.pretendConverter(t)
	chunks := e.play(t).URL // the list of chunks: asking for it converts nothing
	var original playOut
	decode(t, e.do("POST", "/api/items/"+e.firstItem(e.c, "clip")+"/play", remote, e.c, nil), &original)
	if !strings.HasPrefix(chunks, "/hls/") || !strings.HasPrefix(original.URL, "/stream/") {
		t.Fatalf("addresses: %q %q", chunks, original.URL)
	}
	answers := func() (cookie, link, list int) {
		return e.do("GET", "/api/movies", remote, e.c, nil).Code, e.do("GET", original.URL, remote, nil, nil).Code, e.do("GET", chunks, remote, nil, nil).Code
	}
	if cookie, link, list := answers(); cookie != 200 || link != 200 || list != 200 {
		t.Fatalf("while signed in: cookie %d, link %d, chunk list %d", cookie, link, list)
	}
	// 181 days later.
	if _, err := e.s.db.Exec(`UPDATE sessions SET created_at = created_at - 181*86400`); err != nil {
		t.Fatal(err)
	}
	if cookie, link, list := answers(); cookie != 401 || link != 403 || list != 403 {
		t.Errorf("after the sign-in ran out: cookie %d, link %d, chunk list %d; want 401, 403, 403", cookie, link, list)
	}
	if n := e.s.hls.Sessions(); n != 0 {
		t.Errorf("%d films still open for conversion under a sign-in that has run out", n)
	}
}

// bundleFiles opens a support bundle and returns what each file in it says.
func bundleFiles(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("support bundle: %d %s", w.Code, w.Body)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}
