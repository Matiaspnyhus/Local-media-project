// Package library finds video files in the owner's folders and records them.
// It only ever reads media folders; nothing in them is created, renamed or
// deleted.
package library

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
)

// videoExt lists the extensions treated as video in M0.
var videoExt = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true,
	".avi": true, ".ts": true, ".m2ts": true, ".wmv": true, ".mpg": true, ".mpeg": true,
}

// IsVideo reports whether name looks like a video file.
func IsVideo(name string) bool { return videoExt[strings.ToLower(filepath.Ext(name))] }

// ScanResult summarises one scan.
type ScanResult struct {
	Added   int `json:"added"`
	Missing int `json:"missing"`
	Total   int `json:"total"`
}

// Add registers a folder as a library and returns its ID.
func Add(d *sql.DB, path string) (string, error) {
	abs, err := filepath.Abs(path)
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
	name := filepath.Base(abs)
	_, err = d.Exec(`INSERT INTO libraries(id,name,path,created_at) VALUES(?,?,?,?)`, id, name, abs, time.Now().Unix())
	if err != nil {
		return "", errors.New("that folder is already in the library")
	}
	return id, nil
}

// Scan walks one library folder and brings the database in line with it.
// Files that have disappeared are marked missing, not forgotten, so watch
// progress survives a drive that is unplugged for a while.
func Scan(d *sql.DB, libraryID string) (ScanResult, error) {
	var res ScanResult
	var root string
	if err := d.QueryRow(`SELECT path FROM libraries WHERE id=?`, libraryID).Scan(&root); err != nil {
		return res, err
	}
	type found struct {
		size, mtime int64
	}
	onDisk := map[string]found{}
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if e != nil && e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := e.Name()
		if e.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") || name == "System Volume Information") {
				return filepath.SkipDir
			}
			return nil
		}
		// Regular files only: links are not followed, so a link cannot
		// lead the server outside the folder the owner chose.
		if !e.Type().IsRegular() || strings.HasPrefix(name, ".") || !IsVideo(name) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		onDisk[filepath.ToSlash(rel)] = found{info.Size(), info.ModTime().Unix()}
		return nil
	})
	if err != nil {
		return res, err
	}

	tx, err := d.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	known := map[string]bool{}
	rows, err := tx.Query(`SELECT rel_path FROM files WHERE library_id=?`, libraryID)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var rel string
		rows.Scan(&rel)
		known[rel] = true
	}
	rows.Close()

	rels := make([]string, 0, len(onDisk))
	for rel := range onDisk {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	now := time.Now().Unix()
	for _, rel := range rels {
		f := onDisk[rel]
		if known[rel] {
			if _, err := tx.Exec(`UPDATE files SET size=?, mtime=?, missing=0 WHERE library_id=? AND rel_path=?`, f.size, f.mtime, libraryID, rel); err != nil {
				return res, err
			}
			continue
		}
		itemID, versionID, fileID := ids.New(), ids.New(), ids.New()
		title := TitleFromPath(rel)
		if _, err := tx.Exec(`INSERT INTO items(id,library_id,kind,title,sort_title,created_at) VALUES(?,?,?,?,?,?)`,
			itemID, libraryID, "video", title, strings.ToLower(title), now); err != nil {
			return res, err
		}
		if _, err := tx.Exec(`INSERT INTO versions(id,item_id) VALUES(?,?)`, versionID, itemID); err != nil {
			return res, err
		}
		if _, err := tx.Exec(`INSERT INTO files(id,version_id,library_id,rel_path,size,mtime) VALUES(?,?,?,?,?,?)`,
			fileID, versionID, libraryID, rel, f.size, f.mtime); err != nil {
			return res, err
		}
		if _, err := tx.Exec(`INSERT INTO item_fts(item_id,title) VALUES(?,?)`, itemID, title); err != nil {
			return res, err
		}
		res.Added++
	}
	for rel := range known {
		if _, ok := onDisk[rel]; !ok {
			if _, err := tx.Exec(`UPDATE files SET missing=1 WHERE library_id=? AND rel_path=?`, libraryID, rel); err != nil {
				return res, err
			}
			res.Missing++
		}
	}
	res.Total = len(onDisk)
	return res, tx.Commit()
}

// TitleFromPath turns "Films/Some.Movie.2019.mkv" into "Some Movie 2019".
// Proper name recognition arrives in M1; this only makes names readable.
func TitleFromPath(rel string) string {
	base := filepath.Base(filepath.FromSlash(rel))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if !strings.Contains(base, " ") {
		base = strings.NewReplacer(".", " ", "_", " ").Replace(base)
	}
	base = strings.Join(strings.Fields(base), " ")
	if base == "" {
		return rel
	}
	return base
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

// ListDirs lists the sub-folders of path for the folder picker. An empty
// path lists the starting points: drives on Windows, home and / elsewhere.
func ListDirs(path string) (Listing, error) {
	if path == "" {
		return Listing{Dirs: roots()}, nil
	}
	abs, err := filepath.Abs(path)
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
