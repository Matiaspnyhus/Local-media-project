package server

import (
	"context"
	"net/http"
	"time"
)

// UpdateStatus is what the settings screen shows about updates.
type UpdateStatus struct {
	Supported   bool   `json:"supported"`
	WhyNot      string `json:"whyNot,omitempty"`
	Current     string `json:"current"`
	Available   string `json:"available,omitempty"`
	Checking    bool   `json:"checking"`
	Installing  bool   `json:"installing"`
	LastChecked int64  `json:"lastChecked,omitempty"`
	Problem     string `json:"problem,omitempty"`
	Auto        bool   `json:"auto"`
	CanUndo     bool   `json:"canUndo"`
	// The last update that was tried, and how it went.
	LastFrom    string `json:"lastFrom,omitempty"`
	LastTo      string `json:"lastTo,omitempty"`
	LastResult  string `json:"lastResult,omitempty"`
	LastProblem string `json:"lastProblem,omitempty"`
	LastAt      int64  `json:"lastAt,omitempty"`
}

// Updater is the part of the program that can replace it with a newer
// version. The server only passes the owner's wishes on.
type Updater interface {
	Status() UpdateStatus
	Check(ctx context.Context) UpdateStatus
	Install() error
	Undo() error
}

// AutoUpdate reports whether the owner lets updates install by themselves.
func (s *Server) AutoUpdate() bool { return s.setting("auto_update") != "off" }

// IdleFor reports how long it has been since anything was streamed, so an
// update never interrupts someone who is watching.
func (s *Server) IdleFor() time.Duration {
	last := s.lastStream.Load()
	if last == 0 {
		return 24 * time.Hour
	}
	return time.Since(time.Unix(last, 0))
}

func (s *Server) updateStatus(st UpdateStatus) UpdateStatus {
	st.Auto = s.AutoUpdate()
	if st.Current == "" {
		st.Current = s.version
	}
	return st
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeJSON(w, http.StatusOK, s.updateStatus(UpdateStatus{WhyNot: "This copy cannot update itself."}))
		return
	}
	writeJSON(w, http.StatusOK, s.updateStatus(s.update.Status()))
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeErr(w, http.StatusBadRequest, "This copy cannot update itself.")
		return
	}
	writeJSON(w, http.StatusOK, s.updateStatus(s.update.Check(r.Context())))
}

func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeErr(w, http.StatusBadRequest, "This copy cannot update itself.")
		return
	}
	s.log.Printf("the owner asked to install an update")
	// Answer first: installing ends with this program stopping.
	go func() {
		if err := s.update.Install(); err != nil {
			s.log.Printf("update not installed: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) handleUpdateUndo(w http.ResponseWriter, r *http.Request) {
	if s.update == nil {
		writeErr(w, http.StatusBadRequest, "This copy cannot update itself.")
		return
	}
	if !s.update.Status().CanUndo {
		writeErr(w, http.StatusBadRequest, "There is no earlier version to go back to.")
		return
	}
	s.log.Printf("the owner asked to go back to the earlier version")
	go func() {
		if err := s.update.Undo(); err != nil {
			s.log.Printf("going back failed: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) handleSetAutoUpdate(w http.ResponseWriter, r *http.Request) {
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
	if err := s.setSetting("auto_update", v); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the setting.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
