package db

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Every released schema has a sample database in testdata. This version must
// open each one, upgrade it, and still find what the owner had watched.
func TestEverySampleDatabaseUpgrades(t *testing.T) {
	samples, _ := filepath.Glob(filepath.Join("testdata", "schema-*.db"))
	if len(samples) == 0 {
		t.Fatal("no sample databases in testdata; run LM_WRITE_SAMPLE=1 go test ./internal/server -run TestWriteSampleDatabase")
	}
	for _, sample := range samples {
		t.Run(filepath.Base(sample), func(t *testing.T) {
			dir := t.TempDir()
			src, err := os.Open(sample)
			if err != nil {
				t.Fatal(err)
			}
			dst, _ := os.Create(filepath.Join(dir, FileName))
			io.Copy(dst, src)
			src.Close()
			dst.Close()

			d, err := Open(dir)
			if err != nil {
				t.Fatalf("upgrade failed: %v", err)
			}
			defer d.Close()
			var v int
			d.QueryRow(`PRAGMA user_version`).Scan(&v)
			if v != SchemaVersion() {
				t.Fatalf("schema %d after upgrade, want %d", v, SchemaVersion())
			}
			var pos int64
			var title string
			err = d.QueryRow(`SELECT p.position_ms, i.title FROM progress p JOIN items i ON i.id = p.item_id`).Scan(&pos, &title)
			if err != nil || pos != 42000 || title != "clip" {
				t.Fatalf("watch progress lost in upgrade: %d %q %v", pos, title, err)
			}
		})
	}
}
