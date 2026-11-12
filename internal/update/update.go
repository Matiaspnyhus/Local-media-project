// Package update finds, checks and installs new versions of the program.
//
// The order of an update is fixed, because the goal is that an update can
// never cost the owner their library or what they have watched:
//
//  1. find a newer release and download it;
//  2. check it against the release's own list of checksums;
//  3. unpack it beside the installed files, without touching them;
//  4. (the caller backs up the database);
//  5. swap the files, keeping every replaced file as "name.previous";
//  6. (the caller starts the new version and watches it come up);
//  7. if it does not come up, swap back.
package update

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Release is one published version.
type Release struct {
	Version  string // "v1.2.3"
	AssetURL string // the package for this system
	SumsURL  string // SHA256SUMS.txt of the same release
	Asset    string // the package's file name
}

// Source says where releases are published.
type Source struct {
	// Feed is the address that describes the latest release, in the form
	// GitHub's "latest release" call returns.
	Feed string
	// Asset is the name of the package for this system.
	Asset string
	// Hosts lists the hosts downloads may come from (and be redirected
	// to). Anything else is refused.
	Hosts []string
	HTTP  *http.Client
}

// GitHub returns the source for a public GitHub repository.
func GitHub(repo, asset string) *Source {
	return &Source{
		Feed:  "https://api.github.com/repos/" + repo + "/releases/latest",
		Asset: asset,
		Hosts: []string{"api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"},
	}
}

func (s *Source) allowed(u *url.URL) bool {
	for _, h := range s.Hosts {
		if strings.EqualFold(u.Hostname(), h) {
			return u.Scheme == "https" || isLoopbackHost(u.Hostname())
		}
	}
	return false
}

func isLoopbackHost(h string) bool { return h == "127.0.0.1" || h == "localhost" || h == "::1" }

func (s *Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 || !s.allowed(req.URL) {
				return errors.New("the download was redirected somewhere unexpected")
			}
			return nil
		},
	}
}

func (s *Source) get(ctx context.Context, raw string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil || !s.allowed(u) {
		return nil, errors.New("that address is not one updates may come from")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "localmedia-updater")
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	res, err := s.client().Do(req)
	if err != nil {
		return nil, errors.New("the update server could not be reached")
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			return nil, ErrNoRelease
		}
		return nil, fmt.Errorf("the update server answered %d", res.StatusCode)
	}
	return res, nil
}

// ErrNoRelease means nothing has been published (or the project's page is
// not public, which looks the same from outside).
var ErrNoRelease = errors.New("no published version was found")

// Latest returns the newest release if it is newer than current, or nil
// when the program is up to date. A current version that is not of the
// form v1.2.3 (a test build, say) is never updated.
func (s *Source) Latest(ctx context.Context, current string) (*Release, error) {
	if _, ok := parseVersion(current); !ok {
		return nil, nil
	}
	res, err := s.get(ctx, s.Feed)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var feed struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&feed); err != nil {
		return nil, errors.New("the update server's answer could not be read")
	}
	if feed.Draft || !Newer(feed.TagName, current) {
		return nil, nil
	}
	rel := &Release{Version: feed.TagName, Asset: s.Asset}
	for _, a := range feed.Assets {
		switch a.Name {
		case s.Asset:
			rel.AssetURL = a.URL
		case "SHA256SUMS.txt":
			rel.SumsURL = a.URL
		}
	}
	if rel.AssetURL == "" || rel.SumsURL == "" {
		return nil, fmt.Errorf("version %s has no package for this system", feed.TagName)
	}
	return rel, nil
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

func parseVersion(v string) ([3]int, bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// Newer reports whether version a is newer than version b. Anything that
// is not a plain v1.2.3 is never newer and never older.
func Newer(a, b string) bool {
	x, ok1 := parseVersion(a)
	y, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// Sum asks the release's checksum list for the SHA-256 of its package, as
// the list says it at this moment. The list is small.
func (s *Source) Sum(ctx context.Context, rel *Release) (string, error) {
	res, err := s.get(ctx, rel.SumsURL)
	if err != nil {
		return "", err
	}
	want := ""
	sc := bufio.NewScanner(io.LimitReader(res.Body, 1<<20))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == rel.Asset {
			want = strings.ToLower(fields[0])
		}
	}
	res.Body.Close()
	if len(want) != 64 {
		return "", errors.New("the release does not list a checksum for its package")
	}
	return want, nil
}

// Download fetches the release's package into dir and checks it against
// the release's checksum list. The file is only kept if it matches.
func (s *Source) Download(ctx context.Context, rel *Release, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	want, err := s.Sum(ctx, rel)
	if err != nil {
		return "", err
	}
	res, err := s.get(ctx, rel.AssetURL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	path := filepath.Join(dir, "download-"+safeName(rel.Version)+".zip")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, 2<<30))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", errors.New("the download was interrupted")
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(path)
		return "", errors.New("the download does not match the release's checksum, so it was thrown away")
	}
	return path, nil
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

// Unpack extracts a package into a new folder inside installDir and
// returns it. Packages hold one top folder, which is dropped. Entries that
// would land outside the folder are refused, as is anything that is not a
// plain file.
func Unpack(zipPath, installDir, version string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", errors.New("the downloaded package could not be opened")
	}
	defer zr.Close()
	staged := stagedDir(installDir, version)
	// A note left by an earlier unpacking says nothing about this one.
	os.Remove(staged + readySuffix)
	if err := os.RemoveAll(staged); err != nil {
		return "", err
	}
	if err := os.MkdirAll(staged, 0o755); err != nil {
		return "", err
	}
	files := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(f.Name)
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.HasPrefix(name, ".") || !f.Mode().IsRegular() {
			return "", fmt.Errorf("the package holds something unexpected (%q), so it was not installed", f.Name)
		}
		if f.UncompressedSize64 > 1<<30 {
			return "", errors.New("the package holds a file that is too large")
		}
		src, err := f.Open()
		if err != nil {
			return "", err
		}
		dst, err := os.OpenFile(filepath.Join(staged, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			src.Close()
			return "", err
		}
		_, err = io.Copy(dst, io.LimitReader(src, 1<<30))
		src.Close()
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", err
		}
		files++
	}
	if files == 0 {
		return "", errors.New("the package is empty")
	}
	return staged, nil
}

func stagedDir(installDir, version string) string {
	return filepath.Join(installDir, "update-"+safeName(version))
}

// An unpacked version may have to wait before it is installed: an update
// that installs by itself waits for a moment when nobody is watching, and
// the PC may be switched off before that moment comes. So that the version
// need not be downloaded again every time, its folder is kept, with a note
// beside it ("update-v1.2.3.ready") that lists what the folder held when it
// was unpacked from a package that matched the release's checksum, and
// which package that was. A folder is only used again if it still holds
// exactly that, and if the release still lists that very package: a
// release's package can be replaced without its version changing, and what
// was unpacked from the earlier one is then no longer the version.
const readySuffix = ".ready"

type readyNote struct {
	Version string            `json:"version"`
	Package string            `json:"package"` // the SHA-256 of the package it was unpacked from
	Files   map[string]string `json:"files"`   // each file's SHA-256, by name
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// holds lists what is in an unpacked folder: each file's SHA-256, by name.
func holds(staged string) (map[string]string, error) {
	entries, err := os.ReadDir(staged)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("%s is not a plain file", e.Name())
		}
		sum, err := hashFile(filepath.Join(staged, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = sum
	}
	return files, nil
}

// MarkReady writes the note that lets a folder made by Unpack be used
// later (see Ready). Call it straight after Unpack. packageSum is the
// SHA-256 of the package that was unpacked, as the release listed it.
func MarkReady(staged, version, packageSum string) error {
	files, err := holds(staged)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(readyNote{Version: version, Package: packageSum, Files: files}, "", "  ")
	tmp := staged + readySuffix + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, staged+readySuffix)
}

// readNote returns the note beside an unpacked folder, if it has one that
// can be read.
func readNote(staged string) (readyNote, bool) {
	var note readyNote
	b, err := os.ReadFile(staged + readySuffix)
	if err != nil || json.Unmarshal(b, &note) != nil || len(note.Files) == 0 {
		return note, false
	}
	return note, true
}

// Ready returns the folder that holds version, unpacked earlier from the
// package with the SHA-256 packageSum (what the release lists for it now,
// see Source.Sum) and still exactly as it was then. ok is false when there
// is none, when it was unpacked from another package, or when anything
// about it has changed: a file missing, added or different.
func Ready(installDir, version, packageSum string) (staged string, ok bool) {
	staged = stagedDir(installDir, version)
	note, ok := readNote(staged)
	if !ok || note.Version != version || packageSum == "" || note.Package != packageSum {
		return "", false
	}
	files, err := holds(staged)
	if err != nil || len(files) != len(note.Files) {
		return "", false
	}
	for name, sum := range note.Files {
		if files[name] != sum {
			return "", false
		}
	}
	return staged, true
}

const previousSuffix = ".previous"

// Swap moves the unpacked files into installDir. Each file it replaces is
// kept beside it as "name.previous" (a running program can be renamed, on
// Windows too, which is what makes this work). If anything goes wrong
// part-way, what was already swapped is put back.
func Swap(installDir, staged string) error {
	entries, err := os.ReadDir(staged)
	if err != nil {
		return err
	}
	var done []string
	undo := func() {
		for i := len(done) - 1; i >= 0; i-- {
			cur := filepath.Join(installDir, done[i])
			os.Remove(cur)
			os.Rename(cur+previousSuffix, cur)
		}
	}
	for _, e := range entries {
		name := e.Name()
		cur := filepath.Join(installDir, name)
		clearAside(cur + previousSuffix)
		if _, err := os.Stat(cur); err == nil {
			if err := os.Rename(cur, cur+previousSuffix); err != nil {
				undo()
				return fmt.Errorf("could not set %s aside: %w", name, err)
			}
		}
		if err := os.Rename(filepath.Join(staged, name), cur); err != nil {
			os.Rename(cur+previousSuffix, cur)
			undo()
			return fmt.Errorf("could not put the new %s in place: %w", name, err)
		}
		done = append(done, name)
	}
	os.Remove(staged)
	os.Remove(staged + readySuffix)
	return nil
}

// clearAside gets a kept file out of the way. Usually it can simply be
// deleted. If it is a program that is still shutting down, Windows will not
// delete it but will let it be renamed, so it is renamed and removed by a
// later Tidy.
func clearAside(p string) {
	if err := os.Remove(p); err == nil || os.IsNotExist(err) {
		return
	}
	os.Rename(p, fmt.Sprintf("%s.stale-%d", strings.TrimSuffix(p, previousSuffix), time.Now().UnixNano()))
}

// HasPrevious reports whether installDir holds a version to go back to.
func HasPrevious(installDir string) bool {
	m, _ := filepath.Glob(filepath.Join(installDir, "*"+previousSuffix))
	return len(m) > 0
}

// Rollback puts every "name.previous" back in place of "name". The files
// it displaces are kept as "name.failed" until the next update.
func Rollback(installDir string) error {
	prev, err := filepath.Glob(filepath.Join(installDir, "*"+previousSuffix))
	if err != nil {
		return err
	}
	if len(prev) == 0 {
		return errors.New("there is no earlier version to go back to")
	}
	for _, p := range prev {
		cur := strings.TrimSuffix(p, previousSuffix)
		if err := os.Remove(cur + ".failed"); err != nil && !os.IsNotExist(err) {
			os.Rename(cur+".failed", fmt.Sprintf("%s.stale-%d", cur, time.Now().UnixNano()))
		}
		if _, err := os.Stat(cur); err == nil {
			if err := os.Rename(cur, cur+".failed"); err != nil {
				return err
			}
		}
		if err := os.Rename(p, cur); err != nil {
			return err
		}
	}
	return nil
}

// Tidy removes what earlier updates left behind: old downloads, unpacked
// folders and files set aside by a rollback. One unpacked folder may stay:
// a version newer than current, the one that is running, that was made
// ready and is still waiting to be installed (see Ready, which checks the
// folder itself when the time comes).
func Tidy(installDir, current string) {
	for _, pattern := range []string{"download-*.zip", "*.failed", "*.stale-*", "update-*" + readySuffix + ".tmp"} {
		m, _ := filepath.Glob(filepath.Join(installDir, pattern))
		for _, p := range m {
			os.Remove(p)
		}
	}
	m, _ := filepath.Glob(filepath.Join(installDir, "update-*"))
	for _, p := range m {
		st, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			// A note whose folder is gone says nothing any more.
			if dir, isNote := strings.CutSuffix(p, readySuffix); isNote {
				if _, err := os.Stat(dir); err != nil {
					os.Remove(p)
				}
			}
			continue
		}
		if note, ok := readNote(p); ok && p == stagedDir(installDir, note.Version) && Newer(note.Version, current) {
			continue
		}
		os.RemoveAll(p)
		os.Remove(p + readySuffix)
	}
}

// State is what the program remembers about its last update, so that it
// can tell the owner what happened and undo it if asked.
type State struct {
	From     string `json:"from"`
	To       string `json:"to"`
	At       int64  `json:"at"`
	DBBackup string `json:"dbBackup"`
	// SchemaBefore is the database layout the earlier version used. Going
	// back only needs the backup if the newer version changed it.
	SchemaBefore int    `json:"schemaBefore"`
	Result       string `json:"result"` // "pending", "ok", "failed", "undone"
	Problem      string `json:"problem,omitempty"`
}

func statePath(dataDir string) string { return filepath.Join(dataDir, "update-state.json") }

// ReadState returns the last update's record, or a zero State.
func ReadState(dataDir string) State {
	var s State
	if b, err := os.ReadFile(statePath(dataDir)); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

// WriteState stores the record.
func WriteState(dataDir string, s State) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := statePath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(dataDir))
}

// RestoreDatabase puts a backup back in place of the database. It is used
// when an update that changed the database has to be undone.
func RestoreDatabase(backup, dbPath string) error {
	if backup == "" {
		return nil
	}
	src, err := os.Open(backup)
	if err != nil {
		return fmt.Errorf("the backup is gone: %w", err)
	}
	defer src.Close()
	tmp := dbPath + ".restoring"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	// The write-ahead files belong to the database being replaced.
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
	return os.Rename(tmp, dbPath)
}
