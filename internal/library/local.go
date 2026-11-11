package library

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Local information: what the owner (or a tool such as Kodi, Radarr or
// Sonarr) has put beside the files. It always wins over online lookups.
//
//	Film:    "Film.nfo" or "movie.nfo" beside the video;
//	         "poster.jpg", "folder.jpg" or "Film-poster.jpg" for the picture.
//	Show:    "tvshow.nfo" and "poster.jpg" in the show's folder.
//	Episode: "Episode file name.nfo" beside the video.

// nfo is the part of a Kodi-style info file that is used.
type nfo struct {
	Title     string `xml:"title"`
	Year      string `xml:"year"`
	Premiered string `xml:"premiered"`
	Plot      string `xml:"plot"`
	TMDBID    string `xml:"tmdbid"`
	UniqueIDs []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
}

type localInfo struct {
	title, overview string
	year, tmdb      int
}

// readNFO reads an info file through the library's root, so a link cannot
// lead it elsewhere. Files that are not XML (some hold only a web address)
// are ignored.
func readNFO(root *os.Root, rel string) (localInfo, bool) {
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return localInfo{}, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return localInfo{}, false
	}
	var n nfo
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	if err := dec.Decode(&n); err != nil {
		return localInfo{}, false
	}
	info := localInfo{title: strings.TrimSpace(n.Title), overview: strings.TrimSpace(n.Plot)}
	if info.title == "" {
		return localInfo{}, false
	}
	year := strings.TrimSpace(n.Year)
	if year == "" && len(n.Premiered) >= 4 {
		year = n.Premiered[:4]
	}
	if y, err := strconv.Atoi(year); err == nil && y > 1870 && y < 2200 {
		info.year = y
	}
	id := strings.TrimSpace(n.TMDBID)
	for _, u := range n.UniqueIDs {
		if strings.EqualFold(u.Type, "tmdb") {
			id = strings.TrimSpace(u.Value)
		}
	}
	info.tmdb, _ = strconv.Atoi(id)
	return info, true
}

func hasName(names []string, want string) (string, bool) {
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return n, true
		}
	}
	return "", false
}

func join(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}

// findPoster returns the picture file for a folder, if there is one.
func findPoster(names []string, base string) (string, bool) {
	var candidates []string
	if base != "" {
		for _, ext := range []string{".jpg", ".jpeg", ".png"} {
			candidates = append(candidates, base+"-poster"+ext)
		}
	}
	for _, stem := range []string{"poster", "folder", "cover"} {
		for _, ext := range []string{".jpg", ".jpeg", ".png"} {
			candidates = append(candidates, stem+ext)
		}
	}
	for _, c := range candidates {
		if n, ok := hasName(names, c); ok {
			return n, true
		}
	}
	return "", false
}

// local applies info files and local pictures to the items of this scan.
// aux lists, per folder, the .nfo and picture files found in it.
func (t *tree) local(rootPath string, onDisk map[string]diskFile, known map[string]*knownFile, aux map[string][]string) error {
	if len(aux) == 0 {
		return nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil
	}
	defer root.Close()

	type item struct {
		kind, source, poster, showID, showSource, showPoster string
	}
	items := map[string]item{}
	rows, err := t.tx.Query(`SELECT i.id, i.kind, i.name_source, i.poster, COALESCE(sh.id,''), COALESCE(sh.name_source,''), COALESCE(sh.poster,'')
		FROM items i LEFT JOIN items se ON se.id=i.parent_id AND i.kind='episode' LEFT JOIN items sh ON sh.id=se.parent_id
		WHERE i.library_id=? AND i.kind IN ('movie','episode')`, t.lib)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var it item
		rows.Scan(&id, &it.kind, &it.source, &it.poster, &it.showID, &it.showSource, &it.showPoster)
		items[id] = it
	}
	rows.Close()

	apply := func(id string, info localInfo, keepNumbering bool) error {
		year := any(info.year)
		if keepNumbering {
			year = 0
		}
		if _, err := t.tx.Exec(`UPDATE items SET title=?, sort_title=CASE WHEN ? THEN sort_title ELSE ? END, year=CASE WHEN ?>0 THEN ? ELSE year END,
			overview=?, tmdb_id=CASE WHEN ?>0 THEN ? ELSE tmdb_id END, name_source='nfo' WHERE id=?`,
			info.title, keepNumbering, sortTitle(info.title), year, year, info.overview, info.tmdb, info.tmdb, id); err != nil {
			return err
		}
		if keepNumbering {
			return nil
		}
		return t.setFTS(id, info.title)
	}
	setPoster := func(id, rel string) error {
		_, err := t.tx.Exec(`UPDATE items SET poster=? WHERE id=?`, "local:"+rel, id)
		return err
	}
	doneShows := map[string]bool{}

	for rel := range onDisk {
		k := known[rel]
		if k == nil {
			continue
		}
		it, ok := items[k.itemID]
		if !ok {
			continue
		}
		dir := path.Dir(rel)
		names := aux[dir]
		base := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
		if it.source != "nfo" {
			name, found := hasName(names, base+".nfo")
			if !found && it.kind == "movie" {
				name, found = hasName(names, "movie.nfo")
			}
			if found {
				if info, ok := readNFO(root, join(dir, name)); ok {
					if err := apply(k.itemID, info, it.kind == "episode"); err != nil {
						return err
					}
				}
			}
		}
		if it.kind == "movie" && !strings.HasPrefix(it.poster, "local:") && dir != "." {
			if name, ok := findPoster(names, base); ok {
				if err := setPoster(k.itemID, join(dir, name)); err != nil {
					return err
				}
			}
		}
		if it.kind != "episode" || it.showID == "" || doneShows[it.showID] {
			continue
		}
		// The show's folder is the episode's folder or one of the two above
		// it ("Show/Season 1/episode.mkv").
		doneShows[it.showID] = true
		for d, up := dir, 0; up < 3 && d != "." && d != "/"; d, up = path.Dir(d), up+1 {
			showNames := aux[d]
			if it.showSource != "nfo" {
				if name, ok := hasName(showNames, "tvshow.nfo"); ok {
					if info, ok := readNFO(root, join(d, name)); ok {
						if err := apply(it.showID, info, false); err != nil {
							return err
						}
						it.showSource = "nfo"
					}
				}
			}
			_, isShowFolder := hasName(showNames, "tvshow.nfo")
			if !strings.HasPrefix(it.showPoster, "local:") && (isShowFolder || up == 1) {
				if name, ok := findPoster(showNames, ""); ok {
					if err := setPoster(it.showID, join(d, name)); err != nil {
						return err
					}
					it.showPoster = "local:"
				}
			}
			if isShowFolder {
				break
			}
		}
	}
	return nil
}
