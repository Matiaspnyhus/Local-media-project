package metadata

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Summary says what one round of lookups did.
type Summary struct {
	Found    int
	NotFound int
}

// Enrich looks up every film and show that has not been looked up yet and
// stores what TMDB says: the proper title, year, description and poster,
// and for shows the names of the episodes that are in the library.
//
// It stops at the first sign that the key is bad or TMDB is unreachable,
// leaving the rest for next time; a film that is simply not found is
// remembered as such and not asked about again.
func Enrich(ctx context.Context, d *sql.DB, c *Client, artworkDir string) (Summary, error) {
	var sum Summary
	if err := os.MkdirAll(artworkDir, 0o700); err != nil {
		return sum, err
	}
	type todo struct {
		id, kind, title, rel, source, poster string
		year, tmdb                           int
	}
	rows, err := d.Query(`SELECT i.id, i.kind, i.title, i.year, i.name_source, i.poster, i.tmdb_id, COALESCE((
			SELECT f.rel_path FROM files f JOIN versions v ON v.id = f.version_id
			WHERE v.item_id = i.id OR v.item_id IN (SELECT e.id FROM items e JOIN items se ON se.id = e.parent_id WHERE se.parent_id = i.id) LIMIT 1), '')
		FROM items i WHERE i.kind IN ('movie','show') AND i.lookup_state = 0 ORDER BY i.created_at`)
	if err != nil {
		return sum, err
	}
	var list []todo
	for rows.Next() {
		var t todo
		rows.Scan(&t.id, &t.kind, &t.title, &t.year, &t.source, &t.poster, &t.tmdb, &t.rel)
		list = append(list, t)
	}
	rows.Close()

	for _, t := range list {
		if ctx.Err() != nil {
			return sum, ctx.Err()
		}
		kind := "movie"
		if t.kind == "show" {
			kind = "tv"
		}
		var hit Result
		found := false
		hinted := HintID(t.rel)
		if hinted == 0 {
			hinted = t.tmdb // from a local info file
		}
		if id := hinted; id > 0 {
			hit, err = c.ByID(ctx, kind, id)
			if err == nil {
				found = true
			} else if !errors.Is(err, errNotFound) {
				return sum, err
			}
		}
		if !found {
			hits, err := c.search(ctx, kind, t.title, t.year)
			if err != nil {
				return sum, err
			}
			// A year in a file name is sometimes off by one, and TMDB's
			// year filter is exact: ask again without it.
			if hit, found = Pick(t.title, t.year, hits); !found && t.year > 0 {
				if hits, err = c.search(ctx, kind, t.title, 0); err != nil {
					return sum, err
				}
				hit, found = Pick(t.title, t.year, hits)
			}
		}
		now := time.Now().Unix()
		if !found {
			if _, err := d.Exec(`UPDATE items SET lookup_state = 2, lookup_at = ? WHERE id = ?`, now, t.id); err != nil {
				return sum, err
			}
			sum.NotFound++
			continue
		}
		poster := t.poster
		if hit.PosterPath != "" && !strings.HasPrefix(t.poster, "local:") {
			if img, err := c.Poster(ctx, hit.PosterPath); err == nil {
				name := t.id + strings.ToLower(filepath.Ext(hit.PosterPath))
				if os.WriteFile(filepath.Join(artworkDir, name), img, 0o600) == nil {
					poster = name
				}
			} else if ctx.Err() != nil {
				return sum, ctx.Err()
			}
		}
		year := hit.Year
		if year == 0 {
			year = t.year
		}
		tx, err := d.Begin()
		if err != nil {
			return sum, err
		}
		if t.source == "nfo" {
			// The owner's own info file wins: keep its title, year and
			// description, and take only what it lacks.
			_, err = tx.Exec(`UPDATE items SET overview = CASE WHEN overview = '' THEN ? ELSE overview END, year = CASE WHEN year = 0 THEN ? ELSE year END,
				tmdb_id = ?, poster = ?, lookup_state = 1, lookup_at = ? WHERE id = ?`, hit.Overview, year, hit.ID, poster, now, t.id)
			hit.Title = t.title
		} else {
			_, err = tx.Exec(`UPDATE items SET title = ?, sort_title = ?, year = ?, overview = ?, tmdb_id = ?, poster = ?, name_source = 'tmdb', lookup_state = 1, lookup_at = ? WHERE id = ?`,
				hit.Title, sortTitle(hit.Title), year, hit.Overview, hit.ID, poster, now, t.id)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM item_fts WHERE item_id = ?`, t.id)
		}
		if err == nil {
			// Search finds it under both the proper title and the file's.
			_, err = tx.Exec(`INSERT INTO item_fts(item_id, title) VALUES(?, ?)`, t.id, hit.Title+" "+t.title)
		}
		if err != nil {
			tx.Rollback()
			return sum, err
		}
		if err := tx.Commit(); err != nil {
			return sum, err
		}
		sum.Found++
		if t.kind == "show" {
			if err := episodes(ctx, d, c, t.id, hit.ID); err != nil {
				return sum, err
			}
		}
	}
	// Episodes added later to a show that is already matched.
	late, err := d.Query(`SELECT DISTINCT sh.id, sh.tmdb_id FROM items sh JOIN items se ON se.parent_id = sh.id JOIN items e ON e.parent_id = se.id
		WHERE sh.kind = 'show' AND sh.lookup_state = 1 AND sh.tmdb_id > 0 AND e.kind = 'episode' AND e.lookup_state = 0`)
	if err != nil {
		return sum, err
	}
	type show struct {
		id   string
		tmdb int
	}
	var shows []show
	for late.Next() {
		var s show
		late.Scan(&s.id, &s.tmdb)
		shows = append(shows, s)
	}
	late.Close()
	for _, s := range shows {
		if err := episodes(ctx, d, c, s.id, s.tmdb); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// episodes names the episodes of one show, one request per season.
func episodes(ctx context.Context, d *sql.DB, c *Client, showID string, tmdbID int) error {
	rows, err := d.Query(`SELECT DISTINCT se.season_number FROM items se JOIN items e ON e.parent_id = se.id
		WHERE se.parent_id = ? AND e.kind = 'episode' AND e.lookup_state = 0`, showID)
	if err != nil {
		return err
	}
	var seasons []int
	for rows.Next() {
		var n int
		rows.Scan(&n)
		seasons = append(seasons, n)
	}
	rows.Close()
	now := time.Now().Unix()
	for _, n := range seasons {
		eps, err := c.Season(ctx, tmdbID, n)
		if err != nil {
			return err
		}
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		for _, e := range eps {
			if strings.TrimSpace(e.Name) == "" {
				continue
			}
			if _, err := tx.Exec(`UPDATE items SET title = ?, overview = ?, name_source = 'tmdb', lookup_state = 1, lookup_at = ?
				WHERE kind = 'episode' AND name_source <> 'nfo' AND episode_number = ? AND episode_end IS NULL AND parent_id IN (SELECT id FROM items WHERE parent_id = ? AND season_number = ?)`,
				e.Name, e.Overview, now, e.Number, showID, n); err != nil {
				tx.Rollback()
				return err
			}
		}
		// Whatever TMDB did not list is settled too, and not asked again.
		if _, err := tx.Exec(`UPDATE items SET lookup_state = 2, lookup_at = ? WHERE kind = 'episode' AND lookup_state = 0
			AND parent_id IN (SELECT id FROM items WHERE parent_id = ? AND season_number = ?)`, now, showID, n); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func sortTitle(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, article) && len(s) > len(article) {
			return s[len(article):]
		}
	}
	return s
}
