package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pkg(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

// feed is a stand-in for GitHub's release page.
type feed struct {
	*httptest.Server
	tag      string
	zip      []byte
	sums     string // "" means the true checksum
	noAsset  bool
	redirect string
}

func newFeed(t *testing.T, tag string, zipBytes []byte) *feed {
	f := &feed{tag: tag, zip: zipBytes}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			if f.tag == "" {
				http.NotFound(w, r)
				return
			}
			assets := []map[string]string{{"name": "SHA256SUMS.txt", "browser_download_url": f.URL + "/sums"}}
			if !f.noAsset {
				assets = append(assets, map[string]string{"name": "LocalMedia-Windows.zip", "browser_download_url": f.URL + "/zip"})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "assets": assets})
		case "/sums":
			sum := sha256.Sum256(f.zip)
			line := hex.EncodeToString(sum[:])
			if f.sums != "" {
				line = f.sums
			}
			fmt.Fprintf(w, "%s  other-file.zip\n%s  LocalMedia-Windows.zip\n", strings.Repeat("0", 64), line)
		case "/zip":
			if f.redirect != "" {
				http.Redirect(w, r, f.redirect, http.StatusFound)
				return
			}
			w.Write(f.zip)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *feed) source() *Source {
	u, _ := url.Parse(f.URL)
	return &Source{Feed: f.URL + "/latest", Asset: "LocalMedia-Windows.zip", Hosts: []string{u.Hostname()}}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.3", "v1.2.2", true}, {"v1.10.0", "v1.9.9", true}, {"v2.0.0", "v1.99.99", true}, {"1.0.1", "v1.0.0", true},
		{"v1.2.3", "v1.2.3", false}, {"v1.2.2", "v1.2.3", false},
		// Builds that are not plain releases are never updated or offered.
		{"v1.2.3", "dev", false}, {"v9.9.9", "claude/some-branch", false}, {"main", "v1.0.0", false}, {"v1.2", "v1.0.0", false}, {"v1.2.3-beta", "v1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestLatest(t *testing.T) {
	ctx := context.Background()
	f := newFeed(t, "v1.1.0", pkg(t, map[string]string{"LocalMedia/localmedia.exe": "new"}))
	if rel, err := f.source().Latest(ctx, "v1.0.0"); err != nil || rel == nil || rel.Version != "v1.1.0" {
		t.Fatalf("%+v %v", rel, err)
	}
	if rel, err := f.source().Latest(ctx, "v1.1.0"); err != nil || rel != nil {
		t.Fatalf("up to date: %+v %v", rel, err)
	}
	if rel, err := f.source().Latest(ctx, "v2.0.0"); err != nil || rel != nil {
		t.Fatalf("a newer install must not be downgraded: %+v %v", rel, err)
	}
	if rel, err := f.source().Latest(ctx, "dev"); err != nil || rel != nil {
		t.Fatalf("a test build must never update: %+v %v", rel, err)
	}
	f.noAsset = true
	if _, err := f.source().Latest(ctx, "v1.0.0"); err == nil {
		t.Fatal("a release without our package was accepted")
	}
	f.tag = ""
	if _, err := f.source().Latest(ctx, "v1.0.0"); err != ErrNoRelease {
		t.Fatalf("nothing published: %v", err)
	}
	// An address outside the allowed hosts is never fetched.
	bad := &Source{Feed: "https://example.com/latest", Asset: "x", Hosts: []string{"api.github.com"}}
	if _, err := bad.Latest(ctx, "v1.0.0"); err == nil {
		t.Fatal("fetched from a host that is not allowed")
	}
	plain := &Source{Feed: "http://api.github.com/x", Asset: "x", Hosts: []string{"api.github.com"}}
	if _, err := plain.Latest(ctx, "v1.0.0"); err == nil {
		t.Fatal("fetched over plain http from a real host")
	}
}

func TestDownloadKeepsOnlyWhatMatchesItsChecksum(t *testing.T) {
	ctx := context.Background()
	f := newFeed(t, "v1.1.0", pkg(t, map[string]string{"LocalMedia/localmedia.exe": "new"}))
	rel, _ := f.source().Latest(ctx, "v1.0.0")
	dir := t.TempDir()
	path, err := f.source().Download(ctx, rel, dir)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Size() != int64(len(f.zip)) {
		t.Fatalf("download: %v", err)
	}
	// A package that was tampered with, or damaged on the way.
	f.sums = strings.Repeat("a", 64)
	if _, err := f.source().Download(ctx, rel, dir); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a wrong checksum was accepted: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.zip")); len(m) != 0 {
		t.Fatalf("a rejected download was kept: %v", m)
	}
	f.sums = "not-a-checksum"
	if _, err := f.source().Download(ctx, rel, dir); err == nil {
		t.Fatal("a malformed checksum list was accepted")
	}
	// A redirect to somewhere else is not followed.
	f.sums = ""
	f.redirect = "https://example.com/evil.zip"
	if _, err := f.source().Download(ctx, rel, dir); err == nil {
		t.Fatal("a redirect to another host was followed")
	}
}

// The checksum list can be asked by itself, to learn which package a
// release holds at this moment without fetching the package.
func TestSumIsWhatTheReleaseListsForItsPackage(t *testing.T) {
	zipBytes := pkg(t, map[string]string{"LocalMedia/localmedia.exe": "new program"})
	f := newFeed(t, "v1.1.0", zipBytes)
	ctx := context.Background()
	rel, err := f.source().Latest(ctx, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(zipBytes)
	if got, err := f.source().Sum(ctx, rel); err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("Sum = %q, %v; want the package's own checksum", got, err)
	}
	// The package is replaced: the list says so at the next asking.
	f.zip = pkg(t, map[string]string{"LocalMedia/localmedia.exe": "new program, put up again"})
	want = sha256.Sum256(f.zip)
	if got, err := f.source().Sum(ctx, rel); err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("after the package was replaced, Sum = %q, %v", got, err)
	}
	f.sums = "not a checksum"
	if _, err := f.source().Sum(ctx, rel); err == nil {
		t.Fatal("a list without a checksum for the package was accepted")
	}
}

func TestUnpackRefusesAnythingButPlainFilesInOneFolder(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.zip")
	os.WriteFile(good, pkg(t, map[string]string{"LocalMedia/localmedia.exe": "new program", "LocalMedia/ffprobe.exe": "new inspector"}), 0o600)
	staged, err := Unpack(good, dir, "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(staged, "localmedia.exe")); string(b) != "new program" {
		t.Fatalf("unpacked %q", b)
	}
	for name, files := range map[string]map[string]string{
		"climbs out":  {"LocalMedia/../../evil.exe": "x"},
		"nested":      {"LocalMedia/sub/evil.exe": "x"},
		"backslashes": {`LocalMedia/..\evil.exe`: "x"},
		"hidden":      {"LocalMedia/.hidden": "x"},
		"empty":       {},
	} {
		p := filepath.Join(dir, "bad.zip")
		os.WriteFile(p, pkg(t, files), 0o600)
		if _, err := Unpack(p, dir, "v9.9.9"); err == nil {
			t.Errorf("a package that %s was unpacked", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.exe")); err == nil {
		t.Fatal("a file was written outside the install folder")
	}
	os.WriteFile(filepath.Join(dir, "junk.zip"), []byte("not a zip"), 0o600)
	if _, err := Unpack(filepath.Join(dir, "junk.zip"), dir, "v1"); err == nil {
		t.Fatal("junk was unpacked")
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestSwapRollbackAndTidy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "localmedia.exe"), []byte("v1 program"), 0o755)
	os.WriteFile(filepath.Join(dir, "ffprobe.exe"), []byte("v1 inspector"), 0o755)
	os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("keep me"), 0o644)
	z := filepath.Join(dir, "download-v2.zip")
	os.WriteFile(z, pkg(t, map[string]string{"LocalMedia/localmedia.exe": "v2 program", "LocalMedia/ffprobe.exe": "v2 inspector", "LocalMedia/NEW.txt": "new in v2"}), 0o600)
	staged, err := Unpack(z, dir, "v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if HasPrevious(dir) {
		t.Fatal("a fresh install claims an earlier version")
	}
	if err := Swap(dir, staged); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "localmedia.exe")) != "v2 program" || read(t, filepath.Join(dir, "localmedia.exe.previous")) != "v1 program" ||
		read(t, filepath.Join(dir, "NEW.txt")) != "new in v2" || read(t, filepath.Join(dir, "unrelated.txt")) != "keep me" || !HasPrevious(dir) {
		t.Fatal("swap left the folder in the wrong state")
	}
	if err := Rollback(dir); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "localmedia.exe")) != "v1 program" || read(t, filepath.Join(dir, "ffprobe.exe")) != "v1 inspector" || HasPrevious(dir) {
		t.Fatal("rollback did not restore the earlier version")
	}
	if err := Rollback(dir); err == nil {
		t.Fatal("rolled back twice")
	}
	Tidy(dir, "v1.0.0")
	if got := listing(dir); got != "NEW.txt ffprobe.exe localmedia.exe unrelated.txt" {
		t.Fatalf("after tidying: %s", got)
	}
}

func listing(dir string) string {
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}

// A version that was unpacked but could not be installed yet (someone was
// watching, and then the PC was switched off) is kept for the next start,
// so that it is not downloaded again every evening. It is only used again
// if it is still exactly what was unpacked.
func TestAnUnpackedVersionIsKeptUntilItCanBeInstalled(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "localmedia.exe"), []byte("v1 program"), 0o755)
	// The checksum the release lists for its package, and the one it lists
	// once that package has been replaced under the same version number.
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	unpack := func(version string) string {
		t.Helper()
		z := filepath.Join(dir, "download-"+version+".zip")
		os.WriteFile(z, pkg(t, map[string]string{"LocalMedia/localmedia.exe": version + " program", "LocalMedia/ffmpeg.exe": version + " converter"}), 0o600)
		staged, err := Unpack(z, dir, version)
		os.Remove(z)
		if err != nil {
			t.Fatal(err)
		}
		return staged
	}

	staged := unpack("v1.1.0")
	if _, ok := Ready(dir, "v1.1.0", first); ok {
		t.Fatal("a folder that was never noted as whole is called ready")
	}
	if err := MarkReady(staged, "v1.1.0", first); err != nil {
		t.Fatal(err)
	}
	if got, ok := Ready(dir, "v1.1.0", first); !ok || got != staged {
		t.Fatalf("Ready = %q, %v; want the unpacked folder", got, ok)
	}
	if _, ok := Ready(dir, "v1.2.0", first); ok {
		t.Fatal("the folder for one version was offered for another")
	}
	if _, ok := Ready(dir, "v1.1.0", second); ok {
		t.Fatal("the folder was offered although the release now lists another package for that version")
	}
	if _, ok := Ready(dir, "v1.1.0", ""); ok {
		t.Fatal("the folder was offered without knowing which package the release lists")
	}

	// The next start tidies up, and must leave the waiting version alone.
	Tidy(dir, "v1.0.0")
	if got, ok := Ready(dir, "v1.1.0", first); !ok || got != staged {
		t.Fatalf("tidying removed a version that was waiting to be installed (the folder now holds: %s)", listing(dir))
	}
	if got := listing(dir); got != "localmedia.exe update-v1.1.0 update-v1.1.0.ready" {
		t.Fatalf("after tidying: %s", got)
	}

	// Anything about the folder that has changed makes it unusable.
	for name, change := range map[string]func(){
		"a file was changed": func() { os.WriteFile(filepath.Join(staged, "ffmpeg.exe"), []byte("something else"), 0o755) },
		"a file is missing":  func() { os.Remove(filepath.Join(staged, "ffmpeg.exe")) },
		"a file was added":   func() { os.WriteFile(filepath.Join(staged, "extra.exe"), []byte("x"), 0o755) },
		"the note is junk":   func() { os.WriteFile(staged+".ready", []byte("{"), 0o600) },
		"the note does not say which package": func() {
			b, _ := os.ReadFile(staged + ".ready")
			os.WriteFile(staged+".ready", bytes.Replace(b, []byte(first), nil, 1), 0o600)
		},
		"the note is gone": func() { os.Remove(staged + ".ready") },
	} {
		staged = unpack("v1.1.0")
		if err := MarkReady(staged, "v1.1.0", first); err != nil {
			t.Fatal(err)
		}
		change()
		if _, ok := Ready(dir, "v1.1.0", first); ok {
			t.Errorf("%s, and the folder was still called ready", name)
		}
	}

	// Unpacking again starts afresh: the old note does not vouch for it.
	staged = unpack("v1.1.0")
	if err := MarkReady(staged, "v1.1.0", first); err != nil {
		t.Fatal(err)
	}
	staged = unpack("v1.1.0")
	if _, ok := Ready(dir, "v1.1.0", first); ok {
		t.Fatal("a note from an earlier unpacking vouched for a new one")
	}

	// Once installed, nothing of it is left to tidy; and a folder for a
	// version that is not newer than the running one is not kept.
	if err := MarkReady(staged, "v1.1.0", first); err != nil {
		t.Fatal(err)
	}
	if err := Swap(dir, staged); err != nil {
		t.Fatal(err)
	}
	if got := listing(dir); got != "ffmpeg.exe localmedia.exe localmedia.exe.previous" {
		t.Fatalf("after installing: %s", got)
	}
	old := unpack("v1.1.0")
	MarkReady(old, "v1.1.0", first)
	half := unpack("v1.3.0") // unpacked, never noted as whole
	os.WriteFile(filepath.Join(dir, "update-v1.4.0.ready"), []byte(`{"version":"v1.4.0","files":{"a":"b"}}`), 0o600)
	_ = half
	Tidy(dir, "v1.1.0")
	if got := listing(dir); got != "ffmpeg.exe localmedia.exe localmedia.exe.previous" {
		t.Fatalf("after tidying a version that is already installed, a half-unpacked one and a note without a folder: %s", got)
	}
}

func TestStateAndDatabaseRestore(t *testing.T) {
	dir := t.TempDir()
	if s := ReadState(dir); s.Result != "" {
		t.Fatalf("%+v", s)
	}
	want := State{From: "v1.0.0", To: "v1.1.0", At: 5, DBBackup: "b.db", Result: "pending"}
	if err := WriteState(dir, want); err != nil {
		t.Fatal(err)
	}
	if got := ReadState(dir); got != want {
		t.Fatalf("%+v", got)
	}
	dbPath, backup := filepath.Join(dir, "localmedia.db"), filepath.Join(dir, "backup.db")
	os.WriteFile(dbPath, []byte("after the update"), 0o600)
	os.WriteFile(dbPath+"-wal", []byte("stale"), 0o600)
	os.WriteFile(backup, []byte("before the update"), 0o600)
	if err := RestoreDatabase(backup, dbPath); err != nil {
		t.Fatal(err)
	}
	if read(t, dbPath) != "before the update" || read(t, dbPath+"-wal") != "<missing>" || read(t, backup) != "before the update" {
		t.Fatal("restore left the wrong files")
	}
	if err := RestoreDatabase(filepath.Join(dir, "gone.db"), dbPath); err == nil || read(t, dbPath) != "before the update" {
		t.Fatal("a missing backup damaged the database")
	}
}
