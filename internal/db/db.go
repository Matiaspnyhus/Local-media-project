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
	// 2: M1 library. Items become films, shows, seasons and episodes; each
	// file remembers what ffprobe said about it, so later versions never
	// need to re-read the owner's whole library.
	`
ALTER TABLE items ADD COLUMN year INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN season_number INTEGER;
ALTER TABLE items ADD COLUMN episode_number INTEGER;
ALTER TABLE items ADD COLUMN episode_end INTEGER;
ALTER TABLE items ADD COLUMN overview TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN name_source TEXT NOT NULL DEFAULT '';
CREATE INDEX items_kind ON items(kind, sort_title);

-- 0 = not inspected yet, 1 = inspected, 2 = could not be read
ALTER TABLE files ADD COLUMN probed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE files ADD COLUMN probed_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE files ADD COLUMN probed_mtime INTEGER NOT NULL DEFAULT 0;
ALTER TABLE files ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE files ADD COLUMN container TEXT NOT NULL DEFAULT '';
ALTER TABLE files ADD COLUMN bit_rate INTEGER NOT NULL DEFAULT 0;
-- size plus a hash of the first and last 64 KB: recognises a moved file
ALTER TABLE files ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';
CREATE INDEX files_fingerprint ON files(fingerprint);

CREATE TABLE streams (
  file_id TEXT NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  idx INTEGER NOT NULL,
  kind TEXT NOT NULL,              -- video, audio, subtitle
  codec TEXT NOT NULL DEFAULT '',
  profile TEXT NOT NULL DEFAULT '',
  level INTEGER NOT NULL DEFAULT 0,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  pix_fmt TEXT NOT NULL DEFAULT '',
  bit_depth INTEGER NOT NULL DEFAULT 0,
  color_transfer TEXT NOT NULL DEFAULT '',
  color_primaries TEXT NOT NULL DEFAULT '',
  frame_rate TEXT NOT NULL DEFAULT '',
  channels INTEGER NOT NULL DEFAULT 0,
  channel_layout TEXT NOT NULL DEFAULT '',
  sample_rate INTEGER NOT NULL DEFAULT 0,
  bit_rate INTEGER NOT NULL DEFAULT 0,
  language TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  is_default INTEGER NOT NULL DEFAULT 0,
  is_forced INTEGER NOT NULL DEFAULT 0,
  is_commentary INTEGER NOT NULL DEFAULT 0,
  is_hearing_impaired INTEGER NOT NULL DEFAULT 0,
  side_data TEXT NOT NULL DEFAULT '',   -- e.g. "dovi:5" for Dolby Vision profile 5
  PRIMARY KEY (file_id, idx)
);

-- subtitle files that sit next to a video ("Film.en.srt")
CREATE TABLE sidecars (
  id TEXT PRIMARY KEY,
  file_id TEXT NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  rel_path TEXT NOT NULL,
  format TEXT NOT NULL,
  language TEXT NOT NULL DEFAULT '',
  is_forced INTEGER NOT NULL DEFAULT 0,
  UNIQUE (file_id, rel_path)
);
`,
	// 3: online lookups. match_key is the title as the file names it,
	// which is what the scan matches on; the title shown may then be
	// replaced by the proper one from TMDB without confusing the scan.
	`
ALTER TABLE items ADD COLUMN match_key TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN tmdb_id INTEGER NOT NULL DEFAULT 0;
-- 0 = not looked up yet, 1 = found, 2 = looked up and not found
ALTER TABLE items ADD COLUMN lookup_state INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN lookup_at INTEGER NOT NULL DEFAULT 0;
-- file name of the poster in the artwork folder, '' when there is none
ALTER TABLE items ADD COLUMN poster TEXT NOT NULL DEFAULT '';
`,
	// 4: speed at 50,000 files. Each episode remembers its show directly,
	// so a show's episodes are one index lookup instead of two joins.
	`
ALTER TABLE items ADD COLUMN show_id TEXT;
UPDATE items SET show_id = (SELECT se.parent_id FROM items se WHERE se.id = items.parent_id) WHERE kind = 'episode';
CREATE INDEX items_show ON items(show_id, season_number, episode_number);
-- only the files still waiting to be inspected, so the planner is never
-- tempted to start a library query from this index
CREATE INDEX files_pending ON files(probed) WHERE probed = 0;
CREATE INDEX items_lookup ON items(lookup_state, kind);
CREATE INDEX progress_recent ON progress(user_id, updated_at);
`,
	// 5: playing files a browser cannot take as they are. What each viewer
	// prefers, what they last chose for a show (or a film), and where each
	// file can be cut into chunks, which is worked out once and kept.
	`
CREATE TABLE prefs (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  audio_lang TEXT NOT NULL DEFAULT '',
  sub_lang TEXT NOT NULL DEFAULT '',
  subs TEXT NOT NULL DEFAULT 'auto'
);
CREATE TABLE track_memory (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  scope_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,  -- the show, or the film itself
  audio_lang TEXT NOT NULL DEFAULT '',
  sub_lang TEXT NOT NULL DEFAULT '',
  subs_on INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, scope_id)
);
CREATE TABLE cuts (
  file_id TEXT PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
  stamp TEXT NOT NULL,   -- the file's size and date when the cuts were found
  grid TEXT NOT NULL
);
`,
	// 6: what the review of M3 found. What each signed-in device said it can
	// play is kept with its sign-in, so a page left open still gets the
	// right treatment after the program restarts.
	`
CREATE TABLE device_reports (
  session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  caps TEXT NOT NULL,                   -- the report as the device sent it
  user_agent TEXT NOT NULL DEFAULT '',  -- how the browser describes itself
  updated_at INTEGER NOT NULL
);

-- What a viewer picked is kept as the pick itself and its kind, not only
-- as two languages: a language does not say which of two English sound
-- tracks it was, or whether the subtitles were the full or the forced ones.
-- Rows from before this have sub_kind '' and are read as languages only.
ALTER TABLE track_memory ADD COLUMN audio_index INTEGER NOT NULL DEFAULT -1;  -- the sound track, counted from 0
ALTER TABLE track_memory ADD COLUMN audio_kind TEXT NOT NULL DEFAULT '';      -- '' ordinary sound, 'commentary'
ALTER TABLE track_memory ADD COLUMN sub_key TEXT NOT NULL DEFAULT '';         -- the subtitle as the page names it, '' for none
ALTER TABLE track_memory ADD COLUMN sub_kind TEXT NOT NULL DEFAULT '';        -- 'full', 'described', 'forced', 'off', or 'untouched' (only sound was picked)

-- The four-letter label a file gives each codec ("hvc1", "hev1"). HEVC in
-- an MP4 plays on Apple devices under the first label and not the second.
ALTER TABLE streams ADD COLUMN codec_tag TEXT NOT NULL DEFAULT '';

-- Two kinds of file are inspected again, because what was stored about
-- them is wrong or lacks something now needed: files that may be WebM,
-- which used to be stored as MKV (Firefox and Safari play WebM and not
-- MKV), and MP4 or MOV files with an HEVC picture, whose label was not
-- kept. An inspected size that cannot match marks them for the next round
-- (see library.ProbePending). Until then they keep what was stored.
UPDATE files SET probed_size = -1 WHERE probed = 1 AND (
  (container = 'matroska' AND (rel_path LIKE '%.webm'
    OR id IN (SELECT file_id FROM streams WHERE kind = 'video' AND codec IN ('vp8','vp9','av1'))))
  OR (container IN ('mp4','mov') AND id IN (SELECT file_id FROM streams WHERE kind = 'video' AND codec = 'hevc')));
`,
	// 7: converting the picture. A device is a browser on a phone, a PC or a
	// TV, told apart by a cookie that outlives signing out. What trouble
	// while playing has taught about a device is kept as lessons, so the
	// next film does not run into the same trouble.
	`
CREATE TABLE devices (
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL DEFAULT '',   -- a kind of device ("iPhone", "Windows PC, Chrome"), never a name
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
);

-- The device a sign-in was made on. Sign-ins from before this have none
-- until they are next used.
ALTER TABLE sessions ADD COLUMN device_id TEXT REFERENCES devices(id) ON DELETE SET NULL;
CREATE INDEX sessions_device ON sessions(device_id);

-- One row for each thing learned about a device: a kind of picture it had
-- trouble showing, a cap on what it is sent, and so on. A lesson names a
-- kind of film, never a title. While file_id is set it holds for that one
-- file only (the first failure may have been the file's fault); a second
-- file makes it hold for the kind, and file_id is then NULL. A lesson
-- about one file goes when the file's row does.
CREATE TABLE lessons (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,               -- 'picture', 'height', 'rate', 'hdr', 'draw'
  value TEXT NOT NULL DEFAULT '',   -- which picture, how many lines, how many bits a second, which drawing
  file_id TEXT REFERENCES files(id) ON DELETE CASCADE,
  hits INTEGER NOT NULL DEFAULT 1,  -- how many times this trouble was reported
  learned_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,      -- kept for a while after this, so that a lesson learned again lasts longer
  UNIQUE (device_id, kind, value)
);
CREATE INDEX lessons_file ON lessons(file_id);

-- What converting a film's picture starts from (where its clock stands,
-- how fast its frames come, their shape), found by a short look at the
-- file and kept for as long as the file stays the same.
CREATE TABLE picture_facts (
  file_id TEXT PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
  stamp TEXT NOT NULL,   -- the file's size and date when it was looked at
  facts TEXT NOT NULL
);

-- What to do with subtitles that have their own design where the page
-- cannot draw them: 'designed' puts them into the picture, 'plain' shows
-- their words as plain text.
ALTER TABLE prefs ADD COLUMN styled TEXT NOT NULL DEFAULT 'designed';
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
	// WAL lets readers carry on while one writer writes. Every transaction
	// takes the write lock at its start (_txlock=immediate) and waits its
	// turn (busy_timeout), so two writers queue instead of failing.
	dsn := "file:" + filepath.ToSlash(path) +
		"?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A few connections: a long scan no longer makes every page wait.
	d.SetMaxOpenConns(4)
	d.SetMaxIdleConns(4)
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
