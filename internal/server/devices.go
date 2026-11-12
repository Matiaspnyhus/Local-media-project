package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
)

// A device is one browser on a phone, a PC or a TV. It is told apart from
// the others by a cookie of its own, which outlives signing out: what was
// learned about a phone (see lessons.go) is about the phone, whoever is
// signed in on it.
//
// The cookie grants nothing. No route lets anyone in, or shows anyone
// anything, because of it; it only says which device's lessons to plan a
// film with, and that only for a request that is signed in already.
//
// What is kept about a device is a kind ("iPhone", "Windows PC, Chrome"),
// worked out from how the browser describes itself. Never a name.

const (
	deviceCookie = "lm_device"
	// deviceLife is how long the cookie lasts. Browsers keep none for
	// longer than 400 days. Every sign-in sets it afresh, and a sign-in
	// lasts less than that, so a device in use never loses it.
	deviceLife = 400 * 24 * time.Hour
	// deviceSeenEvery is how often at most a device's "last seen" is
	// written down: not on every request.
	deviceSeenEvery = time.Hour
)

// deviceCookieValue is what the cookie of a device holds: the device's id
// and a signature only this server can make. An id alone is not accepted
// back, so whoever is shown a device's id (the owner, in the list of
// devices) cannot pass for that device.
func (s *Server) deviceCookieValue(id string) string {
	mac := hmac.New(sha256.New, s.deviceKey)
	mac.Write([]byte("device\n" + id))
	return id + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// cookieDevice reads the device a request's cookie names: "" unless the
// cookie is one this server made. Whether the device is still known is for
// the caller to look up.
func (s *Server) cookieDevice(r *http.Request) string {
	c, err := r.Cookie(deviceCookie)
	if err != nil || len(c.Value) > 200 {
		return ""
	}
	id, _, _ := strings.Cut(c.Value, ".")
	if !ids.Valid(id) || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.deviceCookieValue(id))) != 1 {
		return ""
	}
	return id
}

func (s *Server) setDeviceCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: s.deviceCookieValue(id), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(deviceLife.Seconds()),
		// No Secure flag, as for the sign-in cookie: the server must work
		// over plain HTTP at home.
	})
}

// newDevice makes a device for the browser that sent r.
func (s *Server) newDevice(r *http.Request) (string, error) {
	now := s.now().Unix()
	// Devices whose cookie has run out can never come back. They go, and
	// what was learned about them with them.
	s.db.Exec(`DELETE FROM devices WHERE last_seen_at < ?`, now-int64(deviceLife.Seconds()))
	id := newID()
	_, err := s.db.Exec(`INSERT INTO devices(id,label,created_at,last_seen_at) VALUES(?,?,?,?)`, id, deviceLabel(r.UserAgent()), now, now)
	return id, err
}

// knownDevice reports whether a device is still known, and notes that it
// was seen just now if that has not been noted for a while.
func (s *Server) knownDevice(id string, r *http.Request) bool {
	var seen int64
	var label string
	if s.db.QueryRow(`SELECT last_seen_at, label FROM devices WHERE id=?`, id).Scan(&seen, &label) != nil {
		return false
	}
	s.deviceSeen(id, seen, label, r)
	return true
}

// deviceSeen notes that a device was seen just now, and what kind of
// device it says it is today (a browser that is updated, or set to ask for
// the desktop version of pages, describes itself differently).
func (s *Server) deviceSeen(id string, seen int64, label string, r *http.Request) {
	now := s.now().Unix()
	if now-seen < int64(deviceSeenEvery.Seconds()) {
		return
	}
	if l := deviceLabel(r.UserAgent()); l != "Browser" {
		label = l
	}
	s.db.Exec(`UPDATE devices SET last_seen_at=?, label=? WHERE id=?`, now, label, id)
}

// signInDevice finds the device a new sign-in is made on: the one its
// cookie names, if this server knows it, and a new one otherwise. The
// cookie is set afresh either way, so that it lasts from today. A cookie
// with anything else in it is replaced, not believed.
func (s *Server) signInDevice(w http.ResponseWriter, r *http.Request) (sql.NullString, error) {
	id := s.cookieDevice(r)
	if id == "" || !s.knownDevice(id, r) {
		var err error
		if id, err = s.newDevice(r); err != nil {
			return sql.NullString{}, err
		}
	}
	s.setDeviceCookie(w, id)
	return sql.NullString{String: id, Valid: true}, nil
}

// requestDevice settles which device a signed-in request comes from, and
// notes it in u. The page does nothing for this: it happens on whatever
// the browser asks for first.
//
//   - The cookie names a device this server knows: that is the device,
//     and the sign-in is noted as being on it.
//   - There is no such cookie, and the sign-in has a device: the cookie
//     is handed out again.
//   - Neither (a sign-in from before there were devices): a device is
//     made now.
//
// When a device cannot be made (the disk is full), the request goes on
// without one and its films are planned as if nothing had been learned.
func (s *Server) requestDevice(w http.ResponseWriter, r *http.Request, u *User, seen sql.NullInt64, label sql.NullString) {
	fromCookie := s.cookieDevice(r)
	switch {
	case fromCookie != "" && fromCookie == u.DeviceID:
		s.deviceSeen(u.DeviceID, seen.Int64, label.String, r)
		return
	case fromCookie != "" && s.knownDevice(fromCookie, r):
		// The browser has a device already, from another sign-in on it.
		s.db.Exec(`UPDATE sessions SET device_id=? WHERE id=?`, fromCookie, u.SessionID)
		u.DeviceID = fromCookie
		return
	case u.DeviceID != "":
		s.deviceSeen(u.DeviceID, seen.Int64, label.String, r)
		s.setDeviceCookie(w, u.DeviceID)
		return
	}
	id, err := s.newDevice(r)
	if err != nil {
		s.log.Printf("a device could not be noted, so nothing learned about it is used for now: %s", s.pathless(err.Error()))
		return
	}
	// A page asks for several things at once when it opens, and each of
	// those requests arrives here without a device. The first to write
	// decides; the others take its device and withdraw their own.
	var took int64
	if res, err := s.db.Exec(`UPDATE sessions SET device_id=? WHERE id=? AND device_id IS NULL`, id, u.SessionID); err == nil {
		took, _ = res.RowsAffected()
	}
	if took != 1 {
		s.db.Exec(`DELETE FROM devices WHERE id=?`, id)
		var first sql.NullString
		if s.db.QueryRow(`SELECT device_id FROM sessions WHERE id=?`, u.SessionID).Scan(&first) != nil || !first.Valid {
			return
		}
		id = first.String
	}
	u.DeviceID = id
	s.setDeviceCookie(w, id)
}

// deviceLabel says what kind of device a browser is, from how it describes
// itself. The answer is always one of a handful of kinds made here; nothing
// is copied out of the description, which on some phones holds the model
// and could one day hold more.
func deviceLabel(userAgent string) string {
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(userAgent, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("iPhone", "iPod"):
		return "iPhone"
	case has("iPad"):
		return "iPad"
	// TVs and the boxes plugged into them say "Linux" or "Android" too, so
	// they are asked about first.
	case has("SmartTV", "SMART-TV", "Smart TV", "Tizen", "Web0S", "webOS", "WebOS", "HbbTV", "NetCast", "BRAVIA", "VIDAA", "Roku",
		"AppleTV", "CrKey", "GoogleTV", "Google TV", "Android TV", "AndroidTV", " AFT", "PlayStation", "Xbox"):
		return "TV browser"
	case has("Android") && has("Mobile"):
		return "Android phone"
	case has("Android"):
		return "Android tablet"
	}
	var system string
	switch {
	case has("Windows"):
		system = "Windows PC"
	case has("CrOS"):
		system = "Chromebook"
	case has("Macintosh", "Mac OS X"):
		system = "Mac"
	case has("Linux", "X11"):
		system = "Linux PC"
	default:
		return "Browser"
	}
	// Every browser lists the ones it descends from, so the newest name
	// is asked about first.
	switch {
	case has("Edg/", "Edge/"):
		return system + ", Edge"
	case has("OPR/", "Opera"):
		return system + ", Opera"
	case has("Firefox/"):
		return system + ", Firefox"
	case has("Chrome/", "Chromium/"):
		return system + ", Chrome"
	case has("Safari/"):
		return system + ", Safari"
	}
	return system
}
