package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteSampleDatabase saves a small database, as this version writes it,
// into internal/db/testdata. Run it when the schema changes:
//
//	LM_WRITE_SAMPLE=1 go test ./internal/server -run TestWriteSampleDatabase
//
// Later versions must upgrade every saved sample without losing its data
// (see internal/db/samples_test.go).
func TestWriteSampleDatabase(t *testing.T) {
	if os.Getenv("LM_WRITE_SAMPLE") == "" {
		t.Skip("set LM_WRITE_SAMPLE=1 to rewrite the sample database")
	}
	e := newEnv(t)
	c := e.setup()
	id := e.firstItem(c, "clip")
	if w := e.do("PUT", "/api/items/"+id+"/progress", remote, c, map[string]int64{"positionMs": 42000, "durationMs": 100000}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	// The sample must not carry anything from the machine that made it.
	e.s.db.Exec(`UPDATE libraries SET path='/sample/media', name='media'`)
	e.s.db.Exec(`DELETE FROM sessions`)
	e.s.db.Exec(`UPDATE users SET password_hash='sample'`)
	e.s.db.Exec(`DELETE FROM settings`)
	out, _ := filepath.Abs(filepath.Join("..", "db", "testdata", "schema-001.db"))
	os.MkdirAll(filepath.Dir(out), 0o755)
	if _, err := os.Stat(out); err == nil {
		t.Fatalf("%s already exists; a released sample is never rewritten. Move it aside by hand if this schema was never released.", out)
	}
	if _, err := e.s.db.Exec(`VACUUM INTO ?`, filepath.ToSlash(out)); err != nil {
		t.Fatal(err)
	}
}
