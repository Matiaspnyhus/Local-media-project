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
