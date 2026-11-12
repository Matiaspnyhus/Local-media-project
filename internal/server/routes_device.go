package server

import (
	"net/http"
	"runtime"

	"github.com/go-chi/chi/v5"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
)

// These routes show what was learned about devices (lessons.go) and about
// this PC's speed (speeds.go), and let it be forgotten: a viewer for the
// device in their hand, the owner for every device and for the PC.

// deviceView is a device as the page shows it.
type deviceView struct {
	// ID names the device in the owner's routes. It is not sent to the
	// device itself, which has no use for it.
	ID string `json:"id,omitempty"`
	// Label is the kind of device: "iPhone", "Windows PC, Chrome".
	Label      string `json:"label"`
	LastSeenAt string `json:"lastSeenAt,omitempty"`
	// ThisDevice marks, in the owner's list, the device that is asking.
	ThisDevice bool `json:"thisDevice,omitempty"`
	// HDRWrong says the viewer reported that HDR colours look wrong on
	// this device, and the report still holds.
	HDRWrong bool         `json:"hdrWrong"`
	Lessons  []lessonView `json:"lessons"`
}

// canConvert says this PC can convert pictures, which is what most lessons
// lead to (see lessonText).
func (s *Server) canConvert() bool {
	l := s.limits(nil)
	return l.Convert && !l.NoConverter
}

// handleDevice says what kind of device is asking and what was learned
// about it.
func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	out := deviceView{Label: "Browser", Lessons: []lessonView{}}
	if u.DeviceID != "" {
		s.db.QueryRow(`SELECT label FROM devices WHERE id=?`, u.DeviceID).Scan(&out.Label)
		held, err := s.lessonsOf(u.DeviceID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not read what was learned about this device.")
			return
		}
		out.Lessons = viewsOf(held, s.canConvert())
		for _, l := range held {
			out.HDRWrong = out.HDRWrong || l.Kind == lessonHDR
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleForgetLesson forgets one lesson about the device that is asking.
// A lesson about another device is not found, whoever asks.
func (s *Server) handleForgetLesson(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id := chi.URLParam(r, "id")
	if u.DeviceID == "" || !ids.Valid(id) || !s.forgetLesson(u.DeviceID, id) {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	s.log.Printf("a viewer had something that was learned about their device forgotten")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleHDRWrong takes the viewer's word that HDR colours look wrong on
// this device (HDR films are then converted to normal colour for it), or
// that they look right after all.
func (s *Server) handleHDRWrong(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var in struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if u.DeviceID == "" {
		writeErr(w, http.StatusInternalServerError, "Could not save.")
		return
	}
	out := map[string]any{"ok": true, "on": in.On, "lesson": ""}
	if in.On {
		id, err := s.learn(u.DeviceID, "", learned{kind: lessonHDR, life: hdrLife})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not save.")
			return
		}
		out["lesson"] = id
	} else if _, err := s.db.Exec(`DELETE FROM lessons WHERE device_id=? AND kind=?`, u.DeviceID, lessonHDR); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save.")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDevices lists, for the owner, every device with what was learned
// about it, and what was learned about this PC's speed.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	failed := func() { writeErr(w, http.StatusInternalServerError, "Could not read the devices.") }
	held, err := s.lessonsOf("")
	if err != nil {
		failed()
		return
	}
	byDevice := map[string][]lesson{}
	for _, l := range held {
		byDevice[l.Device] = append(byDevice[l.Device], l)
	}
	rows, err := s.db.Query(`SELECT id, label, last_seen_at FROM devices ORDER BY last_seen_at DESC, id LIMIT 500`)
	if err != nil {
		failed()
		return
	}
	defer rows.Close()
	can := s.canConvert()
	devices := []deviceView{}
	for rows.Next() {
		var d deviceView
		var seen int64
		if rows.Scan(&d.ID, &d.Label, &seen) != nil {
			failed()
			return
		}
		d.LastSeenAt, d.ThisDevice = stamp(seen), d.ID == u.DeviceID
		d.Lessons = viewsOf(byDevice[d.ID], can)
		for _, l := range byDevice[d.ID] {
			d.HDRWrong = d.HDRWrong || l.Kind == lessonHDR
		}
		devices = append(devices, d)
	}
	if rows.Err() != nil {
		failed()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices, "pc": map[string]any{"lessons": s.speedLessons()}})
}

// handleForgetDeviceLesson lets the owner forget one lesson about any
// device, or (with "pc" in the device's place) one speed this PC was seen
// to convert at.
func (s *Server) handleForgetDeviceLesson(w http.ResponseWriter, r *http.Request) {
	device, id := chi.URLParam(r, "id"), chi.URLParam(r, "lesson")
	var ok bool
	if device == "pc" {
		ok = len(id) <= 80 && s.forgetSpeed(id)
	} else {
		ok = ids.Valid(device) && ids.Valid(id) && s.forgetLesson(device, id)
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "Not found.")
		return
	}
	s.log.Printf("the owner had something that was learned forgotten")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// learnedReport says, for the support bundle, what was learned about each
// device and about this PC, and how often films ran into trouble. Devices
// and lessons appear as kinds only: no ids, no titles, no files, and
// nothing a browser said about itself beyond the kind made of it.
func (s *Server) learnedReport() map[string]any {
	now := s.now().Unix()
	daysFrom := func(a, b int64) int { return int((b - a) / int64(day.Seconds())) }
	type lessonLine struct {
		Kind  string `json:"kind"`
		About string `json:"about"`
		// HoldsFor is "one film" until a second film showed the same.
		HoldsFor      string `json:"holdsFor"`
		TimesReported int    `json:"timesReported"`
		DaysLeft      int    `json:"daysLeft"`
		// RanOut marks a lesson that no longer holds and is only kept so
		// that learned again it lasts longer.
		RanOut bool `json:"ranOut"`
	}
	type deviceLine struct {
		Kind            string       `json:"kind"`
		LastSeenDaysAgo int          `json:"lastSeenDaysAgo"`
		Lessons         []lessonLine `json:"lessons"`
	}
	byDevice := map[string][]lessonLine{}
	if rows, err := s.db.Query(`SELECT device_id, kind, value, file_id IS NOT NULL, hits, expires_at FROM lessons ORDER BY learned_at, rowid`); err == nil {
		for rows.Next() {
			var device string
			var l lessonLine
			var one bool
			var expires int64
			if rows.Scan(&device, &l.Kind, &l.About, &one, &l.TimesReported, &expires) != nil {
				continue
			}
			l.HoldsFor = "every film"
			if one {
				l.HoldsFor = "one film"
			}
			l.DaysLeft, l.RanOut = max(daysFrom(now, expires), 0), expires <= now
			byDevice[device] = append(byDevice[device], l)
		}
		rows.Close()
	}
	devices := []deviceLine{}
	if rows, err := s.db.Query(`SELECT id, label, last_seen_at FROM devices ORDER BY last_seen_at DESC, id LIMIT 500`); err == nil {
		for rows.Next() {
			var id string
			var d deviceLine
			var seen int64
			if rows.Scan(&id, &d.Kind, &seen) != nil {
				continue
			}
			d.LastSeenDaysAgo = max(daysFrom(seen, now), 0)
			if d.Lessons = byDevice[id]; d.Lessons == nil {
				d.Lessons = []lessonLine{}
			}
			devices = append(devices, d)
		}
		rows.Close()
	}
	pc := map[string]any{"converterVersion": s.converterVersion(), "processors": runtime.NumCPU(), "speeds": s.speedReport()}
	return map[string]any{"devices": devices, "thisPC": pc, "trouble": s.troubleReport()}
}
