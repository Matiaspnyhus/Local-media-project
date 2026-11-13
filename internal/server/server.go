// Package server is the HTTP layer. Every route is declared in routes.go
// with a policy that says who may call it; a route without one stops the
// server from starting.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Matiaspnyhus/Local-media-project/internal/auth"
	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
	"github.com/Matiaspnyhus/Local-media-project/internal/library"
	"github.com/Matiaspnyhus/Local-media-project/internal/metadata"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// Policy says who may call a route.
type Policy int

const (
	// PolicyUnset is the zero value; a route left at it is a startup error.
	PolicyUnset Policy = iota
	// Public needs no sign-in. Only the sign-in page, its assets, the
	// health check and the sign-in call itself are public.
	Public
	// SetupLocal is allowed only before setup is finished, and only from
	// the PC the server runs on.
	SetupLocal
	// Session needs a signed-in user.
	Session
	// Owner needs the signed-in owner.
	Owner
	// SignedLink needs a valid, unexpired signature in the address. It is
	// for players that fetch media without the browser's sign-in.
	SignedLink
)

func (p Policy) String() string {
	return [...]string{"unset", "public", "setup-local", "session", "owner", "signed-link"}[p]
}

// Route is one entry in the route table.
type Route struct {
	Method  string
	Pattern string
	Policy  Policy
	Handler http.HandlerFunc
}

// Config is what New needs.
type Config struct {
	DB      *sql.DB
	DataDir string
	Version string
	Port    int
	Assets  fs.FS // the built web app
	LogOut  io.Writer
	FFprobe string  // path to ffprobe; "" means files are not inspected
	FFmpeg  string  // path to ffmpeg; "" means nothing can be repackaged or converted
	Update  Updater // nil when this copy cannot update itself
}

// Server holds the state behind the routes.
type Server struct {
	db         *sql.DB
	dataDir    string
	version    string
	port       int
	assets     fs.FS
	linkKey    []byte
	setupDone  atomic.Bool
	limiter    *loginLimiter
	log        *logger
	events     *eventLog
	scanMu     sync.Mutex
	ffprobe    string
	ffmpeg     string
	hls        *hls.Manager // nil when there is no ffmpeg
	inspectMu  sync.Mutex
	inspecting atomic.Bool
	background sync.WaitGroup
	update     Updater
	lastStream atomic.Int64
	enriching  atomic.Bool
	lookupErr  atomic.Value // string: why the last round of lookups stopped
	// newTMDB builds the lookup client; tests replace it.
	newTMDB func(key string) *metadata.Client
	capsMu  sync.Mutex
	caps    map[string]*playback.Caps // by session; the database holds them too (see capsFor)
	// converting is the file each open conversion reads, by chunk token.
	// It is how the file's folder is taken out of anything the converter
	// says about it before that is logged (see pathless).
	convMu     sync.Mutex
	converting map[string]string
	helpers    *helpers  // shared and limited runs of ffmpeg and ffprobe, see helpers.go
	watch      *watchers // who is watching right now, see watching.go
	ctx        context.Context
	stop       context.CancelFunc
	handler    http.Handler
	routes     []Route
}

type ctxKey int

const userKey ctxKey = 1

// User is the signed-in user attached to a request.
type User struct {
	ID        string
	Name      string
	Role      string
	SessionID string
}

const (
	cookieName  = "lm_session"
	sessionLife = 180 * 24 * time.Hour
	linkLife    = 12 * time.Hour
)

// New builds the server and checks the route table.
func New(cfg Config) (*Server, error) {
	s := &Server{
		db: cfg.DB, dataDir: cfg.DataDir, version: cfg.Version, port: cfg.Port, assets: cfg.Assets,
		limiter: newLoginLimiter(), events: &eventLog{}, update: cfg.Update, ffprobe: cfg.FFprobe, caps: map[string]*playback.Caps{},
		helpers: newHelpers(), watch: newWatchers(),
	}
	s.ctx, s.stop = context.WithCancel(context.Background())
	s.newTMDB = metadata.New
	s.lookupErr.Store("")
	var err error
	if s.log, err = newLogger(cfg.DataDir, cfg.LogOut); err != nil {
		return nil, err
	}
	if s.linkKey, err = s.loadOrCreateKey("link_key"); err != nil {
		return nil, err
	}
	// Before anything can be written to the cache. The program takes its
	// port before it comes here, which stops the usual second copy (the
	// Start-menu entry pressed while a copy is running) before it can
	// remove that copy's chunks. Nothing stops a copy started on purpose
	// with another port on the same data folder: it removes them here,
	// whether or not it finds a converter, as the converter's own start
	// below always has when there is one.
	removeLeftovers(cfg.DataDir)
	if cfg.FFmpeg != "" {
		s.ffmpeg = cfg.FFmpeg
		if s.hls, err = hls.New(cfg.FFmpeg, filepath.Join(cfg.DataDir, "cache", "chunks")); err != nil {
			return nil, err
		}
		// The converter's own lines quote ffmpeg, which names files by
		// their full path.
		s.hls.Log = func(format string, args ...any) { s.log.Printf("%s", s.pathless(fmt.Sprintf(format, args...))) }
		s.askConverterSoon()
	}
	var users int
	if err := s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil {
		return nil, err
	}
	s.setupDone.Store(users > 0)

	s.routes = s.routeTable()
	if err := checkRoutes(s.routes); err != nil {
		return nil, err
	}
	mux := chi.NewRouter()
	for _, rt := range s.routes {
		mux.Method(rt.Method, rt.Pattern, s.guard(rt))
	}
	mux.NotFound(s.guard(Route{Method: "GET", Pattern: "/*", Policy: Public, Handler: s.handleAsset}))
	s.handler = mux
	return s, nil
}

// checkRoutes refuses a table in which any route lacks a policy.
func checkRoutes(routes []Route) error {
	for _, rt := range routes {
		if rt.Policy <= PolicyUnset || rt.Policy > SignedLink || rt.Handler == nil {
			return fmt.Errorf("route %s %s has no access policy; refusing to start", rt.Method, rt.Pattern)
		}
	}
	return nil
}

// Close releases the log file. The database belongs to the caller.
func (s *Server) Close() error {
	s.stop()
	if s.hls != nil {
		s.hls.Close() // stops every conversion and removes its chunks
	}
	s.background.Wait() // let running inspection and lookups notice
	return s.log.file.Close()
}

// inspectSoon starts inspecting new files in the background, unless that
// is already happening.
func (s *Server) inspectSoon() {
	if s.ffprobe == "" || !s.inspecting.CompareAndSwap(false, true) {
		return
	}
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		defer s.inspecting.Store(false)
		s.inspectMu.Lock() // one background job at a time
		defer s.inspectMu.Unlock()
		res, err := library.ProbePending(s.ctx, s.db, s.ffprobe)
		if err != nil && s.ctx.Err() == nil {
			s.log.Printf("inspecting files failed: %v", err)
		}
		if res.Inspected+res.Failed > 0 {
			s.log.Printf("inspected %d files, %d could not be read", res.Inspected, res.Failed)
		}
	}()
}

func (s *Server) setting(key string) string {
	var v []byte
	s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return string(v)
}

func (s *Server) setSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, []byte(value))
	return err
}

func (s *Server) artworkDir() string { return filepath.Join(s.dataDir, "artwork") }

// lookupsOn reports whether online lookups may run: a key is saved and the
// owner has not switched them off.
func (s *Server) lookupsOn() bool {
	return s.setting("tmdb_key") != "" && s.setting("online_lookups") != "off"
}

// enrichSoon looks new films and shows up in the background.
func (s *Server) enrichSoon() {
	if !s.lookupsOn() || !s.enriching.CompareAndSwap(false, true) {
		return
	}
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		defer s.enriching.Store(false)
		s.inspectMu.Lock()
		defer s.inspectMu.Unlock()
		s.lookupErr.Store("")
		sum, err := metadata.Enrich(s.ctx, s.db, s.newTMDB(s.setting("tmdb_key")), s.artworkDir())
		if err != nil && s.ctx.Err() == nil {
			s.lookupErr.Store(err.Error())
			s.log.Printf("online lookups stopped: %v", err)
		}
		if sum.Found+sum.NotFound > 0 {
			s.log.Printf("online lookups: %d found, %d not found", sum.Found, sum.NotFound)
		}
	}()
}

// EnrichNow runs lookups and waits; tests use it.
func (s *Server) EnrichNow() {
	if s.lookupsOn() {
		metadata.Enrich(s.ctx, s.db, s.newTMDB(s.setting("tmdb_key")), s.artworkDir())
	}
}

// InspectNow inspects pending files and waits; tests use it.
func (s *Server) InspectNow() {
	if s.ffprobe != "" {
		library.ProbePending(s.ctx, s.db, s.ffprobe)
	}
}

// Routes returns the route table (used by tests to prove nothing is open).
func (s *Server) Routes() []Route { return s.routes }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) loadOrCreateKey(name string) ([]byte, error) {
	var key []byte
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, name).Scan(&key)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, name, key)
	return key, err
}

// guard wraps a handler with the checks its policy demands.
func (s *Server) guard(rt Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; media-src 'self' blob:; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")

		local := isLoopback(r)
		// Until the owner has finished setup, the server answers only the
		// PC it runs on. Otherwise anyone on the Wi-Fi could claim it.
		if !s.setupDone.Load() && !local && rt.Pattern != "/health" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Local Media</title><body style="font:16px system-ui;margin:2rem;max-width:32rem"><h1>Setup is not finished</h1><p>Finish the setup in the browser on the PC that runs Local Media, then open this page again.</p>`)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !sameOrigin(r) || r.Header.Get("X-Requested-With") != "localmedia" {
				writeErr(w, http.StatusForbidden, "This request did not come from the Local Media page.")
				return
			}
		}
		switch rt.Policy {
		case Public:
		case SetupLocal:
			if s.setupDone.Load() {
				writeErr(w, http.StatusConflict, "Setup is already finished.")
				return
			}
			if !local {
				writeErr(w, http.StatusForbidden, "Setup can only be done on the PC that runs Local Media.")
				return
			}
		case Session, Owner:
			u := s.userFromRequest(r)
			if u == nil {
				writeErr(w, http.StatusUnauthorized, "Please sign in.")
				return
			}
			if rt.Policy == Owner && u.Role != "owner" {
				writeErr(w, http.StatusForbidden, "Only the owner can do this.")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), userKey, u))
		case SignedLink:
			// The handler verifies the signature; it needs the route's
			// own parameters to do so.
		default:
			writeErr(w, http.StatusInternalServerError, "Route has no policy.")
			return
		}
		rt.Handler(s.watched(w, r, rt), r)
	}
}

func currentUser(r *http.Request) *User {
	u, _ := r.Context().Value(userKey).(*User)
	return u
}

func (s *Server) userFromRequest(r *http.Request) *User {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	var u User
	var created int64
	err = s.db.QueryRow(`SELECT u.id, u.name, u.role, s.id, s.created_at FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.revoked_at IS NULL`, auth.HashToken(c.Value)).Scan(&u.ID, &u.Name, &u.Role, &u.SessionID, &created)
	if err != nil {
		return nil
	}
	if signInExpired(created) {
		return nil
	}
	return &u
}

// signInExpired reports whether a sign-in made at created (in Unix
// seconds) has run out.
func signInExpired(created int64) bool {
	return time.Since(time.Unix(created, 0)) > sessionLife
}

// signInAlive reports whether the sign-in with this id may still be used:
// it exists, has not been signed out and has not run out. Everything
// handed out under a sign-in ends with it, by this one test: links to
// original files and the addresses of chunks, which work without the
// cookie, as much as the cookie itself.
func (s *Server) signInAlive(sessionID string) bool {
	var created int64
	var revoked sql.NullInt64
	if err := s.db.QueryRow(`SELECT created_at, revoked_at FROM sessions WHERE id=?`, sessionID).Scan(&created, &revoked); err != nil || revoked.Valid {
		return false
	}
	return !signInExpired(created)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID string) error {
	secret, hash := auth.NewToken()
	now := time.Now().Unix()
	device := r.UserAgent()
	if len(device) > 200 {
		device = device[:200]
	}
	if _, err := s.db.Exec(`INSERT INTO sessions(id, token_hash, user_id, device_name, created_at, last_seen_at) VALUES(?,?,?,?,?,?)`,
		newID(), hash, userID, device, now, now); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: secret, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionLife.Seconds()),
		// No Secure flag: the server must work over plain HTTP at home.
	})
	return nil
}

// isLoopback reports whether the request came from the PC itself. It looks
// only at the real connection, never at headers a client could forge.
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin rejects writes that another website tries to send through the
// owner's browser.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "That request could not be read.")
		return false
	}
	return true
}

// LANURLs lists the addresses other devices at home can use, home-network
// (private) addresses first.
func (s *Server) LANURLs() []string {
	var private, other []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			u := fmt.Sprintf("http://%s:%d", ip, s.port)
			if ip.IsPrivate() {
				private = append(private, u)
			} else {
				other = append(other, u)
			}
		}
	}
	return append(private, other...)
}

// handleAsset serves the built web app, falling back to its start page so
// that addresses inside the app work when opened directly.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/stream/") {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if f, err := s.assets.Open(name); err == nil {
		st, _ := f.Stat()
		f.Close()
		if st != nil && !st.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			s.serveAsset(w, r, name)
			return
		}
	}
	if _, err := fs.Stat(s.assets, "index.html"); err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "The web app was not built into this program. Run `make web` and build again.\n")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	s.serveAsset(w, r, "index.html")
}

// serveAsset writes one embedded file. (http.ServeFileFS is avoided because
// it redirects /index.html to /, which breaks opening the page directly.)
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	b, err := fs.ReadFile(s.assets, name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}

// openMedia opens a library file for reading through os.Root, which refuses
// any path that would leave the library folder.
func openMedia(libraryPath, rel string) (*os.File, os.FileInfo, error) {
	root, err := os.OpenRoot(libraryPath)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, errors.New("not a regular file")
	}
	return f, st, nil
}
