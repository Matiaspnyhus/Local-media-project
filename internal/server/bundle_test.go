package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a player reports is kept for the support bundle. It must leave the
// PC scrubbed the same way the log is: no addresses, nothing long enough to
// be a secret.
func TestProblemsInTheSupportBundleAreScrubbedLikeTheLog(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	id := e.firstItem(c, "clip")
	e.do("POST", "/api/client-events", remote, c, map[string]string{"kind": "play-error", "itemId": id,
		"detail": "could not load http://192.168.1.50:8177/stream/abc?g=" + strings.Repeat("k", 43) + " MEDIA_ERR_NETWORK"})
	files := bundleFiles(t, e.do("GET", "/api/support-bundle", remote, c, nil))
	problems := files["playback-problems.json"]
	if !strings.Contains(problems, "MEDIA_ERR_NETWORK") || !strings.Contains(problems, "clip.mp4") {
		t.Fatalf("the problem itself is missing from the bundle: %s", problems)
	}
	for name, text := range files {
		for _, bad := range []string{"192.168.1.50", strings.Repeat("k", 43)} {
			if strings.Contains(text, bad) {
				t.Errorf("%s in the bundle contains %q", name, bad)
			}
		}
	}
}

// A subtitle track taken out of a film is kept for next time. If the copy
// could not be written in full (the disk filled up), it must not be kept:
// a short copy would be served to everyone from then on, with the film's
// subtitles stopping partway through and nothing to say why.
func TestASubtitleThatCouldNotBeWrittenInFullIsNotKept(t *testing.T) {
	e := newConvertEnv(t)
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Test Film")
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if len(p.Subtitles) == 0 || p.Subtitles[0].Key != "e0" {
		t.Fatalf("subtitles: %+v", p.Subtitles)
	}
	dir := filepath.Join(e.s.dataDir, "cache", "subtitles")
	inFolder := func() (kept, other []string) {
		entries, _ := os.ReadDir(dir)
		for _, f := range entries {
			if strings.HasSuffix(f.Name(), ".vtt") {
				kept = append(kept, f.Name())
			} else {
				other = append(other, f.Name())
			}
		}
		return kept, other
	}
	whole := func(when string) {
		t.Helper()
		w := e.do("GET", p.Subtitles[0].URL, remote, c, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Hello there") || !strings.Contains(w.Body.String(), "Goodbye") {
			t.Fatalf("%s the viewer got %d %q", when, w.Code, w.Body)
		}
	}

	// The disk is full: the file for the copy can be made, and every write
	// to it fails. (A file opened for reading only refuses writes too.)
	real := subtitleTemp
	subtitleTemp = func(dir, pattern string) (*os.File, error) {
		f, err := real(dir, pattern)
		if err != nil {
			return nil, err
		}
		f.Close()
		return os.Open(f.Name())
	}
	defer func() { subtitleTemp = real }()
	whole("while the copy could not be written,")
	if kept, other := inFolder(); len(kept) != 0 || len(other) != 0 {
		t.Fatalf("a copy that could not be written was kept, or its leftovers were: %v %v", kept, other)
	}

	// With room on the disk again, the subtitle is taken out afresh, whole,
	// and kept this time.
	subtitleTemp = real
	whole("afterwards")
	if kept, other := inFolder(); len(kept) != 1 || len(other) != 0 {
		t.Fatalf("after a good run the folder holds %v and %v, want one kept copy", kept, other)
	}
	whole("from the kept copy")
}

// When the converter cannot open a film (its drive dropped out, or it was
// moved while playing), ffmpeg names the file by its full path. On the
// owner's PC that path holds their account name, or the name or address of
// their network drive. Only the file's own name may reach the log and the
// support bundle.
func TestAFilmThatVanishesLeavesNoFolderNamesInTheLogOrTheBundle(t *testing.T) {
	e := newConvertEnv(t)
	// The library is on something that looks like a share on a NAS.
	lib := filepath.Join(t.TempDir(), "192.168.1.20", "Household Videos")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	made, _ := os.ReadDir(e.media)
	for _, f := range made {
		if err := os.Rename(filepath.Join(e.media, f.Name()), filepath.Join(lib, f.Name())); err != nil {
			t.Fatal(err)
		}
	}
	e.media = lib
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Test Film")
	e.do("POST", "/api/capabilities", remote, c, map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true},
		"video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}, "hls": true})
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if !strings.HasPrefix(p.URL, "/hls/") {
		t.Fatalf("play: %+v", p)
	}
	// The film goes away while the player is open.
	src := filepath.Join(lib, "Test Film (2020).mkv")
	if err := os.Rename(src, src+".gone"); err != nil {
		t.Fatal(err)
	}
	if w := e.do("GET", strings.TrimSuffix(p.URL, "index.m3u8")+"seg-0.m4s", remote, nil, nil); w.Code != 500 {
		t.Fatalf("a chunk of a film that is gone answered %d %s", w.Code, w.Body)
	}

	files := bundleFiles(t, e.do("GET", "/api/support-bundle", remote, c, nil))
	if !strings.Contains(files["playback-problems.json"], "convert-failed") || !strings.Contains(files["playback-problems.json"], "Test Film (2020).mkv") {
		t.Errorf("the bundle does not say which film failed: %s", files["playback-problems.json"])
	}
	// The log as it lies on disk, before anything is scrubbed for a bundle.
	onDisk, err := os.ReadFile(filepath.Join(e.s.dataDir, "logs", "localmedia.log"))
	if err != nil {
		t.Fatal(err)
	}
	files["the log on disk"] = string(onDisk)
	if !strings.Contains(string(onDisk), "Test Film (2020).mkv") {
		t.Errorf("the log does not say which film failed:\n%s", onDisk)
	}
	for name, text := range files {
		for _, bad := range []string{lib, "Household Videos", e.s.dataDir} {
			if strings.Contains(text, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
		if name != "the log on disk" && strings.Contains(text, "192.168.1.20") {
			t.Errorf("%s contains the address 192.168.1.20", name)
		}
	}
}
