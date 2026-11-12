package server

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/Matiaspnyhus/Local-media-project/internal/auth"
	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/library"
)

// "Open in another player" is the way out for a film the browser cannot
// play well: the original file is handed to a real video player, which
// plays almost anything, at full quality.
//
// On the PC the server runs on, the film is opened in that PC's own video
// player (handleOpenLocal). Any other device gets a link to the original
// file, either as text to paste into a player or inside a small playlist
// file that VLC opens (handleHandoff, handleHandoffXSPF). The link is an
// ordinary signed stream link, as handlePlay makes: it plays that one file
// without the sign-in cookie, runs out after linkLife, and stops working
// the moment its sign-in is signed out or runs out (see handleStream).

// openInPlayer asks the operating system to open one file in whatever
// program the PC uses for videos. It is a variable so that tests can put a
// recorder in its place; nothing else may change it.
var openInPlayer = openOrRecord

// openRecordEnv names a switch for tests of the whole program. The browser
// test presses the button for real, on machines where no video player must
// start. With this variable set, the file's path is added as one line to
// the file the variable names, and nothing is opened.
const openRecordEnv = "LOCALMEDIA_OPEN_RECORD"

func openOrRecord(path string) error {
	if record := os.Getenv(openRecordEnv); record != "" {
		return recordOpen(record, path)
	}
	return openWithSystem(path)
}

// openWait is how long the operating system is given to say that it could
// not open a file. Some systems answer only when the player is closed
// again; silence for this long counts as "opened".
var openWait = 3 * time.Second

func recordOpen(record, path string) error {
	f, err := os.OpenFile(record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(path + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// homeAddresses lists the addresses other devices at home can reach this
// PC at. It is a variable so that tests do not depend on the network of
// the machine they run on.
var homeAddresses = (*Server).LANURLs

// fromThisPC reports whether a request was made by a browser on the PC the
// server runs on, showing the server's own page: the two checks that also
// guard setup (see guard).
func fromThisPC(r *http.Request) bool {
	return isLoopback(r) && sameOrigin(r)
}

// handedFile is the file behind an item, once it is known to be a real
// video file inside its library.
type handedFile struct {
	id         string // the file's, not the item's
	path       string // where it is on this PC
	title      string
	durationMs int64
}

// fileToHand finds the file an item plays from. Which file that is comes
// from the item's id and the database alone, never from anything else in
// the request. When there is none it answers the request itself and
// returns false.
func (s *Server) fileToHand(w http.ResponseWriter, r *http.Request) (handedFile, bool) {
	var f handedFile
	itemID := chi.URLParam(r, "id")
	if !ids.Valid(itemID) {
		writeErr(w, http.StatusNotFound, "Not found.")
		return f, false
	}
	var rel, libPath string
	if err := s.db.QueryRow(`SELECT f.id, f.rel_path, l.path, i.title, f.duration_ms`+playedFile, itemID).
		Scan(&f.id, &rel, &libPath, &f.title, &f.durationMs); err != nil {
		writeErr(w, http.StatusNotFound, "That file is not available right now.")
		return f, false
	}
	// Only what the library lists as a video. The operating system decides
	// what "open" means from a file's name, and for a program or a script
	// it means "run it".
	if !library.IsVideo(rel) {
		writeErr(w, http.StatusNotFound, "Not found.")
		return f, false
	}
	file, _, err := openMedia(libPath, rel) // confirms the file is a real file inside its library
	if err != nil {
		writeErr(w, http.StatusNotFound, "That file is not available right now.")
		return f, false
	}
	file.Close()
	f.path = filepath.Join(libPath, filepath.FromSlash(rel))
	if views, _ := s.loadItems(currentUser(r), `i.id = ?`, "", itemID); len(views) == 1 {
		f.title = views[0].Display()
	}
	return f, true
}

// handleOpenLocal opens a film in the video player of the PC the server
// runs on. Only a browser on that PC may ask: from anywhere else it would
// let anyone signed in start programs on somebody else's screen.
//
// The request's body is never read. The one thing that reaches the
// operating system is the file's own path.
func (s *Server) handleOpenLocal(w http.ResponseWriter, r *http.Request) {
	if !fromThisPC(r) {
		writeErr(w, http.StatusForbidden, "A film can only be opened in the PC's own video player from the browser on that PC. On this device, use the file for VLC or the link instead.")
		return
	}
	f, ok := s.fileToHand(w, r)
	if !ok {
		return
	}
	if err := openInPlayer(f.path); err != nil {
		// The file's own name stays in the log, because it says which film
		// had the problem; its folder does not (see pathless).
		why := strings.ReplaceAll(err.Error(), f.path, filepath.Base(f.path))
		s.log.Printf("could not open %s in this PC's video player: %s", filepath.Base(f.path), s.pathless(why))
		writeErr(w, http.StatusInternalServerError, "This PC could not open the film in a video player. It may have no program set up for this kind of file. VLC is a free player that plays almost anything.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// signedStream makes a fresh link to an original file for one sign-in, the
// same kind handlePlay hands to the page's own player, and says when it
// runs out.
func (s *Server) signedStream(fileID, sessionID string) (link string, expires int64) {
	expires = time.Now().Add(linkLife).Unix()
	sig := auth.SignLink(s.linkKey, fileID, expires, sessionID)
	return "/stream/" + fileID + "?e=" + strconv.FormatInt(expires, 10) + "&s=" + sessionID + "&g=" + sig, expires
}

// handoffBase is the address a player outside the browser should use for
// this PC, or false if none can be found. It is always one of the server's
// own addresses, never one the request supplied: the request's Host header
// only picks among them, so that a PC with several network connections
// hands out the address this device has just shown it can reach.
func (s *Server) handoffBase(r *http.Request) (string, bool) {
	urls := homeAddresses(s)
	for _, u := range urls {
		if strings.EqualFold(u, "http://"+r.Host) {
			return u, true
		}
	}
	if len(urls) > 0 {
		return urls[0], true // the address the QR code shows
	}
	// No home network address is known (see LANURLs for which count). The
	// address this very connection arrived at still works for whoever is
	// asking.
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok && len(a.IP) > 0 && !a.IP.IsUnspecified() {
		return "http://" + net.JoinHostPort(a.IP.String(), strconv.Itoa(a.Port)), true
	}
	if isLoopback(r) {
		return fmt.Sprintf("http://127.0.0.1:%d", s.port), true
	}
	return "", false
}

// handoffLink is a complete, freshly signed address for a film's file.
func (s *Server) handoffLink(w http.ResponseWriter, r *http.Request, f handedFile) (link string, expires int64, ok bool) {
	base, ok := s.handoffBase(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "No home network address was found for this PC, so there is no link to give to another player.")
		return "", 0, false
	}
	link, expires = s.signedStream(f.id, currentUser(r).SessionID)
	return base + link, expires, true
}

// handleHandoff gives the page a link to show as text, for pasting into a
// player that can open a network address.
func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileToHand(w, r)
	if !ok {
		return
	}
	link, expires, ok := s.handoffLink(w, r, f)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":          link,
		"expiresAt":    expires, // Unix seconds
		"validSeconds": int64(linkLife / time.Second),
		// Whether open-local would be allowed for this browser, so the page
		// knows which of the two ways out to offer.
		"thisPC": fromThisPC(r),
	})
}

// handleHandoffXSPF sends a playlist with one entry, in the XSPF format
// that VLC opens: the film's link, its title and, with ?at=<seconds>, the
// place to start from.
func (s *Server) handleHandoffXSPF(w http.ResponseWriter, r *http.Request) {
	at := 0.0
	if v := r.URL.Query().Get("at"); v != "" {
		var err error
		// No film is ten million seconds long; the limit keeps the number
		// one that fits everywhere it goes.
		if at, err = strconv.ParseFloat(v, 64); err != nil || math.IsNaN(at) || at < 0 || at > 1e7 {
			writeErr(w, http.StatusBadRequest, "The place to start from could not be read.")
			return
		}
	}
	f, ok := s.fileToHand(w, r)
	if !ok {
		return
	}
	link, _, ok := s.handoffLink(w, r, f)
	if !ok {
		return
	}
	start := int64(at) // whole seconds
	if f.durationMs > 0 && start*1000 > f.durationMs-5000 {
		start = 0 // the last few seconds: start over, as the page's own player does
	}
	w.Header().Set("Content-Type", "application/xspf+xml; charset=utf-8")
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": downloadName(f.title) + ".xspf"})
	if disposition == "" {
		disposition = `attachment; filename="film.xspf"`
	}
	w.Header().Set("Content-Disposition", disposition)
	noStore(w)
	w.Write(xspf(f.title, link, f.durationMs, start))
}

// xspf writes the playlist. Every value goes through xml.EscapeText; the
// rest is fixed text.
func xspf(title, link string, durationMs, startSeconds int64) []byte {
	var b bytes.Buffer
	text := func(before, value, after string) {
		b.WriteString(before)
		xml.EscapeText(&b, []byte(value))
		b.WriteString(after)
	}
	b.WriteString(xml.Header)
	b.WriteString(`<playlist xmlns="http://xspf.org/ns/0/" xmlns:vlc="http://www.videolan.org/vlc/playlist/ns/0/" version="1">` + "\n")
	text("  <title>", title, "</title>\n")
	b.WriteString("  <trackList>\n    <track>\n")
	text("      <location>", link, "</location>\n")
	text("      <title>", title, "</title>\n")
	if durationMs > 0 {
		text("      <duration>", strconv.FormatInt(durationMs, 10), "</duration>\n")
	}
	if startSeconds > 0 {
		// VLC's own addition to the format: options for one entry.
		b.WriteString(`      <extension application="http://www.videolan.org/vlc/playlist/0">` + "\n")
		text("        <vlc:option>", "start-time="+strconv.FormatInt(startSeconds, 10), "</vlc:option>\n")
		b.WriteString("      </extension>\n")
	}
	b.WriteString("    </track>\n  </trackList>\n</playlist>\n")
	return b.Bytes()
}

// reservedNames cannot be file names on Windows, whatever follows the dot.
var reservedNames = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true}

// downloadName turns a film's title into a file name (without its ending)
// that Windows, macOS and Linux all accept: no path separators, none of
// the characters Windows forbids, nothing invisible, not too long.
func downloadName(title string) string {
	var b strings.Builder
	n, space := 0, true // as if a space came before: none is kept at the start
	for _, c := range title {
		switch {
		case c == unicode.ReplacementChar || !unicode.IsPrint(c):
			continue
		case strings.ContainsRune(`<>:"/\|?*`, c):
			c = ' '
		}
		if c == ' ' && space {
			continue // never two spaces in a row
		}
		if n++; n > 80 {
			break
		}
		space = c == ' '
		b.WriteRune(c)
	}
	name := strings.Trim(b.String(), " .")
	if name == "" || reservedNames[strings.ToLower(name)] {
		return "film"
	}
	return name
}
