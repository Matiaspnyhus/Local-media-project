package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
)

func write(t *testing.T, p string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanAddsMarksMissingAndNeverTouchesMedia(t *testing.T) {
	media := t.TempDir()
	write(t, filepath.Join(media, "Films", "Some.Movie.2019.mkv"))
	write(t, filepath.Join(media, "clip one.MP4"))
	write(t, filepath.Join(media, "notes.txt"))
	write(t, filepath.Join(media, ".hidden", "secret.mp4"))
	outside := t.TempDir()
	write(t, filepath.Join(outside, "outside.mp4"))
	os.Symlink(filepath.Join(outside, "outside.mp4"), filepath.Join(media, "link.mp4"))

	before := snapshot(t, media)

	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	lib, err := Add(d, media)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Scan(d, lib)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || res.Total != 2 {
		t.Fatalf("first scan: %+v (want 2 added: hidden folder, text file and link skipped)", res)
	}
	res, _ = Scan(d, lib)
	if res.Added != 0 {
		t.Fatalf("rescan added %d", res.Added)
	}

	var title string
	d.QueryRow(`SELECT i.title FROM items i JOIN versions v ON v.item_id=i.id JOIN files f ON f.version_id=v.id WHERE f.rel_path='Films/Some.Movie.2019.mkv'`).Scan(&title)
	if title != "Some Movie 2019" {
		t.Fatalf("title %q", title)
	}

	os.Remove(filepath.Join(media, "clip one.MP4"))
	res, _ = Scan(d, lib)
	if res.Missing != 1 {
		t.Fatalf("missing: %+v", res)
	}
	var items int
	d.QueryRow(`SELECT count(*) FROM items`).Scan(&items)
	if items != 2 {
		t.Fatalf("a missing file's item was deleted (items=%d); its watch progress would be lost", items)
	}
	write(t, filepath.Join(media, "clip one.MP4"))
	res, _ = Scan(d, lib)
	if res.Added != 0 || res.Missing != 0 {
		t.Fatalf("file came back: %+v", res)
	}

	after := snapshot(t, media)
	if len(before) != len(after) {
		t.Fatalf("scan changed the media folder: before %v after %v", before, after)
	}
	if _, err := Add(d, media); err == nil {
		t.Fatal("same folder added twice")
	}
}

func snapshot(t *testing.T, root string) []string {
	var out []string
	filepath.Walk(root, func(p string, _ os.FileInfo, _ error) error { out = append(out, p); return nil })
	return out
}

func TestListDirs(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "b"), 0o755)
	os.Mkdir(filepath.Join(root, "A"), 0o755)
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	write(t, filepath.Join(root, "file.mp4"))
	l, err := ListDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Dirs) != 2 || l.Dirs[0].Name != "A" || l.Parent == "" {
		t.Fatalf("%+v", l)
	}
	if l, _ := ListDirs(""); len(l.Dirs) == 0 {
		t.Fatal("no starting points")
	}
	if _, err := ListDirs(filepath.Join(root, "nope")); err == nil {
		t.Fatal("listed a folder that does not exist")
	}
}
