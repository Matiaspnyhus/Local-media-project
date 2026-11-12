package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
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
	// What schema 7 added: a device, what was learned about it (one lesson
	// about a kind of film, one about a single file), what was found about
	// a film's picture, and a viewer's choice for subtitles with a design.
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := e.s.db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if w := e.do("PUT", "/api/prefs", remote, c, map[string]string{"audioLang": "en", "subLang": "", "subs": "auto", "styled": "plain"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var device, file string
	if err := e.s.db.QueryRow(`SELECT device_id FROM sessions`).Scan(&device); err != nil {
		t.Fatal(err)
	}
	if err := e.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, id).Scan(&file); err != nil {
		t.Fatal(err)
	}
	must(`UPDATE devices SET label='Windows PC, Chrome', created_at=1700000000, last_seen_at=1700000000`)
	must(`INSERT INTO lessons(id,device_id,kind,value,file_id,hits,learned_at,expires_at) VALUES(?,?,'picture','hevc/10/4k',NULL,2,1700000000,1702592000)`, newID(), device)
	must(`INSERT INTO lessons(id,device_id,kind,value,file_id,hits,learned_at,expires_at) VALUES(?,?,'height','720',?,1,1700000000,1702592000)`, newID(), device, file)
	facts, err := json.Marshal(hls.PictureFacts{LastMs: 99960, EndMs: 100000, RateNum: 25, RateDen: 1, Width: 1920, Height: 1080, SarNum: 1, SarDen: 1, Format: "mov,mp4,m4a,3gp,3g2,mj2"})
	if err != nil {
		t.Fatal(err)
	}
	must(`INSERT INTO picture_facts(file_id,stamp,facts) VALUES(?,'20-1700000000-f1',?)`, file, string(facts))

	// The sample must not carry anything from the machine that made it.
	e.s.db.Exec(`UPDATE libraries SET path='/sample/media', name='media'`)
	e.s.db.Exec(`DELETE FROM sessions`)
	e.s.db.Exec(`UPDATE users SET password_hash='sample'`)
	e.s.db.Exec(`DELETE FROM settings`)
	// The scan had SQLite measure its tables, and the measurements quote
	// samples of what was in them then, the real folder included. Measure
	// again now that it is gone.
	e.s.db.Exec(`ANALYZE`)
	out, _ := filepath.Abs(filepath.Join("..", "db", "testdata", fmt.Sprintf("schema-%03d.db", db.SchemaVersion())))
	os.MkdirAll(filepath.Dir(out), 0o755)
	if _, err := os.Stat(out); err == nil {
		t.Fatalf("%s already exists; a released sample is never rewritten. Move it aside by hand if this schema was never released.", out)
	}
	if _, err := e.s.db.Exec(`VACUUM INTO ?`, filepath.ToSlash(out)); err != nil {
		t.Fatal(err)
	}
}
