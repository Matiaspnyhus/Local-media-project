package server

import (
	"database/sql"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// An update restarts the program, and must not do that under someone who
// is watching. This file is how the server knows whether anyone is.
//
// WHAT COUNTS AS WATCHING. A sign-in (one signed-in browser or app) is
// watching when either of two things holds.
//
// The first: within the last 15 minutes (quietFor) it showed a sign of
// watching. The signs are:
//
//   - a chunk, the list of chunks or the header of a repackaged film being
//     sent to it;
//   - a piece of an original file being sent to it. A film sent as it is,
//     is usually one long answer that the device reads a little at a time,
//     so every piece that really goes out counts afresh, and a device that
//     has stopped reading stops counting;
//   - subtitle text being sent to it (a track being taken out of a film is
//     sent while it is made);
//   - its player opening a film (Play) or saving how far it has got. The
//     app's own player saves every ten seconds while a film plays, and when
//     it is paused, ends or is left. These count from the moment they are
//     asked, since a film that takes a while to open is being waited for.
//
// What is sent only counts when the answer is a success: a link that is
// refused is nobody watching.
//
// The second: it has a repackaged film open that it last asked for less
// than 4 hours ago (pausedFor). This is the paused film. A paused film
// asks for nothing, and a repackaged one does not survive a restart: its
// chunk addresses die with the program, and the page left open on it ends
// in an error when Play is pressed. So it is protected for much longer
// than the 15 minutes. A film sent as it is needs no more than those:
// after a restart the browser simply asks for the next piece. "Open" means
// the converter still holds the film: the player has not been closed, the
// sign-in not signed out, and it is not one of the films the converter has
// closed or forgotten by itself.
//
// WHAT DOES NOT. A browser tab left open on a film that has finished asks
// for nothing more. It stops counting 15 minutes after the end if the film
// was sent as it is, and 4 hours after the end if it was repackaged (the
// server cannot tell a finished repackaged film from a paused one). A
// sign-in that has been signed out, or has run out, is never watching.
// (If the database cannot say whether it has, it still counts.)
//
// Times are by the PC's clock, not by how long the program has been
// awake: a PC that slept through the night was quiet all night.
const (
	quietFor  = 15 * time.Minute
	pausedFor = 4 * time.Hour
)

// filmRoutes are the routes whose use is a sign of watching, by the
// pattern they have in the route table. A test checks that each of them is
// still in the table, and that no route a player fetches film from is
// missing here.
var filmRoutes = map[string]bool{
	"/stream/{fileID}":                    true,
	"/hls/{token}/index.m3u8":             true,
	"/hls/{token}/init.mp4":               true,
	"/hls/{token}/seg-{n}.m4s":            true,
	"/api/items/{id}/play":                true,
	"/api/items/{id}/progress":            true,
	"/api/files/{fileID}/subtitles/{key}": true,
}

// streamPiece is how much of an original file is handed to the system at
// a time (see watchedWriter.ReadFrom). At the bit rates films have, a
// piece is a few seconds of film. Tests make it smaller.
var streamPiece int64 = 4 << 20

// watchers remembers the signs of watching.
type watchers struct {
	mu    sync.Mutex
	now   func() time.Time     // the PC's clock; tests replace it
	seen  map[string]time.Time // by sign-in: its last sign of watching
	films map[string]openFilm  // by chunk token: the repackaged films that have been asked for
}

// openFilm is a repackaged film and when it was last asked for.
type openFilm struct {
	signIn string
	asked  time.Time
}

func newWatchers() *watchers {
	return &watchers{now: time.Now, seen: map[string]time.Time{}, films: map[string]openFilm{}}
}

// clock reads the PC's clock. Go's time values also carry a second clock
// that stands still while the PC sleeps; Round(0) drops it, so that
// differences are taken on the clock that kept going.
func (w *watchers) clock() time.Time { return w.now().Round(0) }

// sign notes a sign of watching by a sign-in, just now. token is the
// repackaged film it asked for, or "".
func (w *watchers) sign(signIn, token string) {
	now := w.clock()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen[signIn] = now
	if token == "" {
		return
	}
	w.films[token] = openFilm{signIn: signIn, asked: now}
	// Every film that is opened gets a token of its own. Watching clears
	// out the ones that are over, but it is only called when someone asks;
	// on a PC where nobody does, the list is kept short here.
	if len(w.films) > manyFilms {
		for t, f := range w.films {
			if now.Sub(f.asked) >= pausedFor {
				delete(w.films, t)
			}
		}
	}
}

// manyFilms is how many repackaged films are remembered before the ones
// that can no longer count are looked for and dropped.
const manyFilms = 200

// Watcher is one sign-in that is watching right now.
type Watcher struct {
	SignIn string // the sign-in's id
	Who    string // for the owner to read: "Anna on an iPhone"
}

// Watching lists the sign-ins that are watching right now (see the top of
// this file for what that means). It is empty when nobody is.
func (s *Server) Watching() []Watcher {
	w := s.watch
	now := w.clock()
	watching := map[string]bool{}
	w.mu.Lock()
	for id, at := range w.seen {
		if at.After(now) {
			// The PC's clock was set back. The sign is taken as made just
			// now, or it would count until the clock caught up with it.
			at = now
			w.seen[id] = at
		}
		if now.Sub(at) < quietFor {
			watching[id] = true
		} else if now.Sub(at) >= pausedFor {
			delete(w.seen, id) // too old to matter to either rule
		}
	}
	films := make(map[string]openFilm, len(w.films))
	for token, f := range w.films {
		if f.asked.After(now) {
			f.asked = now
			w.films[token] = f
		}
		films[token] = f
	}
	w.mu.Unlock()
	// The converter is asked without the lock held: it has one of its own.
	for token, f := range films {
		if s.hls == nil || s.hls.Owner(token) == "" || now.Sub(f.asked) >= pausedFor {
			w.mu.Lock()
			if w.films[token] == f {
				delete(w.films, token)
			}
			w.mu.Unlock()
			continue
		}
		watching[f.signIn] = true
	}
	ids := make([]string, 0, len(watching))
	for id := range watching {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []Watcher
	for _, id := range ids {
		var name, device string
		var created int64
		var revoked sql.NullInt64
		err := s.db.QueryRow(`SELECT u.name, s.device_name, s.created_at, s.revoked_at FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id=?`, id).
			Scan(&name, &device, &created, &revoked)
		// Only a clear answer takes a sign-in off the list: there is no
		// such sign-in, it has been signed out, or it has run out (the
		// test signInAlive makes). A question the database could not
		// answer is not a "no": the sign-in goes on counting, because
		// waiting a minute too long costs nothing, and restarting under a
		// film does.
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (revoked.Valid || signInExpired(created))) {
			continue
		}
		if name == "" {
			name = "Someone"
		}
		out = append(out, Watcher{SignIn: id, Who: name + " on " + deviceKind(device)})
	}
	return out
}

// watchingBesides lists who is watching apart from one sign-in: the one
// that is asking, which knows what it is doing itself.
func (s *Server) watchingBesides(signIn string) []string {
	var who []string
	for _, w := range s.Watching() {
		if w.SignIn != signIn {
			who = append(who, w.Who)
		}
	}
	return who
}

// inWords joins names the way a sentence would: "A", "A and B", "A, B and C".
func inWords(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// deviceKind says in a word or two what kind of device signed in, going by
// what its browser called itself then. It is only ever shown to the owner,
// to tell one viewer from another, so a rough answer is enough.
func deviceKind(userAgent string) string {
	ua := strings.ToLower(userAgent)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(ua, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("iphone"):
		return "an iPhone"
	case has("ipad"):
		return "an iPad"
	case has("smart-tv", "smarttv", "tizen", "web0s", "webos", "appletv", "apple tv", "googletv", "crkey", "roku", "bravia"):
		return "a TV"
	case has("android"):
		if has("mobile") {
			return "an Android phone"
		}
		return "an Android tablet"
	case has("windows"):
		return "a Windows PC"
	case has("macintosh", "mac os x"):
		return "a Mac"
	case has("cros"):
		return "a Chromebook"
	case has("linux", "x11"):
		return "a Linux PC"
	}
	return "another device"
}

// watched prepares the answer to a request so that, if the request is for
// a film route and the answer succeeds, what is sent counts as a sign of
// watching. guard calls it for every request, after its own checks.
func (s *Server) watched(w http.ResponseWriter, r *http.Request, rt Route) http.ResponseWriter {
	if !filmRoutes[rt.Pattern] {
		return w
	}
	var signIn, token string
	switch {
	case rt.Policy == Session || rt.Policy == Owner:
		// guard has checked this sign-in already, so the request itself is
		// the sign: a film that takes a while to open is being waited for.
		signIn = currentUser(r).SessionID
		s.watch.sign(signIn, "")
	case strings.HasPrefix(rt.Pattern, "/hls/"):
		// A chunk address carries no sign-in; the converter knows whose
		// film the token is. An unknown token is nobody's.
		token = chi.URLParam(r, "token")
		if s.hls != nil {
			signIn = s.hls.Owner(token)
		}
	default:
		// A link to an original file names its sign-in, and is refused by
		// its handler unless the signature covers that name.
		signIn = r.URL.Query().Get("s")
	}
	if signIn == "" {
		return w
	}
	return &watchedWriter{ResponseWriter: w, sign: func() { s.watch.sign(signIn, token) }}
}

// watchedWriter passes an answer on and notes a sign of watching whenever
// part of a successful one goes out.
type watchedWriter struct {
	http.ResponseWriter
	sign    func()
	started bool
	film    bool // the answer succeeded: what is sent is film, not an error message
}

func (w *watchedWriter) begin(status int) {
	if !w.started {
		w.started, w.film = true, status < 400
	}
}

func (w *watchedWriter) WriteHeader(status int) {
	if status >= 200 { // below that are notes sent ahead of the answer itself
		w.begin(status)
		if w.film {
			w.sign()
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *watchedWriter) Write(p []byte) (int, error) {
	w.begin(http.StatusOK)
	n, err := w.ResponseWriter.Write(p)
	if n > 0 && w.film {
		w.sign()
	}
	return n, err
}

// Flush lets a handler push out what it has written so far; subtitles
// being taken out of a film are sent that way.
func (w *watchedWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap is how http.ResponseController finds the real writer.
func (w *watchedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ReadFrom is how an original file is sent: http.ServeContent hands the
// file over whole, so that the system can send it the cheapest way it has
// without the program copying it. One such answer can last as long as the
// film, so the file is passed on a piece at a time, and every piece that
// has gone out counts as a sign. Each piece is handed on in the one form
// the quick way of sending recognises: a limit directly on the file.
func (w *watchedWriter) ReadFrom(src io.Reader) (int64, error) {
	w.begin(http.StatusOK)
	to, ok := w.ResponseWriter.(io.ReaderFrom)
	if !ok {
		// onlyWrite hides this method, or io.Copy would call it again.
		return io.Copy(onlyWrite{w}, src)
	}
	from, ok := src.(*io.LimitedReader)
	if !ok {
		from = &io.LimitedReader{R: src, N: math.MaxInt64}
	}
	var sent int64
	for from.N > 0 {
		piece := min(from.N, streamPiece)
		n, err := to.ReadFrom(&io.LimitedReader{R: from.R, N: piece})
		sent += n
		from.N -= n
		if n > 0 && w.film {
			w.sign()
		}
		if err != nil {
			return sent, err
		}
		if n < piece {
			break // the file ended before the limit
		}
	}
	return sent, nil
}

type onlyWrite struct{ io.Writer }
