package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLivePublishedVersionCanBeFetched does, against the real releases page,
// what an installed copy on Windows does when it looks for an update: ask
// for the newest version, download its package, check it against the
// release's checksum list, and unpack it. It needs the internet and fetches
// about 75 MB, so it only runs when asked:
//
//	LM_LIVE_UPDATE=1 go test ./internal/update -run TestLive -v
func TestLivePublishedVersionCanBeFetched(t *testing.T) {
	if os.Getenv("LM_LIVE_UPDATE") == "" {
		t.Skip("set LM_LIVE_UPDATE=1 to fetch the published version from GitHub")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	src := GitHub("Matiaspnyhus/Local-media-project", "LocalMedia-Windows.zip")
	// A copy older than anything published must be offered the newest one.
	rel, err := src.Latest(ctx, "v0.0.1")
	if err != nil {
		t.Fatalf("asking for the newest version: %v", err)
	}
	if rel == nil {
		t.Fatal("no version newer than v0.0.1 is published")
	}
	t.Logf("newest published version: %s", rel.Version)
	// A copy that already is that version must be left alone.
	if same, err := src.Latest(ctx, rel.Version); err != nil || same != nil {
		t.Fatalf("a copy that is already %s was offered %+v (%v)", rel.Version, same, err)
	}
	dir := t.TempDir()
	zipPath, err := src.Download(ctx, rel, dir)
	if err != nil {
		t.Fatalf("downloading %s: %v", rel.Asset, err)
	}
	install := filepath.Join(dir, "install")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	staged, err := Unpack(zipPath, install, rel.Version)
	if err != nil {
		t.Fatalf("unpacking: %v", err)
	}
	for _, name := range []string{"localmedia.exe", "ffmpeg.exe", "ffprobe.exe", "LICENSE.txt", "THIRD-PARTY.txt", "THIRD-PARTY-LICENSES.txt"} {
		st, err := os.Stat(filepath.Join(staged, name))
		if err != nil || st.Size() == 0 {
			t.Errorf("the package lacks %s (%v)", name, err)
		}
	}
}
