package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/metadata"
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
	Method     string   `json:"method"`   // direct, remux, audio, none
	Reasons    []string `json:"reasons"`
	Overview   string   `json:"overview,omitempty"`
	Poster     bool     `json:"poster"`

	updatedAt int64
}

// Display is the name shown above the player.
func (v itemView) Display() string {
	if v.Kind == "episode" {
		name := fmt.Sprintf("%s · S%d E%d", v.ShowTitle, v.Season, v.Episode)
		if !strings.HasPrefix(v.Title, "Episode") {
			name += " · " + v.Title
		}
		return name
	}
	if v.Year > 0 {
		return fmt.Sprintf("%s (%d)", v.Title, v.Year)
	}
	return v.Title
}

// capsFor is what this sign-in's device said it can play, or nil if it has
// not said. The report is kept in the database as well as in memory: the
// page sends it once per visit, and a page left open across a restart of
// the program (the PC restarting, an update installing itself) is still
// signed in and will not send it again.
func (s *Server) capsFor(u *User) *playback.Caps {
	s.capsMu.Lock()
	defer s.capsMu.Unlock()
	if c := s.caps[u.SessionID]; c != nil {
		return c
	}
	var raw, userAgent string
	if s.db.QueryRow(`SELECT caps, user_agent FROM device_reports WHERE session_id=?`, u.SessionID).Scan(&raw, &userAgent) != nil {
		return nil
	}
	var c playback.Caps
	if json.Unmarshal([]byte(raw), &c) != nil {
		return nil
	}
	applyOverrides(&c, userAgent)
	s.rememberCaps(u.SessionID, &c)
	return &c
}

// rememberCaps keeps a report in memory. The caller holds capsMu.
func (s *Server) rememberCaps(sessionID string, c *playback.Caps) {
	if len(s.caps) > 5000 {
		s.caps = map[string]*playback.Caps{} // each is read back from the database when next needed
	}
	s.caps[sessionID] = c
}

// handleCapabilities stores what this device says it can play. The page
// measures it once per visit; nothing is guessed from the browser's name
// except the corrections below. What is kept is the report as it came: the
// corrections are applied each time it is read, so a later version of the
// program can correct differently. A report that cannot be written to the
// database is still kept in memory.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	var c playback.Caps
	if !readJSON(w, r, &c) {
		return
	}
	if len(c.Containers) > 40 || len(c.Video) > 40 || len(c.Audio) > 40 {
		writeErr(w, http.StatusBadRequest, "That report is too large.")
		return
	}
	u := currentUser(r)
	userAgent := clip(r.UserAgent(), 300)
	raw, err := json.Marshal(c) // as it came, before the corrections
	applyOverrides(&c, userAgent)
	s.capsMu.Lock()
	s.rememberCaps(u.SessionID, &c)
	s.capsMu.Unlock()
	// The report is in use from here on, whatever becomes of the write
	// below, which may have to wait its turn or may fail (a full disk).
	if err == nil {
		_, err = s.db.Exec(`INSERT INTO device_reports(session_id,caps,user_agent,updated_at) VALUES(?,?,?,?)
			ON CONFLICT(session_id) DO UPDATE SET caps=excluded.caps, user_agent=excluded.user_agent, updated_at=excluded.updated_at`,
			u.SessionID, string(raw), userAgent, time.Now().Unix())
	}
	if err != nil {
		// The page could do nothing about an error, so it is not sent one.
		s.log.Printf("what a device can play could not be stored, so it is kept only until the program next starts: %s", s.pathless(err.Error()))
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// forgetCaps drops a sign-in's report when it signs out.
func (s *Server) forgetCaps(sessionID string) {
	s.capsMu.Lock()
	delete(s.caps, sessionID)
	s.capsMu.Unlock()
	s.db.Exec(`DELETE FROM device_reports WHERE session_id=?`, sessionID)
}

// deviceReports lists, for the support bundle, what each signed-in device
// said it can play, with the browser's own description of itself. No
// names, addresses or sign-in details.
func (s *Server) deviceReports() []map[string]any {
	out := []map[string]any{}
	rows, err := s.db.Query(`SELECT d.caps, d.user_agent FROM device_reports d JOIN sessions s ON s.id = d.session_id
		WHERE s.revoked_at IS NULL ORDER BY d.updated_at DESC LIMIT 200`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var raw, userAgent string
		var c playback.Caps
		if rows.Scan(&raw, &userAgent) != nil || json.Unmarshal([]byte(raw), &c) != nil {
			continue
		}
		applyOverrides(&c, userAgent)
		out = append(out, map[string]any{"browser": userAgent, "canPlay": c})
	}
	return out
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

// version is one copy of a film or an episode. Most have one; a film kept
// as a 1080p file and as a 2160p file has two, and which of them a device
// gets is settled by preferred.
type version struct {
	fileID, rel, libPath, container string
	size, mtime, durationMs         int64
	probed                          bool

	// What judge adds.
	plan   playback.Plan // how this device would get this copy, with the file's own main sound
	pixels int           // how large its picture is; 0 until the file has been inspected
}

// before reports whether a device should be given v rather than o: the copy
// it plays most cheaply, and of two that cost the same the better picture.
// The rest only makes the answer the same every time.
func (v *version) before(o *version) bool {
	if a, b := effort(v.plan), effort(o.plan); a != b {
		return a < b
	}
	if v.pixels != o.pixels {
		return v.pixels > o.pixels
	}
	if v.size != o.size {
		return v.size > o.size // the same picture size with more spent on it
	}
	return v.rel < o.rel
}

// effort ranks the ways a copy can reach a device, cheapest first: as it
// is, repackaged, with its sound converted, with its picture converted. A
// copy that may or may not play (it has not been inspected, or the device
// has not said what it plays) comes after every copy known to play and
// before one known not to.
func effort(p playback.Plan) int {
	switch p.Playable {
	case playback.No:
		return 5
	case playback.Unknown:
		return 4
	}
	switch p.Method {
	case playback.Direct:
		return 0
	case playback.Remux:
		return 1
	case playback.Audio:
		return 2
	}
	return 3
}

// preferred is the copy to play, of the copies of one film or episode. The
// lists and Play both ask here, so what a list says about a film is about
// the very file Play then opens.
func preferred(copies []*version) *version {
	best := copies[0]
	for _, v := range copies[1:] {
		if v.before(best) {
			best = v
		}
	}
	return best
}

// judge works out how this device would get each copy (see version.plan).
func (s *Server) judge(u *User, copies []*version) error {
	byFile := make(map[string]*version, len(copies))
	media := make(map[string]*playback.Media, len(copies))
	fileIDs := make([]any, 0, len(copies))
	for _, v := range copies {
		byFile[v.fileID] = v
		media[v.fileID] = &playback.Media{Probed: v.probed, Container: v.container}
		fileIDs = append(fileIDs, v.fileID)
	}
	for start := 0; start < len(fileIDs); start += 400 {
		end := min(start+400, len(fileIDs))
		q := `SELECT file_id, kind, codec, codec_tag, bit_depth, is_default, is_commentary, language, width, height FROM streams
			WHERE kind IN ('video','audio') AND file_id IN (?` + strings.Repeat(",?", end-start-1) + `) ORDER BY file_id, idx`
		rows, err := s.db.Query(q, fileIDs[start:end]...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var fileID, kind string
			var t playback.Track
			var def, com, width, height int
			if err := rows.Scan(&fileID, &kind, &t.Codec, &t.Tag, &t.BitDepth, &def, &com, &t.Language, &width, &height); err != nil {
				rows.Close()
				return err
			}
			t.Default, t.Commentary = def != 0, com != 0
			m := media[fileID]
			if kind == "video" {
				// The picture's size is kept beside the track, not in it: it
				// ranks the copies here and plays no part in the decision.
				if len(m.Video) == 0 {
					byFile[fileID].pixels = width * height
				}
				m.Video = append(m.Video, t)
			} else {
				m.Audio = append(m.Audio, t)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	caps := s.playCaps(u)
	for _, v := range copies {
		v.plan = playback.Decide(*media[v.fileID], caps, -1)
	}
	return nil
}

// copiesOf lists the copies of one film or episode that are there to play.
func (s *Server) copiesOf(itemID string) ([]*version, error) {
	rows, err := s.db.Query(`SELECT f.id, f.rel_path, l.path, f.size, f.mtime, f.probed, f.container, f.duration_ms
		FROM versions v JOIN files f ON f.version_id=v.id JOIN libraries l ON l.id=f.library_id
		WHERE v.item_id=? AND f.missing=0 AND f.part_index=0`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var copies []*version
	for rows.Next() {
		v := &version{}
		var probed int
		if err := rows.Scan(&v.fileID, &v.rel, &v.libPath, &v.size, &v.mtime, &probed, &v.container, &v.durationMs); err != nil {
			return nil, err
		}
		v.probed = probed == 1
		copies = append(copies, v)
	}
	return copies, rows.Err()
}

// playedVersion is the copy of a film or episode that this device plays,
// or nil if no copy is there. Playing a film and remembering a pick of
// sound and subtitles both ask here, so a pick is always read against the
// file it was made on.
func (s *Server) playedVersion(u *User, itemID string) (*version, error) {
	copies, err := s.copiesOf(itemID)
	if err != nil || len(copies) == 0 {
		return nil, err
	}
	if err := s.judge(u, copies); err != nil {
		return nil, err
	}
	return preferred(copies), nil
}

// loadItems reads films and episodes matching where, with this user's
// progress and whether this device can play each one. For an item with
// several copies the answer is about the copy this device would be given
// (see preferred).
func (s *Server) loadItems(u *User, where, order string, args ...any) ([]itemView, error) {
	if order == "" {
		order = "i.sort_title"
	}
	q := `SELECT i.id, i.kind, i.title, i.year, COALESCE(i.season_number,0), COALESCE(i.episode_number,0), COALESCE(i.episode_end,0),
		COALESCE(sh.id,''), COALESCE(sh.title,''), f.id, f.rel_path, f.probed, f.container, f.duration_ms, f.size,
		COALESCE(p.position_ms,0), COALESCE(p.duration_ms,0), COALESCE(p.played,0), COALESCE(p.updated_at,0), i.overview, i.poster <> ''
		FROM items i
		LEFT JOIN items sh ON sh.id = i.show_id
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
	var progDurs []int64    // beside out: the length a player last reported for the item
	var copies [][]*version // beside out: the item's copies
	var all []*version      // every copy of every item
	at := map[string]int{}  // where each item is in out
	for rows.Next() {
		var v itemView
		c := &version{}
		var probed, played int
		var progDur int64
		if err := rows.Scan(&v.ID, &v.Kind, &v.Title, &v.Year, &v.Season, &v.Episode, &v.EpisodeEnd, &v.ShowID, &v.ShowTitle,
			&c.fileID, &c.rel, &probed, &c.container, &c.durationMs, &c.size, &v.PositionMs, &progDur, &played, &v.updatedAt, &v.Overview, &v.Poster); err != nil {
			rows.Close()
			return nil, err
		}
		c.probed = probed == 1
		all = append(all, c)
		i, seen := at[v.ID]
		if !seen { // a row for every copy of an item: the item is listed once
			i = len(out)
			at[v.ID] = i
			v.Played = played != 0
			v.Reasons = []string{}
			out = append(out, v)
			progDurs = append(progDurs, progDur)
			copies = append(copies, nil)
		}
		copies[i] = append(copies[i], c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.judge(u, all); err != nil {
		return nil, err
	}
	for i := range out {
		c := preferred(copies[i])
		v := &out[i]
		v.DurationMs = c.durationMs
		if v.DurationMs == 0 {
			v.DurationMs = progDurs[i]
		}
		v.Container = c.container
		if v.Container == "" {
			v.Container = strings.TrimPrefix(strings.ToLower(filepath.Ext(c.rel)), ".")
		}
		v.Playable, v.Method = string(c.plan.Playable), string(c.plan.Method)
		if c.plan.Reasons != nil {
			v.Reasons = c.plan.Reasons
		}
	}
	return out, nil
}

type showView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`
	Episodes int    `json:"episodes"`
	Watched  int    `json:"watched"`
	Overview string `json:"overview,omitempty"`
	Poster   bool   `json:"poster"`
}

// loadShows lists shows with how many episodes each has and how many of
// them this user has watched. The counts come from two grouped queries over
// indexes, so the list stays quick with tens of thousands of episodes.
func (s *Server) loadShows(u *User, where string, args ...any) ([]showView, error) {
	rows, err := s.db.Query(`SELECT sh.id, sh.title, sh.year, sh.overview, sh.poster <> '' FROM items sh WHERE sh.kind = 'show' AND (`+where+`) ORDER BY sh.sort_title`, args...)
	if err != nil {
		return nil, err
	}
	out := []showView{}
	index := map[string]int{}
	for rows.Next() {
		var v showView
		if err := rows.Scan(&v.ID, &v.Title, &v.Year, &v.Overview, &v.Poster); err != nil {
			rows.Close()
			return nil, err
		}
		index[v.ID] = len(out)
		out = append(out, v)
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	fill := func(q string, set func(v *showView, n int), qargs ...any) error {
		rows, err := s.db.Query(q, qargs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var n int
			rows.Scan(&id, &n)
			if i, ok := index[id]; ok {
				set(&out[i], n)
			}
		}
		return rows.Err()
	}
	if len(out) == 1 {
		if err := fill(`SELECT show_id, count(*) FROM items WHERE show_id = ? AND kind = 'episode'`, func(v *showView, n int) { v.Episodes = n }, out[0].ID); err != nil {
			return nil, err
		}
	} else if err := fill(`SELECT show_id, count(*) FROM items WHERE show_id IS NOT NULL AND kind = 'episode' GROUP BY show_id`, func(v *showView, n int) { v.Episodes = n }); err != nil {
		return nil, err
	}
	err = fill(`SELECT e.show_id, count(*) FROM progress p JOIN items e ON e.id = p.item_id WHERE p.user_id = ? AND p.played = 1 AND e.show_id IS NOT NULL GROUP BY e.show_id`,
		func(v *showView, n int) { v.Watched = n }, u.ID)
	return out, err
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
	var movies, shows, pending, unreadable, lookupsPending int
	s.db.QueryRow(`SELECT count(*) FROM items WHERE kind IN ('movie','show') AND lookup_state=0`).Scan(&lookupsPending)
	s.db.QueryRow(`SELECT count(*) FROM items i WHERE i.kind IN ('movie','video') AND EXISTS (SELECT 1 FROM versions v JOIN files f ON f.version_id=v.id WHERE v.item_id=i.id AND f.missing=0)`).Scan(&movies)
	s.db.QueryRow(`SELECT count(*) FROM items WHERE kind='show'`).Scan(&shows)
	s.db.QueryRow(`SELECT count(*) FROM files WHERE probed=0 AND missing=0`).Scan(&pending)
	s.db.QueryRow(`SELECT count(*) FROM files WHERE missing=0 AND probed=2`).Scan(&unreadable)
	writeJSON(w, http.StatusOK, map[string]any{
		"continue": orEmpty(cont), "nextUp": orEmpty(next),
		"counts":     map[string]int{"movies": movies, "shows": shows},
		"lookups":    map[string]any{"on": s.lookupsOn(), "pending": lookupsPending, "running": s.enriching.Load()},
		"inspection": map[string]any{"available": s.ffprobe != "", "pending": pending, "unreadable": unreadable, "running": s.inspecting.Load()},
	})
}

// nextUp finds, for every show the user is part-way through, the episode
// that follows the one they watched most recently.
func (s *Server) nextUp(u *User) ([]itemView, error) {
	// Step one: the shows this user has watched anything of (a short list).
	srows, err := s.db.Query(`SELECT DISTINCT e.show_id FROM progress p JOIN items e ON e.id = p.item_id WHERE p.user_id = ? AND e.show_id IS NOT NULL`, u.ID)
	if err != nil {
		return nil, err
	}
	var showIDs []any
	for srows.Next() {
		var id string
		srows.Scan(&id)
		showIDs = append(showIDs, id)
	}
	srows.Close()
	if len(showIDs) == 0 {
		return nil, nil
	}
	if len(showIDs) > 300 {
		showIDs = showIDs[:300]
	}
	// Step two: those shows' episodes in order, through the show index.
	rows, err := s.db.Query(`SELECT e.show_id, e.id, e.season_number, e.episode_number, COALESCE(p.played,0), COALESCE(p.position_ms,0), COALESCE(p.updated_at,0)
		FROM items e LEFT JOIN progress p ON p.item_id = e.id AND p.user_id = ?
		WHERE e.kind = 'episode' AND e.show_id IN (?`+strings.Repeat(",?", len(showIDs)-1)+`)
		ORDER BY e.show_id, e.season_number, e.episode_number`, append([]any{u.ID}, showIDs...)...)
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
	eps, err := s.loadItems(u, `i.show_id = ?`, `i.season_number, i.episode_number`, id)
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

// handlePoster serves a poster from the artwork folder. The file name comes
// from the database, never from the request.
func (s *Server) handlePoster(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var name string
	if !ids.Valid(id) || s.db.QueryRow(`SELECT poster FROM items WHERE id=?`, id).Scan(&name) != nil || name == "" {
		writeErr(w, http.StatusNotFound, "No poster.")
		return
	}
	dir := s.artworkDir()
	if rel, ok := strings.CutPrefix(name, "local:"); ok {
		// A picture the owner keeps beside the film, read from the library.
		if s.db.QueryRow(`SELECT l.path FROM libraries l JOIN items i ON i.library_id=l.id WHERE i.id=?`, id).Scan(&dir) != nil {
			writeErr(w, http.StatusNotFound, "No poster.")
			return
		}
		name = rel
	}
	f, st, err := openMedia(dir, name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "No poster.")
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// handleSettings reports the owner's settings. The key itself is never
// sent back, only whether one is saved.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var pending, notFound, found int
	s.db.QueryRow(`SELECT count(*) FROM items WHERE kind IN ('movie','show') AND lookup_state=0`).Scan(&pending)
	s.db.QueryRow(`SELECT count(*) FROM items WHERE kind IN ('movie','show') AND lookup_state=2`).Scan(&notFound)
	s.db.QueryRow(`SELECT count(*) FROM items WHERE kind IN ('movie','show') AND lookup_state=1`).Scan(&found)
	writeJSON(w, http.StatusOK, map[string]any{
		"tmdbKeySaved": s.setting("tmdb_key") != "",
		"online":       s.setting("online_lookups") != "off",
		"lookups":      map[string]any{"pending": pending, "found": found, "notFound": notFound, "running": s.enriching.Load(), "problem": s.lookupErr.Load()},
	})
}

func (s *Server) handleSetTMDB(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		s.setSetting("tmdb_key", "")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if len(key) > 600 || strings.ContainsAny(key, " \t\r\n") {
		writeErr(w, http.StatusBadRequest, "That does not look like a TMDB key.")
		return
	}
	if err := s.newTMDB(key).Check(r.Context()); err != nil {
		msg := "TMDB could not be reached, so the key was not saved. Check the PC's internet connection and try again."
		if errors.Is(err, metadata.ErrBadKey) {
			msg = "TMDB did not accept that key. Copy it again from TMDB's API settings page."
		}
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.setSetting("tmdb_key", key); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the key.")
		return
	}
	s.log.Printf("a TMDB key was saved")
	s.enrichSoon()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSetOnline(w http.ResponseWriter, r *http.Request) {
	var in struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	v := "off"
	if in.On {
		v = "on"
	}
	if err := s.setSetting("online_lookups", v); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the setting.")
		return
	}
	s.enrichSoon()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
