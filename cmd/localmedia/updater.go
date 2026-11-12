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

// How the updater spaces what it does by itself. (Variables, so that tests
// need not wait as long.)
var (
	// firstCheck is how long after the start the release page is first
	// asked, and checkEvery how often after that.
	firstCheck = 20 * time.Second
	checkEvery = 12 * time.Hour
	// retryEvery is how often an update that is waiting for a quiet moment
	// looks whether this is one. Looking costs next to nothing: the release
	// page is not asked again, and nothing is downloaded twice.
	retryEvery = time.Minute
)

// executable says where the program's own file is. Tests point it at a
// folder of their own.
var executable = os.Executable

// backUp copies the database before an update. Tests put a wait in front
// of it, so that a film can be started while the copy is being made.
var backUp = db.Backup

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

	// again wakes the updater's own loop when an install the owner asked
	// for has been put off, so that it looks again within the minute.
	again chan struct{}

	mu         sync.Mutex
	available  *update.Release
	checking   bool
	installing bool
	// What the owner has said. byOwner: the owner pressed "Install it now".
	// It holds until the version is in or has failed, also across tries
	// that were put off, and whether or not versions may install by
	// themselves. whoever: the owner was told who is watching and said to
	// install all the same, so the install waits for nobody. besides: the
	// sign-in the owner pressed the button on. A film on that very device
	// is the owner's own to interrupt, for the try the press began.
	byOwner bool
	whoever bool
	besides string
	// waiting: an install was put off because someone was watching. It
	// goes in by itself at the first look that finds nobody.
	waiting     bool
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
	u := &updater{app: a, db: d, again: make(chan struct{}, 1)}
	exe, err := executable()
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
		out.Waiting = u.waiting
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

// Install implements server.Updater: the owner asked for the update. The
// server has already told the owner who was watching at that moment (apart
// from the device the owner asked on, the sign-in from), and whoever says
// whether the owner then said to install all the same.
//
// Without that word the install still looks at the moment of the restart:
// someone who began to watch after the owner pressed has not been mentioned
// to the owner. It is then put off like one that started by itself, the
// settings page says who is watching, and the owner can say "anyway".
func (u *updater) Install(from string, whoever bool) error {
	err := u.install(&word{from: from, whoever: whoever})
	if errors.Is(err, errSomeoneWatching) {
		select {
		case u.again <- struct{}{}:
		default: // the loop has been told already
		}
	}
	return err
}

// word is what the owner said when asking for an install.
type word struct {
	from    string // the sign-in the owner asked on
	whoever bool   // install whoever is watching
}

// hear notes the owner's word. The caller holds the lock.
func (u *updater) hear(w *word) {
	// "Whoever is watching", once said, stands for the install it was said
	// about, also when the button is pressed again without it.
	u.byOwner, u.besides, u.whoever = true, w.from, u.whoever || w.whoever
}

// forget drops the owner's word. The caller holds the lock.
func (u *updater) forget() { u.byOwner, u.besides, u.whoever = false, "", false }

// What an install answers when it has stopped short of restarting the
// program without anything having gone wrong.
var (
	errSomeoneWatching = errors.New("someone is watching, so the update waits")
	errSwitchedOff     = errors.New("installing by itself has been switched off")
)

// install downloads, checks and unpacks the new version and backs up the
// database while the program keeps running; only then does it stop the
// program to swap the files.
//
// An install never stops the program under someone who is watching, unless
// the owner has been told who that is and said to install all the same
// (whoever). Who is watching is asked three times: before anything is
// downloaded, again before the database is backed up, and a last time at
// the moment of the restart, which is the one that counts: a download can
// take minutes, and a film can be started in any of them. When someone is,
// nothing is thrown away. The unpacked version stays where it is, the
// updater's loop looks again every minute, and the first look that finds
// nobody watching restarts. (For an install that started by itself the
// owner may also have switched installing by itself off in those minutes;
// then the version stays unpacked, for when the owner asks for it.)
//
// owner is the owner's word if this try comes from the owner's own press,
// and nil when the updater's loop tries by itself.
func (u *updater) install(owner *word) error {
	if !u.enabled {
		return errors.New(u.why)
	}
	u.mu.Lock()
	rel := u.available
	if u.installing {
		if owner != nil {
			// One is on its way already. What the owner has now said goes
			// for that one.
			u.hear(owner)
			u.mu.Unlock()
			return nil
		}
		u.mu.Unlock()
		return errors.New("an update is already being installed")
	}
	if rel == nil {
		u.mu.Unlock()
		return errors.New("there is no newer version to install")
	}
	u.installing = true
	if owner != nil {
		u.hear(owner)
	}
	u.mu.Unlock()
	fail := func(err error) error {
		u.mu.Lock()
		// It is no longer a quiet moment that the version waits for. What
		// the owner asked for has ended, with a problem the owner is shown.
		u.installing, u.waiting, u.problem = false, false, err.Error()
		u.forget()
		u.mu.Unlock()
		return err
	}
	// held says why this install may not go on towards the restart just
	// now, and notes that it has stopped; nil means it may go on. The lock
	// is kept while the server is asked, so that the owner pressing
	// "Install it now" at that very moment is either seen here or finds
	// this install over and starts one of their own.
	held := func() error {
		u.mu.Lock()
		defer u.mu.Unlock()
		switch {
		case u.whoever:
			return nil
		case !u.byOwner && !u.app.srv.AutoUpdate():
			u.installing, u.waiting = false, false
			return errSwitchedOff
		}
		for _, w := range u.app.srv.Watching() {
			if w.SignIn != u.besides {
				// From here on it waits for a moment when nobody at all is
				// watching, which is what the settings page says of it.
				u.installing, u.waiting, u.besides = false, true, ""
				return errSomeoneWatching
			}
		}
		return nil
	}
	if err := held(); err != nil {
		return err
	}
	staged, err := u.prepare(rel)
	if err != nil {
		return fail(err)
	}
	if err := held(); err != nil {
		return err
	}
	// The backup is made last, so that it holds everything up to the
	// restart, however long the unpacked version had to wait.
	dataDir := u.app.opts.dataDir
	backup, err := backUp(u.db, dataDir, db.SchemaVersion())
	if err != nil {
		return fail(fmt.Errorf("the database could not be backed up, so nothing was changed: %w", err))
	}
	if err := held(); err != nil {
		os.Remove(backup) // the next try makes a fresh one
		return err
	}
	state := update.State{From: version, To: rel.Version, At: time.Now().Unix(), DBBackup: backup, SchemaBefore: db.SchemaVersion(), Result: "pending"}
	if err := update.WriteState(dataDir, state); err != nil {
		return fail(err)
	}
	u.mu.Lock()
	u.waiting = false
	u.mu.Unlock()
	u.app.next = func() error { return u.handOver(state, staged) }
	u.app.stop()
	return nil
}

// prepare returns the folder that holds the new version, checked and
// unpacked: the one an earlier try left ready, if it is untouched and came
// from the package the release holds now, and otherwise a fresh download.
func (u *updater) prepare(rel *update.Release) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	// The release is asked which package it holds at this moment, not what
	// it held when the version was made ready, which can be days ago: a
	// package can be replaced under the same version number. If the release
	// cannot be asked just now, nothing is installed just now.
	sum, err := u.src.Sum(ctx, rel)
	if err != nil {
		return "", err
	}
	if staged, ok := update.Ready(u.installDir, rel.Version, sum); ok {
		return staged, nil
	}
	zipPath, err := u.src.Download(ctx, rel, u.installDir)
	if err != nil {
		return "", err
	}
	staged, err := update.Unpack(zipPath, u.installDir, rel.Version)
	os.Remove(zipPath)
	if err != nil {
		return "", err
	}
	// Without the note the folder still serves this try; it only cannot be
	// used by a later one. (Download checked the package against the list
	// as it read it. Should the list have changed since it was asked above,
	// the note names a package the release no longer holds, and a later try
	// fetches afresh.)
	update.MarkReady(staged, rel.Version, sum)
	return staged, nil
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
		update.Tidy(u.installDir, version)
	}
	go func() {
		wait := firstCheck
		var asked time.Time // when the release page was last asked
		for {
			select {
			case <-u.app.quit:
				return
			case <-time.After(wait):
			case <-u.again:
			}
			wait = u.round(&asked)
		}
	}()
}

// round is one turn of the updater's own loop: it asks the release page
// if that is due, installs a newer version if it may, and says how long
// to wait before the next turn.
func (u *updater) round(asked *time.Time) time.Duration {
	// By the PC's clock (Round drops Go's second clock, which stands still
	// while the PC sleeps).
	now := time.Now().Round(0)
	if asked.IsZero() || now.Sub(*asked) >= checkEvery || now.Before(*asked) {
		*asked = now
		u.Check(context.Background())
	}
	next := max(checkEvery-now.Sub(*asked), time.Second)
	st := u.Status()
	last := update.ReadState(u.app.opts.dataDir)
	// Install by itself only when the owner allows it and this exact
	// version has not already failed; or when the owner has asked for it
	// and it was put off. (Whether anyone is watching is for install to
	// find out, at the moment it matters.)
	u.mu.Lock()
	if st.Available == "" {
		u.forget() // there is nothing left that the owner asked for
	}
	byOwner := u.byOwner
	u.mu.Unlock()
	if st.Available == "" || (!byOwner && (!u.app.srv.AutoUpdate() || (last.Result == "failed" && last.To == st.Available))) {
		u.mu.Lock()
		u.waiting = false
		u.mu.Unlock()
		return next
	}
	if err := u.install(nil); errors.Is(err, errSomeoneWatching) {
		return min(retryEvery, next)
	}
	return next
}
