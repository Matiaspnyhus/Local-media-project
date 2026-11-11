// Package db opens the SQLite database and upgrades its schema.
//
// Rules fixed in M0: the database lives on local disk in the data folder,
// it is copied to backups/ before any schema upgrade, and upgrades only
// move forward.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// FileName is the database file inside the data folder.
const FileName = "localmedia.db"

// migrations are applied in order; the index+1 is the schema version stored
// in PRAGMA user_version. Never edit a released entry: add a new one.
var migrations = []string{
	// 1: M0 schema. item -> versions -> files is the model for all media.
	`
CREATE TABLE settings (key TEXT PRIMARY KEY, value BLOB NOT NULL);

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('owner','member')),
  created_at INTEGER NOT NULL
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_name TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  revoked_at INTEGER
);

CREATE TABLE libraries (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
);

CREATE TABLE items (
  id TEXT PRIMARY KEY,
  library_id TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  parent_id TEXT REFERENCES items(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  title TEXT NOT NULL,
  sort_title TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX items_library ON items(library_id, sort_title);
CREATE INDEX items_parent ON items(parent_id);

CREATE TABLE versions (
  id TEXT PRIMARY KEY,
  item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  label TEXT NOT NULL DEFAULT ''
);
CREATE INDEX versions_item ON versions(item_id);

CREATE TABLE files (
  id TEXT PRIMARY KEY,
  version_id TEXT NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
  library_id TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  part_index INTEGER NOT NULL DEFAULT 0,
  rel_path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime INTEGER NOT NULL,
  missing INTEGER NOT NULL DEFAULT 0,
  UNIQUE (library_id, rel_path)
);
CREATE INDEX files_version ON files(version_id);

CREATE TABLE progress (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  position_ms INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL,
  played INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, item_id)
);

CREATE VIRTUAL TABLE item_fts USING fts5(item_id UNINDEXED, title, tokenize='unicode61 remove_diacritics 2');
`,
}

// SchemaVersion is the version this build expects.
func SchemaVersion() int { return len(migrations) }

// Open opens (creating if needed) the database in dataDir and brings its
// schema up to date, backing the file up first when an upgrade is needed.
func Open(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, FileName)
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: every write is serialised, which is the simple and
	// safe choice for a household server. Revisit with a read pool in M1.
	d.SetMaxOpenConns(1)
	if err := migrate(d, dataDir); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func migrate(d *sql.DB, dataDir string) error {
	var have int
	if err := d.QueryRow(`PRAGMA user_version`).Scan(&have); err != nil {
		return err
	}
	want := len(migrations)
	if have > want {
		return fmt.Errorf("database is from a newer version (schema %d, this program knows %d); install the newer program or restore a backup", have, want)
	}
	if have == want {
		return nil
	}
	if have > 0 {
		if _, err := Backup(d, dataDir, have); err != nil {
			return fmt.Errorf("backup before upgrade failed, nothing was changed: %w", err)
		}
	}
	for v := have; v < want; v++ {
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("upgrade to schema %d failed and was rolled back: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Backup writes a consistent copy of the database into dataDir/backups and
// returns its path.
func Backup(d *sql.DB, dataDir string, schema int) (string, error) {
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("localmedia-schema%d-%s.db", schema, time.Now().UTC().Format("20060102-150405"))
	out := filepath.Join(dir, name)
	if _, err := d.Exec(`VACUUM INTO ?`, filepath.ToSlash(out)); err != nil {
		return "", err
	}
	return out, nil
}
