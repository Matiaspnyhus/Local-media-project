package db

import (
	"database/sql"
	"fmt"
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
	if len(samples) != SchemaVersion() {
		t.Errorf("%d sample databases for %d schema versions; run LM_WRITE_SAMPLE=1 go test ./internal/server -run TestWriteSampleDatabase", len(samples), SchemaVersion())
	}
	for _, sample := range samples {
		t.Run(filepath.Base(sample), func(t *testing.T) {
			var from int
			if _, err := fmt.Sscanf(filepath.Base(sample), "schema-%03d.db", &from); err != nil {
				t.Fatalf("the sample's name does not say which schema it is: %v", err)
			}
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
			if from >= 7 {
				learnedSurvived(t, d)
			}
			devicesAndLessonsWork(t, d)
		})
	}
}

// learnedSurvived checks what a sample from schema 7 on holds about a
// device: its kind, what was learned about it, what was found about a
// film's picture, and the viewer's choice for subtitles with a design.
func learnedSurvived(t *testing.T, d *sql.DB) {
	t.Helper()
	var label string
	if err := d.QueryRow(`SELECT label FROM devices`).Scan(&label); err != nil || label != "Windows PC, Chrome" {
		t.Errorf("the device was lost in the upgrade: %q %v", label, err)
	}
	var about, one, hits int
	err := d.QueryRow(`SELECT count(*), count(file_id), sum(hits) FROM lessons l JOIN devices dv ON dv.id = l.device_id
		WHERE l.kind IN ('picture','height') AND l.expires_at > l.learned_at`).Scan(&about, &one, &hits)
	if err != nil || about != 2 || one != 1 || hits != 3 {
		t.Errorf("what was learned about the device was lost in the upgrade: %d lessons, %d of them about one file, reported %d times, %v", about, one, hits, err)
	}
	var kept int
	if err := d.QueryRow(`SELECT count(*) FROM lessons l JOIN files f ON f.id = l.file_id JOIN picture_facts p ON p.file_id = f.id
		WHERE p.facts LIKE '{%'`).Scan(&kept); err != nil || kept != 1 {
		t.Errorf("the lesson about one file, or what was found about that file's picture, was lost in the upgrade: %d %v", kept, err)
	}
	var styled string
	if err := d.QueryRow(`SELECT styled FROM prefs`).Scan(&styled); err != nil || styled != "plain" {
		t.Errorf("the viewer's choice for subtitles with a design was lost in the upgrade: %q %v", styled, err)
	}
}

// devicesAndLessonsWork checks, on a database upgraded from any schema,
// that what schema 7 added can be used and cleans up after itself: a
// lesson about one file goes with the file, everything about a device goes
// with the device, and a sign-in outlives its device.
func devicesAndLessonsWork(t *testing.T, d *sql.DB) {
	t.Helper()
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := d.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	var user, file string
	if err := d.QueryRow(`SELECT id FROM users LIMIT 1`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id = f.version_id JOIN progress p ON p.item_id = v.item_id`).Scan(&file); err != nil {
		t.Fatal(err)
	}
	must(`INSERT INTO devices(id,label,created_at,last_seen_at) VALUES('dev-a','iPhone',1,1)`)
	must(`INSERT INTO sessions(id,token_hash,user_id,created_at,last_seen_at,device_id) VALUES('sess-a','hash-a',?,1,1,'dev-a')`, user)
	must(`INSERT INTO sessions(id,token_hash,user_id,created_at,last_seen_at) VALUES('sess-old','hash-old',?,1,1)`, user)
	must(`INSERT INTO lessons(id,device_id,kind,value,file_id,learned_at,expires_at) VALUES('one','dev-a','picture','hevc/10/4k',?,1,2)`, file)
	must(`INSERT INTO lessons(id,device_id,kind,value,learned_at,expires_at) VALUES('all','dev-a','rate','5000000',1,2)`)
	must(`INSERT INTO picture_facts(file_id,stamp,facts) VALUES(?,'1-2','{}') ON CONFLICT(file_id) DO UPDATE SET stamp=excluded.stamp`, file)
	if _, err := d.Exec(`INSERT INTO lessons(id,device_id,kind,value,learned_at,expires_at) VALUES('twice','dev-a','rate','5000000',1,2)`); err == nil {
		t.Error("the same lesson could be kept twice for one device")
	}
	if _, err := d.Exec(`INSERT INTO lessons(id,device_id,kind,value,learned_at,expires_at) VALUES('stray','no-such-device','rate','1',1,2)`); err == nil {
		t.Error("a lesson could be kept about a device that does not exist")
	}
	if n := count(`SELECT count(*) FROM sessions WHERE id='sess-old' AND device_id IS NULL`); n != 1 {
		t.Error("a sign-in made without a device does not have none")
	}
	if n := count(`SELECT count(*) FROM prefs WHERE styled NOT IN ('designed','plain')`); n != 0 {
		t.Errorf("%d viewers have a choice for subtitles with a design that is neither of the two", n)
	}

	// The file is taken out of the library: what was kept about that one
	// file goes with it, and what was learned about the kind stays.
	must(`DELETE FROM files WHERE id=?`, file)
	if n := count(`SELECT count(*) FROM lessons WHERE id='one'`) + count(`SELECT count(*) FROM picture_facts WHERE file_id=?`, file); n != 0 {
		t.Errorf("%d rows about a file outlived the file", n)
	}
	if n := count(`SELECT count(*) FROM lessons WHERE id='all'`); n != 1 {
		t.Error("a lesson about every film went with one file")
	}
	// The device goes: its lessons go, its sign-in stays signed in.
	must(`DELETE FROM devices WHERE id='dev-a'`)
	if n := count(`SELECT count(*) FROM lessons WHERE device_id='dev-a'`); n != 0 {
		t.Errorf("%d lessons outlived their device", n)
	}
	if n := count(`SELECT count(*) FROM sessions WHERE id='sess-a' AND device_id IS NULL`); n != 1 {
		t.Error("a sign-in did not outlive its device, or still points at it")
	}
}
