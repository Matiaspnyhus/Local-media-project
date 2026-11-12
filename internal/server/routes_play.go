package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Matiaspnyhus/Local-media-project/internal/auth"
	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// textSubtitles are the subtitle formats made of text, which can be shown
// by any browser once converted. Picture subtitles (as on discs) cannot be
// shown yet.
var textSubtitles = map[string]bool{"subrip": true, "srt": true, "ass": true, "ssa": true, "webvtt": true, "vtt": true, "mov_text": true, "text": true}

type audioView struct {
	Index    int    `json:"index"`
	Label    string `json:"label"`
	Language string `json:"language"`
	Selected bool   `json:"selected"`
}

type subtitleView struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Language string `json:"language"`
	Forced   bool   `json:"forced"`
	Selected bool   `json:"selected"`
	URL      string `json:"url"`
}

// fileTracks is everything a file offers a viewer.
type fileTracks struct {
	media playback.Media
	// subs are the text subtitles, inside the file and beside it. Each has
	// a Key: "e2" is the third subtitle track inside the file, "x..." a
	// file beside it (see sidecarKey).
	subs     []playback.Subtitle
	pictures int // picture subtitles, which cannot be shown yet
}

func (s *Server) loadTracks(fileID string, probed bool, container string) (fileTracks, error) {
	ft := fileTracks{media: playback.Media{Probed: probed, Container: container}}
	rows, err := s.db.Query(`SELECT kind, codec, codec_tag, bit_depth, channels, language, title, is_default, is_forced, is_commentary, is_hearing_impaired
		FROM streams WHERE file_id=? ORDER BY idx`, fileID)
	if err != nil {
		return ft, err
	}
	nSub := 0
	for rows.Next() {
		var kind string
		var t playback.Track
		var def, forced, com, hi int
		if err := rows.Scan(&kind, &t.Codec, &t.Tag, &t.BitDepth, &t.Channels, &t.Language, &t.Title, &def, &forced, &com, &hi); err != nil {
			rows.Close()
			return ft, err
		}
		t.Default, t.Forced, t.Commentary, t.HearingImpaired = def != 0, forced != 0, com != 0, hi != 0
		switch kind {
		case "video":
			ft.media.Video = append(ft.media.Video, t)
		case "audio":
			ft.media.Audio = append(ft.media.Audio, t)
		case "subtitle":
			if textSubtitles[t.Codec] {
				ft.subs = append(ft.subs, playback.Subtitle{Key: "e" + strconv.Itoa(nSub), Language: t.Language, Forced: t.Forced,
					HearingImpaired: t.HearingImpaired, Default: t.Default, Title: t.Title})
			} else {
				ft.pictures++
			}
			nSub++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ft, err
	}
	rows, err = s.db.Query(`SELECT rel_path, format, language, is_forced FROM sidecars WHERE file_id=? ORDER BY rel_path`, fileID)
	if err != nil {
		return ft, err
	}
	defer rows.Close()
	for rows.Next() {
		var rel, format, lang string
		var forced int
		if err := rows.Scan(&rel, &format, &lang, &forced); err != nil {
			return ft, err
		}
		if textSubtitles[strings.ToLower(format)] {
			ft.subs = append(ft.subs, playback.Subtitle{Key: sidecarKey(rel), Language: lang, Forced: forced != 0})
		}
	}
	return ft, rows.Err()
}

// sidecarKey names a subtitle file beside a film in addresses: "x" and a
// fingerprint of the file's path. It is not made from the file's row in the
// database, because every scan writes those rows afresh, and an address
// handed to a player must keep working after the next scan.
func sidecarKey(relPath string) string {
	sum := sha256.Sum256([]byte(relPath))
	return "x" + hex.EncodeToString(sum[:])[:32]
}

// sidecar finds the subtitle file beside a film that key names.
func (s *Server) sidecar(fileID, key string) (relPath, format string, ok bool) {
	rows, err := s.db.Query(`SELECT rel_path, format FROM sidecars WHERE file_id=?`, fileID)
	if err != nil {
		return "", "", false
	}
	defer rows.Close()
	for rows.Next() {
		if rows.Scan(&relPath, &format) == nil && sidecarKey(relPath) == key {
			return relPath, format, true
		}
	}
	return "", "", false
}

var languageNames = map[string]string{
	"en": "English", "no": "Norwegian", "nb": "Norwegian", "nn": "Norwegian (Nynorsk)", "sv": "Swedish", "da": "Danish", "fi": "Finnish",
	"is": "Icelandic", "de": "German", "fr": "French", "es": "Spanish", "it": "Italian", "pt": "Portuguese", "nl": "Dutch",
	"pl": "Polish", "ru": "Russian", "uk": "Ukrainian", "cs": "Czech", "hu": "Hungarian", "ro": "Romanian", "el": "Greek",
	"tr": "Turkish", "ar": "Arabic", "he": "Hebrew", "hi": "Hindi", "ja": "Japanese", "ko": "Korean", "zh": "Chinese",
	"th": "Thai", "vi": "Vietnamese", "id": "Indonesian",
}

func languageName(code string) string {
	l := playback.Lang(code)
	if n := languageNames[l]; n != "" {
		return n
	}
	if l == "" || l == "und" {
		return "Unknown language"
	}
	return strings.ToUpper(l)
}

func audioLabel(t playback.Track) string {
	label := languageName(t.Language) + " · " + playback.AudioName(t.Codec)
	switch t.Channels {
	case 1:
		label += " mono"
	case 2:
		label += " stereo"
	case 6:
		label += " 5.1"
	case 8:
		label += " 7.1"
	}
	if t.Commentary || strings.Contains(strings.ToLower(t.Title), "commentary") {
		label += " (commentary)"
	} else if t.Title != "" && len(t.Title) <= 40 {
		label += " (" + t.Title + ")"
	}
	return label
}

func subtitleLabel(t playback.Subtitle, beside bool) string {
	label := languageName(t.Language)
	switch {
	case t.Forced:
		label += " (only signs and foreign lines)"
	case t.HearingImpaired:
		label += " (with sounds described)"
	}
	if beside {
		label += " · separate file"
	}
	return label
}

func (s *Server) loadPrefs(userID string) playback.Prefs {
	p := playback.Prefs{Subs: "auto"}
	s.db.QueryRow(`SELECT audio_lang, sub_lang, subs FROM prefs WHERE user_id=?`, userID).Scan(&p.AudioLang, &p.SubLang, &p.Subs)
	return p
}

func (s *Server) handleGetPrefs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.loadPrefs(currentUser(r).ID))
}

var langCode = regexp.MustCompile(`^[a-z]{0,3}$`)

func (s *Server) handleSetPrefs(w http.ResponseWriter, r *http.Request) {
	var in playback.Prefs
	if !readJSON(w, r, &in) {
		return
	}
	if !langCode.MatchString(in.AudioLang) || !langCode.MatchString(in.SubLang) || (in.Subs != "auto" && in.Subs != "always" && in.Subs != "off") {
		writeErr(w, http.StatusBadRequest, "Those preferences could not be read.")
		return
	}
	_, err := s.db.Exec(`INSERT INTO prefs(user_id,audio_lang,sub_lang,subs) VALUES(?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET audio_lang=excluded.audio_lang, sub_lang=excluded.sub_lang, subs=excluded.subs`,
		currentUser(r).ID, in.AudioLang, in.SubLang, in.Subs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save.")
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// scopeOf is what a track choice is remembered for: the show an episode
// belongs to, or the film itself.
func (s *Server) scopeOf(itemID string) (string, error) {
	var showID sql.NullString
	if err := s.db.QueryRow(`SELECT show_id FROM items WHERE id=?`, itemID).Scan(&showID); err != nil {
		return "", err
	}
	if showID.Valid && showID.String != "" {
		return showID.String, nil
	}
	return itemID, nil
}

// playedFile is the tail of the query that finds the file an item plays
// from. Playing and remembering a pick both use it, so a pick is always
// read against the file it was made on.
const playedFile = ` FROM items i JOIN versions v ON v.item_id=i.id JOIN files f ON f.version_id=v.id JOIN libraries l ON l.id=f.library_id
		WHERE i.id=? AND f.missing=0 ORDER BY f.part_index LIMIT 1`

// handleChoice remembers the sound and subtitles a viewer picked, so the
// same film, and the next episode of a show, start the same way.
//
// The page says which tracks were picked (audioIndex and subKey). What is
// kept of them, their language and kind, is read from the server's own
// record of the file. A page loaded before those two were added sends only
// two languages and whether subtitles were on, which is kept as it comes.
func (s *Server) handleChoice(w http.ResponseWriter, r *http.Request) {
	itemID := chi.URLParam(r, "id")
	var in struct {
		AudioLang  string  `json:"audioLang"`
		SubLang    string  `json:"subLang"`
		SubsOn     bool    `json:"subsOn"`
		AudioIndex *int    `json:"audioIndex"` // counted from 0, -1 for none
		SubKey     *string `json:"subKey"`     // "" for off
	}
	if !readJSON(w, r, &in) {
		return
	}
	// A language is whatever the file, or a subtitle file's name, called
	// it: usually a short code, sometimes a word ("english"). It is only
	// ever compared, so only its length is checked.
	if !ids.Valid(itemID) || len(in.AudioLang) > 40 || len(in.SubLang) > 40 {
		writeErr(w, http.StatusBadRequest, "That choice could not be read.")
		return
	}
	scope, err := s.scopeOf(itemID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	mem := playback.Memory{AudioLang: playback.Lang(in.AudioLang), SubLang: playback.Lang(in.SubLang), SubsOn: in.SubsOn}
	if in.AudioIndex != nil && in.SubKey != nil {
		var fileID, container string
		var probed int
		if s.db.QueryRow(`SELECT f.id, f.probed, f.container`+playedFile, itemID).Scan(&fileID, &probed, &container) == nil {
			if ft, err := s.loadTracks(fileID, probed == 1, container); err == nil {
				// A pick the file does not have (it changed meanwhile) is
				// not kept as one; the languages sent with it still are.
				if m, ok := playback.Remember(ft.media.Audio, ft.subs, *in.AudioIndex, *in.SubKey); ok {
					mem = m
				}
			}
		}
	}
	on, audioKind, audioIndex, subKey := 0, "", -1, ""
	if mem.SubsOn {
		on = 1
	}
	if mem.Commentary {
		audioKind = "commentary"
	}
	if mem.Pick != nil {
		audioIndex, subKey = mem.Pick.Audio, mem.Pick.Subtitle
	}
	_, err = s.db.Exec(`INSERT INTO track_memory(user_id,scope_id,audio_lang,sub_lang,subs_on,audio_index,audio_kind,sub_key,sub_kind,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id,scope_id) DO UPDATE SET audio_lang=excluded.audio_lang, sub_lang=excluded.sub_lang, subs_on=excluded.subs_on,
		audio_index=excluded.audio_index, audio_kind=excluded.audio_kind, sub_key=excluded.sub_key, sub_kind=excluded.sub_kind, updated_at=excluded.updated_at`,
		currentUser(r).ID, scope, mem.AudioLang, mem.SubLang, on, audioIndex, audioKind, subKey, mem.SubKind, time.Now().Unix())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// loadMemory reads what a viewer last picked for a show or a film, or nil
// if nothing was.
func (s *Server) loadMemory(userID, scope string) *playback.Memory {
	var m playback.Memory
	var on, audioIndex int
	var audioKind, subKey string
	if s.db.QueryRow(`SELECT audio_lang, sub_lang, subs_on, audio_index, audio_kind, sub_key, sub_kind FROM track_memory WHERE user_id=? AND scope_id=?`,
		userID, scope).Scan(&m.AudioLang, &m.SubLang, &on, &audioIndex, &audioKind, &subKey, &m.SubKind) != nil {
		return nil
	}
	m.SubsOn, m.Commentary = on != 0, audioKind == "commentary"
	if m.SubKind != "" {
		m.Pick = &playback.Pick{Audio: audioIndex, Subtitle: subKey}
	}
	return &m
}

// playCaps is what this device can do here and now: what it reported, less
// repackaging when this PC has no converter to do it with.
func (s *Server) playCaps(u *User) *playback.Caps {
	c := s.capsFor(u)
	if c == nil || s.hls != nil || !c.HLS {
		return c
	}
	without := *c
	without.HLS = false
	return &without
}

// handlePlay decides how this device gets the file and hands the player an
// address for it.
//
// When the original can be played as it is, the address is a signed link to
// the file: it works without the session cookie, so players that fetch
// media themselves can use it, and it stops working when the session is
// signed out or the link expires. Otherwise the address is a list of chunks
// (see package hls) reachable only through a random token.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	itemID := chi.URLParam(r, "id")
	if !ids.Valid(itemID) {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	var in struct {
		Audio *int `json:"audio"`
	}
	if r.Body != nil {
		json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in) // the body is optional
	}
	var fileID, rel, libPath, title, container string
	var size, mtime, durationMs int64
	var probed int
	err := s.db.QueryRow(`SELECT f.id, f.rel_path, l.path, i.title, f.size, f.mtime, f.probed, f.container, f.duration_ms`+playedFile,
		itemID).Scan(&fileID, &rel, &libPath, &title, &size, &mtime, &probed, &container, &durationMs)
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
	if views, _ := s.loadItems(u, `i.id = ?`, "", itemID); len(views) == 1 {
		title = views[0].Display()
	}
	ft, err := s.loadTracks(fileID, probed == 1, container)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the file's details.")
		return
	}

	// Which sound and subtitles to start with.
	var mem *playback.Memory
	if scope, err := s.scopeOf(itemID); err == nil {
		mem = s.loadMemory(u.ID, scope)
	}
	choice := playback.Choose(ft.media.Audio, ft.subs, s.loadPrefs(u.ID), mem)
	if in.Audio != nil && *in.Audio >= 0 && *in.Audio < len(ft.media.Audio) {
		choice.Audio = *in.Audio
	}
	caps := s.playCaps(u)
	plan := playback.Decide(ft.media, caps, choice.Audio)
	if plan.Playable == playback.No {
		// The preferred sound track may be what stands in the way (it takes
		// repackaging to switch). The file's own main track is better than
		// nothing.
		if alt := playback.Decide(ft.media, caps, -1); alt.Playable != playback.No {
			plan = alt
		}
	}

	ext := strings.ToLower(filepath.Ext(rel))
	out := map[string]any{
		"title":      title,
		"positionMs": pos,
		"durationMs": durationMs,
		"container":  strings.TrimPrefix(ext, "."),
		"explain":    plan.Explain(),
	}
	if plan.Method == playback.Remux || plan.Method == playback.Audio {
		f, _, err := openMedia(libPath, rel) // confirms the file is a real file inside its library
		if err != nil {
			writeErr(w, http.StatusNotFound, "That file is not available right now.")
			return
		}
		f.Close()
		path := filepath.Join(libPath, filepath.FromSlash(rel))
		token := s.hls.Open(hls.Spec{
			Path: path, Grid: s.grid(r.Context(), fileID, path, size, mtime, durationMs),
			VideoCodec: ft.media.Video[0].Codec, AudioTrack: plan.AudioTrack, AudioCopy: plan.AudioCopy, Owner: u.SessionID,
		})
		s.noteConversion(token, path)
		out["url"] = "/hls/" + token + "/index.m3u8"
		out["mimeType"] = "application/vnd.apple.mpegurl"
		out["hls"] = token
	} else {
		exp := time.Now().Add(linkLife).Unix()
		sig := auth.SignLink(s.linkKey, fileID, exp, u.SessionID)
		out["url"] = "/stream/" + fileID + "?e=" + strconv.FormatInt(exp, 10) + "&s=" + u.SessionID + "&g=" + sig
		out["mimeType"] = mediaType(rel, container)
	}
	planOut := map[string]any{"playable": plan.Playable, "method": plan.Method, "reasons": plan.Reasons, "audioTrack": plan.AudioTrack, "audioCopy": plan.AudioCopy, "hasAudio": nil,
		"deviceUnknown": plan.DeviceUnknown}
	if ft.media.Probed {
		planOut["hasAudio"] = plan.HasAudio
	}
	out["plan"] = planOut

	audio := []audioView{}
	for i, t := range ft.media.Audio {
		audio = append(audio, audioView{Index: i, Label: audioLabel(t), Language: playback.Lang(t.Language), Selected: i == plan.AudioTrack})
	}
	subs := []subtitleView{}
	for i, t := range ft.subs {
		subs = append(subs, subtitleView{Key: t.Key, Label: subtitleLabel(t, strings.HasPrefix(t.Key, "x")), Language: playback.Lang(t.Language),
			Forced: t.Forced, Selected: i == choice.Subtitle, URL: "/api/files/" + fileID + "/subtitles/" + t.Key})
	}
	out["audio"], out["subtitles"], out["pictureSubtitles"] = audio, subs, ft.pictures
	writeJSON(w, http.StatusOK, out)
}

// hlsOwner checks a chunk address: the token must be a live session's, and
// the sign-in it belongs to must still be signed in.
func (s *Server) hlsOwner(w http.ResponseWriter, r *http.Request) (token string, ok bool) {
	token = chi.URLParam(r, "token")
	owner := ""
	if s.hls != nil && ids.Valid(token) {
		owner = s.hls.Owner(token)
	}
	if owner == "" {
		writeErr(w, http.StatusForbidden, "This link is not valid any more.")
		return "", false
	}
	var revoked sql.NullInt64
	if err := s.db.QueryRow(`SELECT revoked_at FROM sessions WHERE id=?`, owner).Scan(&revoked); err != nil || revoked.Valid {
		s.hls.Drop(owner)
		writeErr(w, http.StatusForbidden, "This link is not valid any more.")
		return "", false
	}
	return token, true
}

func hlsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hls.ErrNoSession), errors.Is(err, hls.ErrPastEnd):
		writeErr(w, http.StatusNotFound, "Not found.")
	case errors.Is(err, hls.ErrSuperseded):
		// A newer request moved the converter while this one waited. Most
		// often this request's player has moved on too and is not even
		// listening. If it does still want the chunk, it must ask again:
		// the app's player (hls.js) does that after an answer in the 500s
		// and gives up for good after one in the 400s.
		w.Header().Set("Retry-After", "1")
		writeErr(w, http.StatusServiceUnavailable, "The film was moved to another place while this part was being prepared. Ask for it again.")
	case errors.Is(err, context.Canceled):
		writeErr(w, http.StatusConflict, "The player moved elsewhere in the film.")
	default:
		writeErr(w, http.StatusInternalServerError, "This part could not be prepared.")
	}
}

// The three chunk routes have the SignedLink policy: no cookie is needed,
// because phones and TVs fetch media with players that do not send one.

func (s *Server) handleHLSList(w http.ResponseWriter, r *http.Request) {
	token, ok := s.hlsOwner(w, r)
	if !ok {
		return
	}
	list, err := s.hls.Playlist(token)
	if err != nil {
		hlsError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(list)
}

func (s *Server) handleHLSInit(w http.ResponseWriter, r *http.Request) {
	token, ok := s.hlsOwner(w, r)
	if !ok {
		return
	}
	b, err := s.hls.Init(r.Context(), token)
	if err != nil {
		s.hlsFailed(w, "header", err)
		return
	}
	s.lastStream.Store(time.Now().Unix())
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (s *Server) handleHLSSegment(w http.ResponseWriter, r *http.Request) {
	token, ok := s.hlsOwner(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n < 0 || n > 1<<20 {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	b, err := s.hls.Segment(r.Context(), token, n)
	if err != nil {
		s.hlsFailed(w, "chunk "+strconv.Itoa(n), err)
		return
	}
	s.lastStream.Store(time.Now().Unix())
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (s *Server) hlsFailed(w http.ResponseWriter, what string, err error) {
	if !errors.Is(err, hls.ErrNoSession) && !errors.Is(err, hls.ErrPastEnd) && !errors.Is(err, hls.ErrSuperseded) && !errors.Is(err, context.Canceled) {
		// The error quotes ffmpeg, which names files by their full path.
		detail := s.pathless(what + ": " + err.Error())
		s.log.Printf("converting: %s failed", detail)
		s.events.add(clientEvent{Time: time.Now().UTC().Format(time.RFC3339), Kind: "convert-failed", Detail: detail})
	}
	hlsError(w, err)
}

// noteConversion remembers which file an open conversion reads (see
// pathless), and forgets the ones whose conversion is over.
func (s *Server) noteConversion(token, path string) {
	s.convMu.Lock()
	var known []string
	for t := range s.converting {
		known = append(known, t)
	}
	s.convMu.Unlock()
	var over []string
	for _, t := range known {
		if s.hls.Owner(t) == "" { // asked without convMu held: the converter has a lock of its own
			over = append(over, t)
		}
	}
	s.convMu.Lock()
	defer s.convMu.Unlock()
	for _, t := range over {
		delete(s.converting, t)
	}
	if s.converting == nil {
		s.converting = map[string]string{}
	}
	s.converting[token] = path
}

// pathless takes folder names out of text that quotes the converter or the
// operating system, before the text is logged or kept for the support
// bundle. ffmpeg names a file it cannot open by its full path, and on the
// owner's PC that path holds their account name, or the name or address of
// their network drive. The file's own name stays, because it says which
// film had the problem. The data folder, which is under the account's name
// too, becomes a placeholder.
func (s *Server) pathless(text string) string {
	s.convMu.Lock()
	for _, p := range s.converting {
		text = strings.ReplaceAll(text, p, filepath.Base(p))
	}
	s.convMu.Unlock()
	if dir := filepath.Clean(s.dataDir); len(dir) > 1 {
		text = strings.ReplaceAll(text, dir, "[data folder]")
	}
	return text
}

// handleHLSStop ends a conversion at once when its viewer closes the player.
func (s *Server) handleHLSStop(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if !ids.Valid(token) {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	if s.hls != nil && s.hls.Owner(token) == currentUser(r).SessionID {
		s.hls.Stop(token)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

var subtitleKey = regexp.MustCompile(`^(e[0-9]{1,3}|x[0-9a-f]{32})$`)

// handleSubtitle serves one subtitle as WebVTT, the text format browsers
// show. Files beside the video in the common formats are converted here;
// tracks inside the video are taken out by ffmpeg, sent as they come, and
// kept for next time.
func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	fileID, key := chi.URLParam(r, "fileID"), chi.URLParam(r, "key")
	if !ids.Valid(fileID) || !subtitleKey.MatchString(key) {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	var libPath, rel string
	var size, mtime int64
	if err := s.db.QueryRow(`SELECT l.path, f.rel_path, f.size, f.mtime FROM files f JOIN libraries l ON l.id=f.library_id WHERE f.id=? AND f.missing=0`,
		fileID).Scan(&libPath, &rel, &size, &mtime); err != nil {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if key[0] == 'x' {
		subRel, format, ok := s.sidecar(fileID, key)
		if !ok {
			writeErr(w, http.StatusNotFound, "Not found.")
			return
		}
		f, st, err := openMedia(libPath, subRel)
		if err != nil || st.Size() > 20<<20 {
			writeErr(w, http.StatusNotFound, "That subtitle file is not available.")
			return
		}
		raw, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			writeErr(w, http.StatusNotFound, "That subtitle file is not available.")
			return
		}
		switch strings.ToLower(format) {
		case "srt", "subrip":
			w.Write(srtToVTT(raw))
			return
		case "vtt", "webvtt":
			w.Write(decodeText(raw))
			return
		}
		s.subtitleByFFmpeg(w, r, filepath.Join(libPath, filepath.FromSlash(subRel)), 0, fmt.Sprintf("%s-%s-%d", fileID, key, st.ModTime().Unix()))
		return
	}
	f, _, err := openMedia(libPath, rel)
	if err != nil {
		writeErr(w, http.StatusNotFound, "That file is not available right now.")
		return
	}
	f.Close()
	n, _ := strconv.Atoi(key[1:])
	var codec string
	if err := s.db.QueryRow(`SELECT codec FROM streams WHERE file_id=? AND kind='subtitle' ORDER BY idx LIMIT 1 OFFSET ?`, fileID, n).Scan(&codec); err != nil || !textSubtitles[codec] {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	s.subtitleByFFmpeg(w, r, filepath.Join(libPath, filepath.FromSlash(rel)), n, fmt.Sprintf("%s-%s-%d-%d", fileID, key, size, mtime))
}

// handleDiagnostics says what this device reported it can play and what the
// PC has to convert with: the facts behind every "why is this converting?".
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	out := map[string]any{
		"device":    s.capsFor(u),
		"inspector": s.ffprobe != "",
		"converter": s.hls != nil,
		"running":   0, // conversions at work this moment
		"open":      0, // films open for conversion, working or not
		"userAgent": r.UserAgent(),
	}
	if s.hls != nil {
		out["running"], out["open"] = s.hls.Running(), s.hls.Sessions()
	}
	writeJSON(w, http.StatusOK, out)
}
