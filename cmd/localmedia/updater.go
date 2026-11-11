package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/server"
	"github.com/Matiaspnyhus/Local-media-project/internal/update"
)

// releaseRepo is where published versions live.
const releaseRepo = "Matiaspnyhus/Local-media-project"

// updater runs the update steps that need the whole program: it knows
// where the program is installed, and it can stop and restart it.
type updater struct {
	app        *app
	db         *sql.DB
	src        *update.Source
	installDir string
	exe        string
	enabled    bool
	why        string // why updates are off, in plain words

	mu          sync.Mutex
	available   *update.Release
	checking    bool
	installing  bool
	lastChecked int64
	problem     string
}

func packageName() string {
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		return "LocalMedia-Windows.zip"
	}
	return fmt.Sprintf("localmedia-%s-%s.zip", runtime.GOOS, runtime.GOARCH)
}

func newUpdater(a *app, d *sql.DB) *updater {
	u := &updater{app: a, db: d}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	switch {
	case a.opts.noUpdate:
		u.why = "Updates are switched off for this run."
	case err != nil:
		u.why = "The program cannot tell where it is installed."
	case !update.Newer("v999999.0.0", version):
		u.why = "This is a test build (" + version + "), which never updates itself."
	default:
		u.enabled, u.exe, u.installDir = true, exe, filepath.Dir(exe)
	}
	u.src = update.GitHub(releaseRepo, packageName())
	// Tests point the updater at a stand-in release page on this machine.
	if feed := os.Getenv("LOCALMEDIA_UPDATE_FEED"); feed != "" {
		if p, err := url.Parse(feed); err == nil {
			u.src = &update.Source{Feed: feed, Asset: packageName(), Hosts: []string{p.Hostname()}}
		}
	}
	return u
}

func (u *updater) forServer() server.Updater { return u }

// Status implements server.Updater.
func (u *updater) Status() server.UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := update.ReadState(u.app.opts.dataDir)
	out := server.UpdateStatus{
		Supported: u.enabled, WhyNot: u.why, Current: version, Checking: u.checking, Installing: u.installing,
		LastChecked: u.lastChecked, Problem: u.problem,
		LastFrom: st.From, LastTo: st.To, LastResult: st.Result, LastProblem: st.Problem, LastAt: st.At,
		CanUndo: u.enabled && st.Result == "ok" && st.To == version && update.HasPrevious(u.installDir),
	}
	if u.available != nil {
		out.Available = u.available.Version
	}
	return out
}

// Check implements server.Updater: it asks the release page once.
func (u *updater) Check(ctx context.Context) server.UpdateStatus {
	if u.enabled {
		u.mu.Lock()
		busy := u.checking || u.installing
		u.checking = !busy
		u.mu.Unlock()
		if !busy {
			rel, err := u.src.Latest(ctx, version)
			u.mu.Lock()
			u.checking, u.lastChecked, u.problem = false, time.Now().Unix(), ""
			switch {
			case errors.Is(err, update.ErrNoRelease):
				u.problem = "No published version was found. (If the project's page is still private, updates cannot be downloaded.)"
			case err != nil:
				u.problem = err.Error()
			default:
				u.available = rel
			}
			u.mu.Unlock()
		}
	}
	return u.Status()
}

// Install implements server.Updater. It downloads, checks and unpacks the
// new version and backs up the database while the program keeps running;
// only then does it stop the program to swap the files.
func (u *updater) Install() error {
	if !u.enabled {
		return errors.New(u.why)
	}
	u.mu.Lock()
	rel := u.available
	if u.installing {
		u.mu.Unlock()
		return errors.New("an update is already being installed")
	}
	if rel == nil {
		u.mu.Unlock()
		return errors.New("there is no newer version to install")
	}
	u.installing = true
	u.mu.Unlock()
	fail := func(err error) error {
		u.mu.Lock()
		u.installing, u.problem = false, err.Error()
		u.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	zipPath, err := u.src.Download(ctx, rel, u.installDir)
	if err != nil {
		return fail(err)
	}
	staged, err := update.Unpack(zipPath, u.installDir, rel.Version)
	os.Remove(zipPath)
	if err != nil {
		return fail(err)
	}
	dataDir := u.app.opts.dataDir
	backup, err := db.Backup(u.db, dataDir, db.SchemaVersion())
	if err != nil {
		return fail(fmt.Errorf("the database could not be backed up, so nothing was changed: %w", err))
	}
	state := update.State{From: version, To: rel.Version, At: time.Now().Unix(), DBBackup: backup, SchemaBefore: db.SchemaVersion(), Result: "pending"}
	if err := update.WriteState(dataDir, state); err != nil {
		return fail(err)
	}
	u.app.next = func() error { return u.handOver(state, staged) }
	u.app.stop()
	return nil
}

// handOver runs once the server and the database are closed. It swaps the
// files, starts the new version and watches it come up; if it does not,
// everything is put back and the earlier version is started again.
func (u *updater) handOver(state update.State, staged string) error {
	dataDir := u.app.opts.dataDir
	undo := func(problem string) error {
		state.Result, state.Problem = "failed", problem
		update.WriteState(dataDir, state)
		_, err := spawn(u.exe, u.childArgs())
		return err
	}
	if err := update.Swap(u.installDir, staged); err != nil {
		return undo("The new files could not be put in place: " + err.Error())
	}
	child, err := spawn(u.exe, u.childArgs())
	if err == nil {
		exited := make(chan struct{})
		go func() { child.Wait(); close(exited) }()
		if err = waitForVersion(u.app.opts.port, state.To, 60*time.Second, exited); err == nil {
			return nil // the new version is up; this one is done
		}
		child.Kill()
		<-exited
	}
	problem := "Version " + state.To + " did not start, so it was removed again: " + err.Error()
	if rerr := update.Rollback(u.installDir); rerr != nil {
		return fmt.Errorf("%s. Putting the earlier version back also failed: %w", problem, rerr)
	}
	if rerr := update.RestoreDatabase(state.DBBackup, filepath.Join(dataDir, db.FileName)); rerr != nil {
		return fmt.Errorf("%s. Restoring the database backup also failed: %w", problem, rerr)
	}
	return undo(problem)
}

// Undo implements server.Updater: go back to the version before the last
// update. The database backup is only restored if that update changed the
// database's layout, since the earlier version could not read it otherwise.
func (u *updater) Undo() error {
	st := u.Status()
	if !st.CanUndo {
		return errors.New("there is no earlier version to go back to")
	}
	dataDir := u.app.opts.dataDir
	state := update.ReadState(dataDir)
	u.app.next = func() error {
		if err := update.Rollback(u.installDir); err != nil {
			return err
		}
		if state.SchemaBefore != db.SchemaVersion() {
			if err := update.RestoreDatabase(state.DBBackup, filepath.Join(dataDir, db.FileName)); err != nil {
				return err
			}
		}
		state.Result = "undone"
		update.WriteState(dataDir, state)
		_, err := spawn(u.exe, u.childArgs())
		return err
	}
	u.app.stop()
	return nil
}

func (u *updater) childArgs() []string {
	var args []string
	for _, a := range os.Args[1:] {
		if a != "--after-update" && a != "-after-update" {
			args = append(args, a)
		}
	}
	return append(args, "--after-update")
}

func waitForVersion(port int, want string, limit time.Duration, exited <-chan struct{}) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(limit)
	last := "it never answered"
	for time.Now().Before(deadline) {
		res, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err == nil {
			var h struct {
				Version string `json:"version"`
			}
			json.NewDecoder(res.Body).Decode(&h)
			res.Body.Close()
			if h.Version == want {
				return nil
			}
			last = "version " + h.Version + " answered instead"
		}
		select {
		case <-exited:
			return errors.New("it stopped straight away")
		case <-time.After(300 * time.Millisecond):
		}
	}
	return errors.New(last)
}

// started runs once the server is listening.
func (u *updater) started() {
	dataDir := u.app.opts.dataDir
	if st := update.ReadState(dataDir); st.Result == "pending" && st.To == version {
		st.Result = "ok"
		update.WriteState(dataDir, st)
	}
	if !u.enabled {
		return
	}
	if st := update.ReadState(dataDir); st.Result != "pending" {
		update.Tidy(u.installDir)
	}
	go func() {
		wait := 20 * time.Second
		for {
			select {
			case <-u.app.quit:
				return
			case <-time.After(wait):
			}
			wait = 12 * time.Hour
			st := u.Check(context.Background())
			last := update.ReadState(dataDir)
			// Install by itself only when the owner allows it, nobody is
			// watching, and this exact version has not already failed.
			if st.Available != "" && u.app.srv.AutoUpdate() && u.app.srv.IdleFor() > 15*time.Minute &&
				!(last.Result == "failed" && last.To == st.Available) {
				u.Install()
			}
		}
	}()
}
