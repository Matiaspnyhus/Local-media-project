package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCreatesSchemaWithWALAndFTS5(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var mode string
	if err := d.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}
	var v int
	d.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != SchemaVersion() {
		t.Fatalf("user_version = %d, want %d", v, SchemaVersion())
	}
	if _, err := d.Exec(`INSERT INTO item_fts(item_id, title) VALUES ('a','Amélie'), ('b','The Matrix Reloaded')`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := d.QueryRow(`SELECT item_id FROM item_fts WHERE item_fts MATCH 'amelie'`).Scan(&id); err != nil || id != "a" {
		t.Fatalf("fts5 diacritics search: %q, %v", id, err)
	}
	if err := d.QueryRow(`SELECT item_id FROM item_fts WHERE item_fts MATCH 'matr*' ORDER BY rank`).Scan(&id); err != nil || id != "b" {
		t.Fatalf("fts5 prefix search: %q, %v", id, err)
	}
}

func TestDataSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO settings(key,value) VALUES('k','v')`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var v string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key='k'`).Scan(&v); err != nil || v != "v" {
		t.Fatalf("got %q, %v", v, err)
	}
}

// An older database must be backed up before it is upgraded, and a failing
// upgrade must leave it exactly as it was.
func TestUpgradeBacksUpFirstAndFailureChangesNothing(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(`INSERT INTO settings(key,value) VALUES('watched','yes')`)
	d.Close()

	orig := migrations
	defer func() { migrations = orig }()

	// A good upgrade: backup appears, data kept, version bumped.
	migrations = append(append([]string{}, orig...), `CREATE TABLE extra (x INTEGER);`)
	d, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "*.db"))
	if len(backups) != 1 {
		t.Fatalf("want 1 backup, got %v", backups)
	}
	b, err := sql.Open("sqlite", "file:"+filepath.ToSlash(backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := b.QueryRow(`SELECT value FROM settings WHERE key='watched'`).Scan(&v); err != nil || v != "yes" {
		t.Fatalf("backup lacks data: %q, %v", v, err)
	}
	b.Close()

	// A broken upgrade: error, version unchanged, data intact.
	migrations = append(append([]string{}, migrations...), `CREATE TABLE ok (x INTEGER); THIS IS NOT SQL;`)
	if _, err := Open(dir); err == nil {
		t.Fatal("broken upgrade did not fail")
	}
	migrations = migrations[:len(migrations)-1]
	d, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.QueryRow(`SELECT value FROM settings WHERE key='watched'`).Scan(&v); err != nil || v != "yes" {
		t.Fatalf("data lost after failed upgrade: %q, %v", v, err)
	}
	var n int
	d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='ok'`).Scan(&n)
	if n != 0 {
		t.Fatal("failed upgrade left a table behind")
	}
}

func TestNewerDatabaseIsRefused(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d.Exec(`PRAGMA user_version = 9999`)
	d.Close()
	if _, err := Open(dir); err == nil {
		t.Fatal("opened a database from the future")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
}

// While a long write is in progress (a scan of a big library), pages must
// still load, and a second writer (someone's watch progress) must wait its
// turn rather than fail.
func TestReadsAndASecondWriterAreNotBlockedOut(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO settings(key,value) VALUES('seen','before')`)

	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Exec(`INSERT INTO settings(key,value) VALUES('scan','in progress')`)

	read := make(chan string, 1)
	go func() {
		var v string
		d.QueryRow(`SELECT value FROM settings WHERE key='seen'`).Scan(&v)
		read <- v
	}()
	select {
	case v := <-read:
		if v != "before" {
			t.Fatalf("read %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a read waited for the writer")
	}

	wrote := make(chan error, 1)
	go func() {
		_, err := d.Exec(`INSERT INTO settings(key,value) VALUES('progress','saved')`)
		wrote <- err
	}()
	select {
	case err := <-wrote:
		t.Fatalf("the second writer did not wait: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("the second writer failed after waiting: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second writer never finished")
	}
}
