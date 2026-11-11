package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// itemView is one playable thing, a film or an episode, as the page sees it.
type itemView struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Year       int      `json:"year,omitempty"`
	ShowID     string   `json:"showId,omitempty"`
	ShowTitle  string   `json:"showTitle,omitempty"`
	Season     int      `json:"season"`
	Episode    int      `json:"episode"`
	EpisodeEnd int      `json:"episodeEnd,omitempty"`
	Container  string   `json:"container"`
	DurationMs int64    `json:"durationMs"`
	PositionMs int64    `json:"positionMs"`
	Played     bool     `json:"played"`
	Playable   string   `json:"playable"` // yes, no, unknown
	Reasons    []string `json:"reasons"`

	fileID    string
	probed    bool
	updatedAt int64
	hasAudio  any
}

// Display is the name shown above the player.
func (v itemView) Display() string {
	if v.Kind == "episode" {
		return fmt.Sprintf("%s · S%d E%d", v.ShowTitle, v.Season, v.Episode)
	}
	if v.Year > 0 {
		return fmt.Sprintf("%s (%d)", v.Title, v.Year)
	}
	return v.Title
}

func (s *Server) capsFor(u *User) *playback.Caps {
	s.capsMu.Lock()
	defer s.capsMu.Unlock()
	return s.caps[u.SessionID]
}

// handleCapabilities stores what this device says it can play. The page
// measures it once per visit; nothing is guessed from the browser's name
// except the corrections below.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	var c playback.Caps
	if !readJSON(w, r, &c) {
		return
	}
	if len(c.Containers) > 40 || len(c.Video) > 40 || len(c.Audio) > 40 {
		writeErr(w, http.StatusBadRequest, "That report is too large.")
		return
	}
	applyOverrides(&c, r.UserAgent())
	u := currentUser(r)
	s.capsMu.Lock()
	if len(s.caps) > 5000 {
		s.caps = map[string]*playback.Caps{}
	}
	s.caps[u.SessionID] = &c
	s.capsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// applyOverrides corrects the cases where a browser is known to claim more
// than it delivers.
func applyOverrides(c *playback.Caps, userAgent string) {
	if c.Containers == nil {
		return
	}
	// Firefox says it opens MKV but reads the whole file before playing.
	if strings.Contains(userAgent, "Firefox/") {
		c.Containers["matroska"] = false
	}
}

// loadItems reads films and episodes matching where, with this user's
// progress and whether this device can play each one.
func (s *Server) loadItems(u *User, where, order string, args ...any) ([]itemView, error) {
	if order == "" {
		order = "i.sort_title"
	}
	q := `SELECT i.id, i.kind, i.title, i.year, COALESCE(i.season_number,0), COALESCE(i.episode_number,0), COALESCE(i.episode_end,0),
		COALESCE(sh.id,''), COALESCE(sh.title,''), f.id, f.rel_path, f.probed, f.container, f.duration_ms,
		COALESCE(p.position_ms,0), COALESCE(p.duration_ms,0), COALESCE(p.played,0), COALESCE(p.updated_at,0)
		FROM items i
		LEFT JOIN items se ON se.id = i.parent_id AND i.kind = 'episode'
		LEFT JOIN items sh ON sh.id = se.parent_id
		JOIN versions v ON v.item_id = i.id
		JOIN files f ON f.version_id = v.id AND f.missing = 0 AND f.part_index = 0
		LEFT JOIN progress p ON p.item_id = i.id AND p.user_id = ?
		WHERE i.kind IN ('movie','episode','video') AND (` + where + `)
		ORDER BY ` + order + `, f.size DESC`
	rows, err := s.db.Query(q, append([]any{u.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	var out []itemView
	seen := map[string]bool{}
	for rows.Next() {
		var v itemView
		var rel string
		var probed, played int
		var fileDur, progDur int64
		if err := rows.Scan(&v.ID, &v.Kind, &v.Title, &v.Year, &v.Season, &v.Episode, &v.EpisodeEnd, &v.ShowID, &v.ShowTitle,
			&v.fileID, &rel, &probed, &v.Container, &fileDur, &v.PositionMs, &progDur, &played, &v.updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if seen[v.ID] {
			continue // a second version of the same item
		}
		seen[v.ID] = true
		v.Played, v.probed = played != 0, probed == 1
		v.DurationMs = fileDur
		if v.DurationMs == 0 {
			v.DurationMs = progDur
		}
		if v.Container == "" {
			v.Container = strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), ".")
		}
		v.Reasons = []string{}
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.decide(u, out)
}

// decide fills in, for each item, whether this device can play it.
func (s *Server) decide(u *User, items []itemView) error {
	tracks := map[string]*playback.Media{}
	for i := range items {
		tracks[items[i].fileID] = &playback.Media{Probed: items[i].probed}
	}
	fileIDs := make([]any, 0, len(items))
	for id := range tracks {
		fileIDs = append(fileIDs, id)
	}
	for start := 0; start < len(fileIDs); start += 400 {
		end := min(start+400, len(fileIDs))
		q := `SELECT s.file_id, s.kind, s.codec, s.bit_depth, s.is_default, s.is_commentary, s.language, f.container FROM streams s
			JOIN files f ON f.id = s.file_id WHERE s.kind IN ('video','audio') AND s.file_id IN (?` + strings.Repeat(",?", end-start-1) + `) ORDER BY s.file_id, s.idx`
		rows, err := s.db.Query(q, fileIDs[start:end]...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var fileID, kind, container string
			var t playback.Track
			var def, com int
			rows.Scan(&fileID, &kind, &t.Codec, &t.BitDepth, &def, &com, &t.Language, &container)
			t.Default, t.Commentary = def != 0, com != 0
			m := tracks[fileID]
			m.Container = container
			if kind == "video" {
				m.Video = append(m.Video, t)
			} else {
				m.Audio = append(m.Audio, t)
			}
		}
		rows.Close()
	}
	caps := s.capsFor(u)
	for i := range items {
		m := tracks[items[i].fileID]
		plan := playback.Decide(*m, caps)
		items[i].Playable = string(plan.Direct)
		if plan.Reasons != nil {
			items[i].Reasons = plan.Reasons
		}
		if m.Probed {
			items[i].hasAudio = plan.HasAudio
		}
	}
	return nil
}

type showView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	Episodes int    `json:"episodes"`
	Watched  int    `json:"watched"`
}

const showQuery = `SELECT sh.id, sh.title, sh.year, count(DISTINCT e.id), count(DISTINCT CASE WHEN p.played = 1 THEN e.id END)
	FROM items sh
	JOIN items se ON se.parent_id = sh.id
	JOIN items e ON e.parent_id = se.id AND e.kind = 'episode'
	JOIN versions v ON v.item_id = e.id
	JOIN files f ON f.version_id = v.id AND f.missing = 0
	LEFT JOIN progress p ON p.item_id = e.id AND p.user_id = ?
	WHERE sh.kind = 'show' AND (%s)
	GROUP BY sh.id ORDER BY sh.sort_title`

func (s *Server) loadShows(u *User, where string, args ...any) ([]showView, error) {
	rows, err := s.db.Query(fmt.Sprintf(showQuery, where), append([]any{u.ID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []showView{}
	for rows.Next() {
		var v showView
		if err := rows.Scan(&v.ID, &v.Title, &v.Year, &v.Episodes, &v.Watched); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func orEmpty(v []itemView) []itemView {
	if v == nil {
		return []itemView{}
	}
	return v
}

// handleHome is the first screen: what to carry on with, what comes next,
// and how big the library is.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	cont, err := s.loadItems(u, `p.position_ms > 0 AND p.played = 0`, `p.updated_at DESC`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the library.")
		return
	}
	if len(cont) > 24 {
		cont = cont[:24]
	}
	next, err := s.nextUp(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the library.")
		return
	}
	var movies, shows, pending, unreadable int
	s.db.QueryRow(`SELECT count(*) FROM items i WHERE i.kind IN ('movie','video') AND EXISTS (SELECT 1 FROM versions v JOIN files f ON f.version_id=v.id WHERE v.item_id=i.id AND f.missing=0)`).Scan(&movies)
	s.db.QueryRow(`SELECT count(*) FROM items WHERE kind='show'`).Scan(&shows)
	s.db.QueryRow(`SELECT count(*) FROM files WHERE missing=0 AND probed=0`).Scan(&pending)
	s.db.QueryRow(`SELECT count(*) FROM files WHERE missing=0 AND probed=2`).Scan(&unreadable)
	writeJSON(w, http.StatusOK, map[string]any{
		"continue": orEmpty(cont), "nextUp": orEmpty(next),
		"counts":     map[string]int{"movies": movies, "shows": shows},
		"inspection": map[string]any{"available": s.ffprobe != "", "pending": pending, "unreadable": unreadable, "running": s.inspecting.Load()},
	})
}

// nextUp finds, for every show the user is part-way through, the episode
// that follows the one they watched most recently.
func (s *Server) nextUp(u *User) ([]itemView, error) {
	rows, err := s.db.Query(`SELECT sh.id, e.id, e.season_number, e.episode_number, COALESCE(p.played,0), COALESCE(p.position_ms,0), COALESCE(p.updated_at,0)
		FROM items sh
		JOIN items se ON se.parent_id = sh.id
		JOIN items e ON e.parent_id = se.id AND e.kind = 'episode'
		LEFT JOIN progress p ON p.item_id = e.id AND p.user_id = ?
		WHERE sh.kind = 'show' AND sh.id IN (
			SELECT sh2.id FROM progress p2 JOIN items e2 ON e2.id = p2.item_id JOIN items se2 ON se2.id = e2.parent_id JOIN items sh2 ON sh2.id = se2.parent_id
			WHERE p2.user_id = ? AND e2.kind = 'episode')
		AND EXISTS (SELECT 1 FROM versions v JOIN files f ON f.version_id = v.id WHERE v.item_id = e.id AND f.missing = 0)
		ORDER BY sh.id, e.season_number, e.episode_number`, u.ID, u.ID)
	if err != nil {
		return nil, err
	}
	type ep struct {
		id              string
		season, episode int
		played          bool
		position, when  int64
	}
	byShow := map[string][]ep{}
	var order []string
	for rows.Next() {
		var show string
		var e ep
		var played int
		rows.Scan(&show, &e.id, &e.season, &e.episode, &played, &e.position, &e.when)
		e.played = played != 0
		if _, ok := byShow[show]; !ok {
			order = append(order, show)
		}
		byShow[show] = append(byShow[show], e)
	}
	rows.Close()
	type pick struct {
		id   string
		when int64
	}
	var picks []pick
	for _, show := range order {
		eps := byShow[show]
		last := -1
		for i, e := range eps {
			if e.when > 0 && (last < 0 || e.when > eps[last].when) {
				last = i
			}
		}
		if last < 0 || !eps[last].played {
			continue // nothing watched, or mid-episode: that is "continue watching"
		}
		for i := last + 1; i < len(eps); i++ {
			if eps[i].season == 0 {
				continue // specials are never "next"
			}
			if !eps[i].played {
				if eps[i].position == 0 {
					picks = append(picks, pick{eps[i].id, eps[last].when})
				}
				break
			}
		}
	}
	if len(picks) == 0 {
		return nil, nil
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].when > picks[j].when })
	idList := make([]any, len(picks))
	rank := map[string]int{}
	for i, p := range picks {
		idList[i] = p.id
		rank[p.id] = i
	}
	items, err := s.loadItems(u, `i.id IN (?`+strings.Repeat(",?", len(idList)-1)+`)`, "", idList...)
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return rank[items[i].ID] < rank[items[j].ID] })
	return items, nil
}

func (s *Server) handleMovies(w http.ResponseWriter, r *http.Request) {
	items, err := s.loadItems(currentUser(r), `i.kind IN ('movie','video')`, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the library.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": orEmpty(items)})
}

func (s *Server) handleShows(w http.ResponseWriter, r *http.Request) {
	shows, err := s.loadShows(currentUser(r), `1=1`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the library.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shows": shows})
}

func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	shows, err := s.loadShows(u, `sh.id = ?`, id)
	if err != nil || len(shows) != 1 {
		writeErr(w, http.StatusNotFound, "That show is not in the library.")
		return
	}
	eps, err := s.loadItems(u, `sh.id = ?`, `i.season_number, i.episode_number`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the library.")
		return
	}
	type season struct {
		Number   int        `json:"number"`
		Title    string     `json:"title"`
		Episodes []itemView `json:"episodes"`
	}
	var seasons []season
	for _, e := range eps {
		if len(seasons) == 0 || seasons[len(seasons)-1].Number != e.Season {
			title := fmt.Sprintf("Season %d", e.Season)
			if e.Season == 0 {
				title = "Specials"
			}
			seasons = append(seasons, season{Number: e.Season, Title: title})
		}
		seasons[len(seasons)-1].Episodes = append(seasons[len(seasons)-1].Episodes, e)
	}
	// Specials go last.
	sort.SliceStable(seasons, func(i, j int) bool { return seasons[i].Number != 0 && seasons[j].Number == 0 })
	writeJSON(w, http.StatusOK, map[string]any{"show": shows[0], "seasons": seasons})
}

// ftsQuery turns what someone typed into a safe full-text query: each word
// is quoted and matched as a prefix, so no search syntax can be injected.
func ftsQuery(q string) string {
	var parts []string
	for _, word := range strings.Fields(q) {
		word = strings.Map(func(r rune) rune {
			if r == '"' || r < 32 {
				return -1
			}
			return r
		}, word)
		if word != "" && len(parts) < 8 {
			parts = append(parts, `"`+word+`"*`)
		}
	}
	return strings.Join(parts, " ")
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	q := ftsQuery(r.URL.Query().Get("q"))
	if q == "" || len(q) > 400 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []itemView{}, "shows": []showView{}})
		return
	}
	rows, err := s.db.Query(`SELECT item_id FROM item_fts WHERE item_fts MATCH ? ORDER BY rank LIMIT 60`, q)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "That search could not be run.")
		return
	}
	var found []any
	for rows.Next() {
		var id string
		rows.Scan(&id)
		found = append(found, id)
	}
	rows.Close()
	items, shows := []itemView{}, []showView{}
	if len(found) > 0 {
		in := `(?` + strings.Repeat(",?", len(found)-1) + `)`
		if items, err = s.loadItems(u, `i.id IN `+in, "", found...); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not read the library.")
			return
		}
		if shows, err = s.loadShows(u, `sh.id IN `+in, found...); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not read the library.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": orEmpty(items), "shows": shows})
}

var _ = sql.ErrNoRows
