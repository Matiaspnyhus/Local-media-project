package server

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/Matiaspnyhus/Local-media-project/internal/auth"
	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/library"
)

// routeTable is the complete list of things the server answers. Adding a
// route means choosing its policy here; there is no other way in.
func (s *Server) routeTable() []Route {
	return []Route{
		{"GET", "/health", Public, s.handleHealth},
		{"GET", "/api/state", Public, s.handleState},
		{"POST", "/api/login", Public, s.handleLogin},

		{"GET", "/api/setup/folders", SetupLocal, s.handleFolders},
		{"POST", "/api/setup", SetupLocal, s.handleSetup},

		{"POST", "/api/logout", Session, s.handleLogout},
		{"POST", "/api/capabilities", Session, s.handleCapabilities},
		{"GET", "/api/home", Session, s.handleHome},
		{"GET", "/api/movies", Session, s.handleMovies},
		{"GET", "/api/shows", Session, s.handleShows},
		{"GET", "/api/shows/{id}", Session, s.handleShow},
		{"GET", "/api/search", Session, s.handleSearch},
		{"POST", "/api/items/{id}/play", Session, s.handlePlay},
		{"GET", "/api/items/{id}/poster", Session, s.handlePoster},
		{"PUT", "/api/items/{id}/progress", Session, s.handleProgress},
		{"GET", "/api/connect", Session, s.handleConnect},
		{"GET", "/api/connect/qr.png", Session, s.handleQR},
		{"POST", "/api/client-events", Session, s.handleClientEvent},

		{"GET", "/api/folders", Owner, s.handleFolders},
		{"GET", "/api/libraries", Owner, s.handleLibraries},
		{"POST", "/api/libraries", Owner, s.handleAddLibrary},
		{"POST", "/api/libraries/scan", Owner, s.handleScan},
		{"GET", "/api/support-bundle", Owner, s.handleSupportBundle},
		{"GET", "/api/settings", Owner, s.handleSettings},
		{"GET", "/api/update", Owner, s.handleUpdateStatus},
		{"POST", "/api/update/check", Owner, s.handleUpdateCheck},
		{"POST", "/api/update/install", Owner, s.handleUpdateInstall},
		{"POST", "/api/update/undo", Owner, s.handleUpdateUndo},
		{"PUT", "/api/settings/auto-update", Owner, s.handleSetAutoUpdate},
		{"PUT", "/api/settings/tmdb", Owner, s.handleSetTMDB},
		{"PUT", "/api/settings/online", Owner, s.handleSetOnline},

		{"GET", "/stream/{fileID}", SignedLink, s.handleStream},
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version})
}

// handleState tells the page which screen to show. It is public, so it
// reveals nothing beyond "set up or not" and the caller's own sign-in.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"setupDone": s.setupDone.Load(), "version": s.version, "user": nil}
	if u := s.userFromRequest(r); u != nil {
		out["user"] = map[string]string{"name": u.Name, "role": u.Role}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleFolders(w http.ResponseWriter, r *http.Request) {
	l, err := library.ListDirs(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Folder   string `json:"folder"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 64 {
		writeErr(w, http.StatusBadRequest, "Choose a user name.")
		return
	}
	if err := auth.ValidatePassword(in.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Folder == "" {
		writeErr(w, http.StatusBadRequest, "Choose the folder that holds your videos.")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the password.")
		return
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.setupDone.Load() {
		writeErr(w, http.StatusConflict, "Setup is already finished.")
		return
	}
	libID, err := library.Add(s.db, in.Folder)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	userID := ids.New()
	if _, err := s.db.Exec(`INSERT INTO users(id,name,password_hash,role,created_at) VALUES(?,?,?,?,?)`,
		userID, in.Name, hash, "owner", time.Now().Unix()); err != nil {
		s.db.Exec(`DELETE FROM libraries WHERE id=?`, libID)
		writeErr(w, http.StatusInternalServerError, "Could not create the account.")
		return
	}
	s.setupDone.Store(true)
	res, err := library.Scan(s.db, libID)
	if err != nil {
		s.log.Printf("first scan failed: %v", err)
	}
	s.log.Printf("setup finished; first scan found %d video files", res.Total)
	s.inspectSoon()
	s.enrichSoon()
	if err := s.startSession(w, r, userID); err != nil {
		writeErr(w, http.StatusInternalServerError, "Account created, but sign-in failed. Please sign in.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scan": res})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		w.Header().Set("Retry-After", strconv.Itoa(int(loginWindow.Seconds())))
		writeErr(w, http.StatusTooManyRequests, "Too many wrong tries. Wait ten minutes and try again.")
		return
	}
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	var id, hash string
	err := s.db.QueryRow(`SELECT id, password_hash FROM users WHERE name = ?`, strings.TrimSpace(in.Name)).Scan(&id, &hash)
	if err != nil {
		// Spend the same effort as a real check, so a wrong user name
		// and a wrong password look the same from outside.
		auth.VerifyPassword("$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", in.Password)
	}
	if err != nil || !auth.VerifyPassword(hash, in.Password) {
		s.limiter.fail(ip)
		s.log.Printf("failed sign-in from %s", ip)
		writeErr(w, http.StatusUnauthorized, "Wrong user name or password.")
		return
	}
	s.limiter.reset(ip)
	if err := s.startSession(w, r, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not sign in.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	s.db.Exec(`UPDATE sessions SET revoked_at=? WHERE id=?`, time.Now().Unix(), u.SessionID)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePlay hands the player a signed link to the file. The link works
// without the session cookie, so players that fetch media themselves
// (AirPlay, external players) can use it, and it stops working when the
// session is signed out or the link expires.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	itemID := chi.URLParam(r, "id")
	if !ids.Valid(itemID) {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	var fileID, rel, title string
	err := s.db.QueryRow(`SELECT f.id, f.rel_path, i.title FROM items i JOIN versions v ON v.item_id=i.id
		JOIN files f ON f.version_id=v.id WHERE i.id=? AND f.missing=0 ORDER BY f.part_index LIMIT 1`, itemID).Scan(&fileID, &rel, &title)
	if err != nil {
		writeErr(w, http.StatusNotFound, "That file is not available right now.")
		return
	}
	var pos, dur int64
	var played int
	s.db.QueryRow(`SELECT position_ms, duration_ms, played FROM progress WHERE user_id=? AND item_id=?`, u.ID, itemID).Scan(&pos, &dur, &played)
	if played != 0 {
		pos = 0
	}
	exp := time.Now().Add(linkLife).Unix()
	sig := auth.SignLink(s.linkKey, fileID, exp, u.SessionID)
	ext := strings.ToLower(filepath.Ext(rel))
	views, _ := s.loadItems(u, `i.id = ?`, "", itemID)
	plan := map[string]any{"direct": "unknown", "reasons": []string{}, "hasAudio": nil}
	if len(views) == 1 {
		title = views[0].Display()
		plan = map[string]any{"direct": views[0].Playable, "reasons": views[0].Reasons, "hasAudio": views[0].hasAudio}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":       plan,
		"title":      title,
		"url":        "/stream/" + fileID + "?e=" + strconv.FormatInt(exp, 10) + "&s=" + u.SessionID + "&g=" + sig,
		"mimeType":   mediaTypes[ext],
		"container":  strings.TrimPrefix(ext, "."),
		"positionMs": pos,
	})
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	itemID := chi.URLParam(r, "id")
	var in struct {
		PositionMs int64 `json:"positionMs"`
		DurationMs int64 `json:"durationMs"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !ids.Valid(itemID) || in.PositionMs < 0 || in.DurationMs < 0 {
		writeErr(w, http.StatusBadRequest, "Bad progress.")
		return
	}
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM items WHERE id=?`, itemID).Scan(&one); err != nil {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	played := 0
	if in.DurationMs > 0 && in.PositionMs*100 >= in.DurationMs*92 {
		played = 1
	}
	_, err := s.db.Exec(`INSERT INTO progress(user_id,item_id,position_ms,duration_ms,played,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(user_id,item_id) DO UPDATE SET position_ms=excluded.position_ms, duration_ms=excluded.duration_ms,
		played=excluded.played, updated_at=excluded.updated_at`, u.ID, itemID, in.PositionMs, in.DurationMs, played, time.Now().Unix())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save progress.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"urls": s.LANURLs()})
}

// handleQR draws a QR code for one of the server's own home addresses. It
// takes an index, never a caller-supplied address.
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	urls := s.LANURLs()
	i, _ := strconv.Atoi(r.URL.Query().Get("i"))
	if i < 0 || i >= len(urls) {
		writeErr(w, http.StatusNotFound, "No home network address found.")
		return
	}
	png, err := qrcode.Encode(urls[i], qrcode.Medium, 320)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not draw the code.")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	noStore(w)
	w.Write(png)
}

func (s *Server) handleClientEvent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string `json:"kind"`
		ItemID string `json:"itemId"`
		Detail string `json:"detail"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ev := clientEvent{Time: time.Now().UTC().Format(time.RFC3339), Kind: clip(in.Kind, 40), Detail: clip(in.Detail, 300), UserAgent: clip(r.UserAgent(), 200)}
	if ids.Valid(in.ItemID) {
		s.db.QueryRow(`SELECT f.rel_path FROM versions v JOIN files f ON f.version_id=v.id WHERE v.item_id=? LIMIT 1`, in.ItemID).Scan(&ev.File)
	}
	s.events.add(ev)
	s.log.Printf("client event %s: %s", ev.Kind, ev.Detail)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT l.id, l.name, l.path, (SELECT count(*) FROM files f WHERE f.library_id=l.id AND f.missing=0) FROM libraries l ORDER BY l.created_at`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the folders.")
		return
	}
	defer rows.Close()
	type lib struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Path  string `json:"path"`
		Files int    `json:"files"`
	}
	out := []lib{}
	for rows.Next() {
		var l lib
		rows.Scan(&l.ID, &l.Name, &l.Path, &l.Files)
		out = append(out, l)
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": out})
}

func (s *Server) handleAddLibrary(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Folder string `json:"folder"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	id, err := library.Add(s.db, in.Folder)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, _ := library.Scan(s.db, id)
	s.inspectSoon()
	s.enrichSoon()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scan": res})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	res, err := s.scanAll()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "The scan failed.")
		return
	}
	s.inspectSoon()
	s.enrichSoon()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scan": res})
}

func (s *Server) scanAll() (library.ScanResult, error) {
	var total library.ScanResult
	rows, err := s.db.Query(`SELECT id FROM libraries`)
	if err != nil {
		return total, err
	}
	var libs []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		libs = append(libs, id)
	}
	rows.Close()
	for _, id := range libs {
		res, err := library.Scan(s.db, id)
		if err != nil {
			s.log.Printf("scan failed: %v", err)
			continue
		}
		total.Added += res.Added
		total.Moved += res.Moved
		total.Missing += res.Missing
		total.Total += res.Total
	}
	return total, nil
}

// ScanAll rescans every library; main calls it at start-up.
func (s *Server) ScanAll() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if res, err := s.scanAll(); err == nil {
		s.log.Printf("scan: %d video files, %d new, %d moved, %d missing", res.Total, res.Added, res.Moved, res.Missing)
	}
	s.inspectSoon()
	s.enrichSoon()
}

// handleStream serves the original file with range requests. Its policy is
// SignedLink: the address must carry a signature made by handlePlay that has
// not expired and whose session is still signed in.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileID")
	q := r.URL.Query()
	exp, err := strconv.ParseInt(q.Get("e"), 10, 64)
	sessionID, sig := q.Get("s"), q.Get("g")
	if err != nil || !ids.Valid(fileID) || !ids.Valid(sessionID) ||
		!auth.VerifyLink(s.linkKey, fileID, exp, sessionID, sig) || time.Now().Unix() > exp {
		writeErr(w, http.StatusForbidden, "This link is not valid any more.")
		return
	}
	var revoked sql.NullInt64
	if err := s.db.QueryRow(`SELECT revoked_at FROM sessions WHERE id=?`, sessionID).Scan(&revoked); err != nil || revoked.Valid {
		writeErr(w, http.StatusForbidden, "This link is not valid any more.")
		return
	}
	var libPath, rel string
	err = s.db.QueryRow(`SELECT l.path, f.rel_path FROM files f JOIN libraries l ON l.id=f.library_id WHERE f.id=?`, fileID).Scan(&libPath, &rel)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	f, st, err := openMedia(libPath, rel)
	if err != nil {
		writeErr(w, http.StatusNotFound, "That file is not available right now.")
		return
	}
	defer f.Close()
	s.lastStream.Store(time.Now().Unix())
	if ct := mediaTypes[strings.ToLower(filepath.Ext(rel))]; ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	noStore(w)
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// handleSupportBundle builds the one file the owner sends back after a
// checklist: what the program is, what failed to play, and recent log lines
// with addresses and secrets removed. No video, no passwords.
func (s *Server) handleSupportBundle(w http.ResponseWriter, r *http.Request) {
	var items, missing, libs, users int
	s.db.QueryRow(`SELECT count(*) FROM files WHERE missing=0`).Scan(&items)
	s.db.QueryRow(`SELECT count(*) FROM files WHERE missing=1`).Scan(&missing)
	s.db.QueryRow(`SELECT count(*) FROM libraries`).Scan(&libs)
	s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	byExt := map[string]int{}
	if rows, err := s.db.Query(`SELECT rel_path FROM files WHERE missing=0`); err == nil {
		for rows.Next() {
			var rel string
			rows.Scan(&rel)
			byExt[strings.ToLower(filepath.Ext(rel))]++
		}
		rows.Close()
	}
	info := map[string]any{
		"version": s.version, "schema": db.SchemaVersion(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"created": time.Now().UTC().Format(time.RFC3339), "videoFiles": items, "missingFiles": missing,
		"folders": libs, "users": users, "filesByType": byExt, "homeAddresses": len(s.LANURLs()),
		"requestedFrom": clip(r.UserAgent(), 200),
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="localmedia-support-`+time.Now().UTC().Format("20060102-150405")+`.zip"`)
	noStore(w)
	zw := zip.NewWriter(w)
	defer zw.Close()
	add := func(name string, v any) {
		f, err := zw.Create(name)
		if err != nil {
			return
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		enc.Encode(v)
	}
	// What the library could not match online, so the recogniser can be
	// improved. These are titles as read from file names, nothing more.
	unmatched := []string{}
	if rows, err := s.db.Query(`SELECT kind || ': ' || title || CASE WHEN year>0 THEN ' (' || year || ')' ELSE '' END FROM items WHERE kind IN ('movie','show') AND lookup_state=2 ORDER BY sort_title LIMIT 2000`); err == nil {
		for rows.Next() {
			var line string
			rows.Scan(&line)
			unmatched = append(unmatched, line)
		}
		rows.Close()
	}
	info["onlineLookups"] = map[string]any{"keySaved": s.setting("tmdb_key") != "", "on": s.lookupsOn(), "lastProblem": s.lookupErr.Load()}
	add("info.json", info)
	add("not-found-online.json", unmatched)
	add("playback-problems.json", s.events.all())
	if f, err := zw.Create("log.txt"); err == nil {
		f.Write(s.log.tail(512 << 10))
	}
	if f, err := zw.Create("README.txt"); err == nil {
		f.Write([]byte("Local Media support bundle.\r\n\r\nContains: program version, counts of files by type, the names of files that failed to play,\r\nand recent log lines with network addresses and secrets removed.\r\nContains no video, no passwords and no list of your whole library.\r\n\r\nAttach this file to the chat. Do not post it on GitHub.\r\n"))
	}
}

var _ = errors.New
