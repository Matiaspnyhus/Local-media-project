// Package library finds video files in the owner's folders and records them
// as films, shows, seasons and episodes. It only ever reads media folders;
// nothing in them is created, renamed or deleted.
package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/naming"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

var videoExt = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true,
	".avi": true, ".ts": true, ".m2ts": true, ".wmv": true, ".mpg": true, ".mpeg": true,
}

var subtitleExt = map[string]string{".srt": "srt", ".vtt": "vtt", ".ass": "ass", ".ssa": "ssa", ".sub": "sub"}

// IsVideo reports whether name looks like a video file.
func IsVideo(name string) bool { return videoExt[strings.ToLower(filepath.Ext(name))] }

// ScanResult summarises one scan.
type ScanResult struct {
	Added   int `json:"added"`
	Moved   int `json:"moved"`
	Missing int `json:"missing"`
	Total   int `json:"total"`
}

// Add registers a folder as a library and returns its ID.
func Add(d *sql.DB, folder string) (string, error) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", errors.New("that folder cannot be opened")
	}
	if !st.IsDir() {
		return "", errors.New("that is a file, not a folder")
	}
	id := ids.New()
	_, err = d.Exec(`INSERT INTO libraries(id,name,path,created_at) VALUES(?,?,?,?)`, id, filepath.Base(abs), abs, time.Now().Unix())
	if err != nil {
		return "", errors.New("that folder is already in the library")
	}
	return id, nil
}

type diskFile struct {
	size, mtime int64
}

type knownFile struct {
	id, itemID, fingerprint string
	size                    int64
	missing                 bool
}

// Scan walks one library folder and brings the database in line with it.
//
//   - A new file becomes a film or an episode, recognised from its path.
//   - A file that has disappeared is marked missing, not forgotten, so its
//     watch progress survives a drive that is unplugged for a while.
//   - A file that was renamed or moved keeps its item, and so stays watched.
func Scan(d *sql.DB, libraryID string) (ScanResult, error) {
	var res ScanResult
	var root string
	if err := d.QueryRow(`SELECT path FROM libraries WHERE id=?`, libraryID).Scan(&root); err != nil {
		return res, err
	}
	onDisk := map[string]diskFile{}
	subs := map[string][]string{} // folder -> subtitle file names
	aux := map[string][]string{}  // folder -> info files and pictures
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if e != nil && e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := e.Name()
		if e.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") || name == "System Volume Information" || name == "@eaDir") {
				return filepath.SkipDir
			}
			return nil
		}
		// Regular files only: links are not followed, so a link cannot
		// lead the server outside the folder the owner chose.
		if !e.Type().IsRegular() || strings.HasPrefix(name, ".") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		ext := strings.ToLower(filepath.Ext(name))
		if subtitleExt[ext] != "" {
			subs[path.Dir(rel)] = append(subs[path.Dir(rel)], name)
			return nil
		}
		if ext == ".nfo" || ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
			aux[path.Dir(rel)] = append(aux[path.Dir(rel)], name)
			return nil
		}
		if !videoExt[ext] || isExtra(rel) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		onDisk[rel] = diskFile{info.Size(), info.ModTime().Unix()}
		return nil
	})
	if err != nil {
		return res, err
	}

	known := map[string]*knownFile{}
	rows, err := d.Query(`SELECT f.rel_path, f.id, v.item_id, f.fingerprint, f.size, f.missing FROM files f JOIN versions v ON v.id=f.version_id WHERE f.library_id=?`, libraryID)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var rel string
		k := &knownFile{}
		var missing int
		rows.Scan(&rel, &k.id, &k.itemID, &k.fingerprint, &k.size, &missing)
		k.missing = missing != 0
		known[rel] = k
	}
	rows.Close()

	// Fingerprint what is new before opening the transaction: it reads the
	// disk, and the database should not be held while that happens.
	var newRels []string
	for rel := range onDisk {
		if known[rel] == nil {
			newRels = append(newRels, rel)
		}
	}
	sort.Strings(newRels)
	prints := fingerprintAll(root, newRels)
	// Files that are gone, by fingerprint: candidates for "it was moved".
	gone := map[string]string{} // fingerprint -> old rel
	for rel, k := range known {
		if _, ok := onDisk[rel]; !ok && k.fingerprint != "" {
			gone[k.fingerprint] = rel
		}
	}

	tx, err := d.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	t := &tree{tx: tx, lib: libraryID, shows: map[string]string{}, seasons: map[string]string{}, movies: map[string]string{}}
	if err := t.load(); err != nil {
		return res, err
	}

	for rel, f := range onDisk {
		if k := known[rel]; k != nil {
			if _, err := tx.Exec(`UPDATE files SET size=?, mtime=?, missing=0 WHERE id=?`, f.size, f.mtime, k.id); err != nil {
				return res, err
			}
		}
	}
	for _, rel := range newRels {
		f := onDisk[rel]
		fp := prints[rel]
		if old, ok := gone[fp]; ok && fp != "" {
			// Same size, same first and last 64 KB, and the old path is
			// gone: this is the same file under a new name.
			k := known[old]
			if _, err := tx.Exec(`UPDATE files SET rel_path=?, size=?, mtime=?, missing=0 WHERE id=?`, rel, f.size, f.mtime, k.id); err != nil {
				return res, err
			}
			if err := t.classify(k.itemID, rel); err != nil {
				return res, err
			}
			delete(gone, fp)
			delete(known, old)
			known[rel] = k
			res.Moved++
			continue
		}
		itemID, err := t.place("", rel)
		if err != nil {
			return res, err
		}
		var versionID string
		// A second copy of a film already in the library (say 1080p and
		// 4K) becomes another version of the same item.
		if err := tx.QueryRow(`SELECT v.id FROM versions v WHERE v.item_id=? AND NOT EXISTS (SELECT 1 FROM files f WHERE f.version_id=v.id)`, itemID).Scan(&versionID); err != nil {
			versionID = ids.New()
			if _, err := tx.Exec(`INSERT INTO versions(id,item_id,label) VALUES(?,?,?)`, versionID, itemID, ""); err != nil {
				return res, err
			}
		}
		fileID := ids.New()
		if _, err := tx.Exec(`INSERT INTO files(id,version_id,library_id,rel_path,size,mtime,fingerprint) VALUES(?,?,?,?,?,?,?)`,
			fileID, versionID, libraryID, rel, f.size, f.mtime, fp); err != nil {
			return res, err
		}
		known[rel] = &knownFile{id: fileID, itemID: itemID, fingerprint: fp, size: f.size}
		res.Added++
	}
	for rel, k := range known {
		if _, ok := onDisk[rel]; !ok {
			if !k.missing {
				res.Missing++
			}
			if _, err := tx.Exec(`UPDATE files SET missing=1 WHERE id=?`, k.id); err != nil {
				return res, err
			}
		}
	}
	// Items made by the first version, before names were recognised.
	legacy, err := tx.Query(`SELECT i.id, f.rel_path FROM items i JOIN versions v ON v.item_id=i.id JOIN files f ON f.version_id=v.id WHERE i.library_id=? AND i.kind='video'`, libraryID)
	if err != nil {
		return res, err
	}
	var todo [][2]string
	for legacy.Next() {
		var id, rel string
		legacy.Scan(&id, &rel)
		todo = append(todo, [2]string{id, rel})
	}
	legacy.Close()
	for _, it := range todo {
		if err := t.classify(it[0], it[1]); err != nil {
			return res, err
		}
	}
	if err := t.sidecars(onDisk, known, subs); err != nil {
		return res, err
	}
	if err := t.local(root, onDisk, known, aux); err != nil {
		return res, err
	}
	if err := t.prune(); err != nil {
		return res, err
	}
	res.Total = len(onDisk)
	if err := tx.Commit(); err != nil {
		return res, err
	}
	if res.Added+res.Moved > 0 {
		// Let SQLite re-measure the tables it now holds, so that queries
		// keep choosing their indexes as the library grows.
		d.Exec(`ANALYZE`)
	}
	return res, nil
}

// isExtra skips trailers, samples and bonus material, which are not items
// of their own.
func isExtra(rel string) bool {
	lower := strings.ToLower(rel)
	for _, dir := range []string{"/extras/", "/featurettes/", "/trailers/", "/behind the scenes/", "/deleted scenes/", "/interviews/", "/samples/", "/sample/", "/shorts/", "/other/"} {
		if strings.Contains("/"+lower, dir) {
			return true
		}
	}
	base := strings.TrimSuffix(path.Base(lower), path.Ext(lower))
	for _, suffix := range []string{"-trailer", ".trailer", "-sample", ".sample", "-featurette", "-behindthescenes", "-deleted", "-interview", "-scene", "-short", "-other"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return base == "sample" || base == "trailer"
}

// fingerprintAll computes fingerprints with a few readers at once.
func fingerprintAll(root string, rels []string) map[string]string {
	out := make(map[string]string, len(rels))
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan string)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range work {
				fp := Fingerprint(filepath.Join(root, filepath.FromSlash(rel)))
				mu.Lock()
				out[rel] = fp
				mu.Unlock()
			}
		}()
	}
	for _, rel := range rels {
		work <- rel
	}
	close(work)
	wg.Wait()
	return out
}

// Fingerprint identifies a file by its size and a hash of its first and
// last 64 KB. It reads at most 128 KB, so it is cheap even over a network.
// It returns "" if the file cannot be read.
func Fingerprint(p string) string {
	const chunk = 64 << 10
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	h := sha256.New()
	fmt.Fprintf(h, "%d\n", st.Size())
	if _, err := io.CopyN(h, f, chunk); err != nil && err != io.EOF {
		return ""
	}
	if st.Size() > 2*chunk {
		if _, err := f.Seek(-chunk, io.SeekEnd); err != nil {
			return ""
		}
		if _, err := io.CopyN(h, f, chunk); err != nil && err != io.EOF {
			return ""
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// tree keeps the films, shows and seasons of one library in memory while a
// scan runs, so each lookup is not a database query.
type tree struct {
	tx      *sql.Tx
	lib     string
	shows   map[string]string // folded title -> show item
	seasons map[string]string // show id + "/" + number -> season item
	movies  map[string]string // folded title + "/" + year -> film item
}

func (t *tree) load() error {
	rows, err := t.tx.Query(`SELECT id, kind, CASE WHEN match_key<>'' THEN match_key ELSE title END, year, COALESCE(parent_id,''), COALESCE(season_number,0) FROM items WHERE library_id=? AND kind IN ('show','season','movie')`, t.lib)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, title, parent string
		var year, season int
		rows.Scan(&id, &kind, &title, &year, &parent, &season)
		switch kind {
		case "show":
			t.shows[naming.Fold(title)] = id

		case "season":
			t.seasons[fmt.Sprintf("%s/%d", parent, season)] = id
		case "movie":
			// match_key already carries the year; an older row does not.
			if strings.Contains(title, "/") {
				t.movies[title] = id
			} else {
				t.movies[fmt.Sprintf("%s/%d", naming.Fold(title), year)] = id
			}
		}
	}
	return rows.Err()
}

func (t *tree) setFTS(itemID, title string) error {
	if _, err := t.tx.Exec(`DELETE FROM item_fts WHERE item_id=?`, itemID); err != nil {
		return err
	}
	_, err := t.tx.Exec(`INSERT INTO item_fts(item_id,title) VALUES(?,?)`, itemID, title)
	return err
}

func (t *tree) show(title string, year int) (string, error) {
	key := naming.Fold(title)
	if id, ok := t.shows[key]; ok {
		if year != 0 {
			t.tx.Exec(`UPDATE items SET year=? WHERE id=? AND year=0`, year, id)
		}
		return id, nil
	}
	id := ids.New()
	if _, err := t.tx.Exec(`INSERT INTO items(id,library_id,kind,title,sort_title,year,name_source,match_key,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, t.lib, "show", title, sortTitle(title), year, "file", key, time.Now().Unix()); err != nil {
		return "", err
	}
	t.shows[key] = id
	return id, t.setFTS(id, title)
}

func (t *tree) season(showID string, number int) (string, error) {
	key := fmt.Sprintf("%s/%d", showID, number)
	if id, ok := t.seasons[key]; ok {
		return id, nil
	}
	id := ids.New()
	title := fmt.Sprintf("Season %d", number)
	if number == 0 {
		title = "Specials"
	}
	if _, err := t.tx.Exec(`INSERT INTO items(id,library_id,parent_id,kind,title,sort_title,season_number,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		id, t.lib, showID, "season", title, fmt.Sprintf("%04d", number), number, time.Now().Unix()); err != nil {
		return "", err
	}
	t.seasons[key] = id
	return id, nil
}

// place returns the item a file at rel belongs to. With itemID == "" it
// creates one (or, for a second copy of a film, returns the existing one);
// otherwise it re-labels that item in place, keeping its watch progress.
func (t *tree) place(itemID, rel string) (string, error) {
	info := naming.Parse(rel)
	now := time.Now().Unix()
	if info.Kind == naming.Episode {
		showID, err := t.show(info.Title, info.Year)
		if err != nil {
			return "", err
		}
		seasonID, err := t.season(showID, info.Season)
		if err != nil {
			return "", err
		}
		title := fmt.Sprintf("Episode %d", info.Episode)
		if info.EpisodeEnd > 0 {
			title = fmt.Sprintf("Episodes %d-%d", info.Episode, info.EpisodeEnd)
		}
		sortKey := fmt.Sprintf("%04d", info.Episode)
		var end any
		if info.EpisodeEnd > 0 {
			end = info.EpisodeEnd
		}
		if itemID == "" {
			// The same episode twice (two qualities): one item.
			var existing string
			if err := t.tx.QueryRow(`SELECT id FROM items WHERE parent_id=? AND kind='episode' AND episode_number=?`, seasonID, info.Episode).Scan(&existing); err == nil {
				return existing, nil
			}
			itemID = ids.New()
			_, err = t.tx.Exec(`INSERT INTO items(id,library_id,parent_id,show_id,kind,title,sort_title,season_number,episode_number,episode_end,name_source,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				itemID, t.lib, seasonID, showID, "episode", title, sortKey, info.Season, info.Episode, end, "file", now)
			return itemID, err
		}
		_, err = t.tx.Exec(`UPDATE items SET parent_id=?, show_id=?, kind='episode', title=?, sort_title=?, year=0, season_number=?, episode_number=?, episode_end=?,
			name_source='file', match_key='', tmdb_id=0, lookup_state=0, overview='', poster='' WHERE id=?`,
			seasonID, showID, title, sortKey, info.Season, info.Episode, end, itemID)
		if err == nil {
			_, err = t.tx.Exec(`DELETE FROM item_fts WHERE item_id=?`, itemID)
		}
		return itemID, err
	}
	key := fmt.Sprintf("%s/%d", naming.Fold(info.Title), info.Year)
	if itemID == "" {
		if id, ok := t.movies[key]; ok && info.Year != 0 {
			return id, nil
		}
		itemID = ids.New()
		if _, err := t.tx.Exec(`INSERT INTO items(id,library_id,kind,title,sort_title,year,name_source,match_key,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			itemID, t.lib, "movie", info.Title, sortTitle(info.Title), info.Year, "file", key, now); err != nil {
			return "", err
		}
	} else if _, err := t.tx.Exec(`UPDATE items SET parent_id=NULL, show_id=NULL, kind='movie', title=?, sort_title=?, year=?, season_number=NULL, episode_number=NULL, episode_end=NULL,
		name_source='file', match_key=?, tmdb_id=0, lookup_state=0, overview='', poster='' WHERE id=? AND match_key<>?`,
		info.Title, sortTitle(info.Title), info.Year, key, itemID, key); err != nil {
		return "", err
	}
	t.movies[key] = itemID
	return itemID, t.setFTS(itemID, info.Title)
}

func (t *tree) classify(itemID, rel string) error {
	_, err := t.place(itemID, rel)
	return err
}

// prune removes seasons and shows that no longer hold anything.
func (t *tree) prune() error {
	if _, err := t.tx.Exec(`DELETE FROM items WHERE library_id=? AND kind='season' AND NOT EXISTS (SELECT 1 FROM items c WHERE c.parent_id=items.id)`, t.lib); err != nil {
		return err
	}
	if _, err := t.tx.Exec(`DELETE FROM item_fts WHERE item_id IN (SELECT id FROM items WHERE library_id=? AND kind='show' AND NOT EXISTS (SELECT 1 FROM items c WHERE c.parent_id=items.id))`, t.lib); err != nil {
		return err
	}
	_, err := t.tx.Exec(`DELETE FROM items WHERE library_id=? AND kind='show' AND NOT EXISTS (SELECT 1 FROM items c WHERE c.parent_id=items.id)`, t.lib)
	return err
}

// sidecars records the subtitle files that sit next to each video:
// "Film.srt", "Film.en.srt", "Film.en.forced.srt".
func (t *tree) sidecars(onDisk map[string]diskFile, known map[string]*knownFile, subs map[string][]string) error {
	if _, err := t.tx.Exec(`DELETE FROM sidecars WHERE file_id IN (SELECT id FROM files WHERE library_id=?)`, t.lib); err != nil {
		return err
	}
	for rel := range onDisk {
		k := known[rel]
		dir := path.Dir(rel)
		if k == nil || len(subs[dir]) == 0 {
			continue
		}
		base := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
		for _, name := range subs[dir] {
			if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)+".") {
				continue
			}
			ext := strings.ToLower(path.Ext(name))
			middle := strings.ToLower(strings.Trim(name[len(base):len(name)-len(ext)], "."))
			lang, forced := "", 0
			for _, part := range strings.Split(middle, ".") {
				switch {
				case part == "forced":
					forced = 1
				case part == "sdh" || part == "cc" || part == "hi" || part == "default" || part == "":
				case lang == "" && len(part) <= 8:
					lang = part
				}
			}
			subRel := name
			if dir != "." {
				subRel = dir + "/" + name
			}
			if _, err := t.tx.Exec(`INSERT OR IGNORE INTO sidecars(id,file_id,rel_path,format,language,is_forced) VALUES(?,?,?,?,?,?)`,
				ids.New(), k.id, subRel, subtitleExt[ext], lang, forced); err != nil {
				return err
			}
		}
	}
	return nil
}

// sortTitle files "The Matrix" under M.
func sortTitle(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, article) && len(s) > len(article) {
			return s[len(article):]
		}
	}
	return s
}

// ProbeResult summarises one round of inspecting files.
type ProbeResult struct {
	Inspected int
	Failed    int
}

// ProbePending runs ffprobe on every file that has not been inspected, or
// has changed since it was. Results are stored, so each file is read once.
func ProbePending(ctx context.Context, d *sql.DB, ffprobe string) (ProbeResult, error) {
	var res ProbeResult
	if ffprobe == "" {
		return res, nil
	}
	rows, err := d.Query(`SELECT f.id, l.path, f.rel_path, f.size, f.mtime FROM files f JOIN libraries l ON l.id=f.library_id
		WHERE f.missing=0 AND (f.probed=0 OR f.probed_size<>f.size OR f.probed_mtime<>f.mtime)`)
	if err != nil {
		return res, err
	}
	type job struct {
		id, path    string
		size, mtime int64
	}
	var jobs []job
	for rows.Next() {
		var j job
		var root, rel string
		rows.Scan(&j.id, &root, &rel, &j.size, &j.mtime)
		j.path = filepath.Join(root, filepath.FromSlash(rel))
		jobs = append(jobs, j)
	}
	rows.Close()

	type done struct {
		job
		r   probe.Result
		err error
	}
	work := make(chan job)
	results := make(chan done)
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range work {
				r, err := probe.Run(ctx, ffprobe, j.path)
				results <- done{j, r, err}
			}
		}()
	}
	go func() {
		for _, j := range jobs {
			if ctx.Err() != nil {
				break
			}
			work <- j
		}
		close(work)
		wg.Wait()
		close(results)
	}()
	for r := range results {
		if ctx.Err() != nil {
			continue
		}
		if err := storeProbe(d, r.id, r.size, r.mtime, r.r, r.err); err != nil {
			return res, err
		}
		if r.err != nil {
			res.Failed++
		} else {
			res.Inspected++
		}
	}
	return res, ctx.Err()
}

func storeProbe(d *sql.DB, fileID string, size, mtime int64, r probe.Result, probeErr error) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM streams WHERE file_id=?`, fileID); err != nil {
		return err
	}
	if probeErr != nil {
		if _, err := tx.Exec(`UPDATE files SET probed=2, probed_size=?, probed_mtime=? WHERE id=?`, size, mtime, fileID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE files SET probed=1, probed_size=?, probed_mtime=?, duration_ms=?, container=?, bit_rate=? WHERE id=?`,
		size, mtime, r.DurationMs, r.Container, r.BitRate, fileID); err != nil {
		return err
	}
	for _, s := range r.Streams {
		if _, err := tx.Exec(`INSERT INTO streams(file_id,idx,kind,codec,profile,level,width,height,pix_fmt,bit_depth,color_transfer,color_primaries,frame_rate,channels,channel_layout,sample_rate,bit_rate,language,title,is_default,is_forced,is_commentary,is_hearing_impaired,side_data)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fileID, s.Index, s.Kind, s.Codec, s.Profile, s.Level, s.Width, s.Height, s.PixFmt, s.BitDepth, s.ColorTransfer, s.ColorPrimaries,
			s.FrameRate, s.Channels, s.ChannelLayout, s.SampleRate, s.BitRate, s.Language, s.Title, b(s.Default), b(s.Forced), b(s.Commentary), b(s.HearingImpaired), s.SideData); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func b(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Dir is one entry in the folder picker.
type Dir struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Listing is what the folder picker shows for one location.
type Listing struct {
	Path   string `json:"path"`
	Parent string `json:"parent"`
	Dirs   []Dir  `json:"dirs"`
}

// ListDirs lists the sub-folders of dir for the folder picker. An empty
// path lists the starting points: drives on Windows, home and / elsewhere.
func ListDirs(dir string) (Listing, error) {
	if dir == "" {
		return Listing{Dirs: roots()}, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Listing{}, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return Listing{}, errors.New("that folder cannot be opened")
	}
	l := Listing{Path: abs, Dirs: []Dir{}}
	if parent := filepath.Dir(abs); parent != abs {
		l.Parent = parent
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") {
			continue
		}
		isDir := e.IsDir()
		if !isDir && e.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(abs, name)); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			l.Dirs = append(l.Dirs, Dir{Name: name, Path: filepath.Join(abs, name)})
		}
	}
	sort.Slice(l.Dirs, func(i, j int) bool { return strings.ToLower(l.Dirs[i].Name) < strings.ToLower(l.Dirs[j].Name) })
	return l, nil
}

func roots() []Dir {
	var out []Dir
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, Dir{Name: "Home (" + filepath.Base(home) + ")", Path: home})
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			p := string(c) + `:\`
			if _, err := os.Stat(p); err == nil {
				out = append(out, Dir{Name: "Drive " + string(c) + ":", Path: p})
			}
		}
		return out
	}
	out = append(out, Dir{Name: "Computer (/)", Path: "/"})
	for _, p := range []string{"/Volumes", "/media", "/mnt"} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, Dir{Name: p, Path: p})
		}
	}
	return out
}
