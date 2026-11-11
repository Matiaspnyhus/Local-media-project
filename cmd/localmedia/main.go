// Command localmedia is the whole program: the server and, inside it, the
// web app. On Windows it also shows an icon beside the clock.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
	"github.com/Matiaspnyhus/Local-media-project/internal/server"
	"github.com/Matiaspnyhus/Local-media-project/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// failStart is set in a test build to make a version that cannot start,
// which is how the "a broken update is undone" test gets its broken update.
var failStart = ""

type options struct {
	dataDir   string
	port      int
	noBrowser bool
	noTray    bool
	noUpdate  bool
	after     bool // started by the previous version as the last step of an update
}

func main() {
	var o options
	flag.StringVar(&o.dataDir, "data", defaultDataDir(), "folder for the database and logs (never inside a media folder)")
	flag.IntVar(&o.port, "port", 8177, "port to listen on")
	flag.BoolVar(&o.noBrowser, "no-browser", false, "do not open the browser at start")
	flag.BoolVar(&o.noTray, "no-tray", false, "do not show the icon beside the clock")
	flag.BoolVar(&o.noUpdate, "no-update", false, "never look for or install new versions")
	flag.BoolVar(&o.after, "after-update", false, "internal: this start is the last step of an update")
	showVer := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	if failStart != "" {
		fmt.Fprintln(os.Stderr, "this build is deliberately broken")
		os.Exit(3)
	}

	app := &app{opts: o, quit: make(chan struct{}), ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- app.run() }()

	// The icon beside the clock needs the program's first thread; where
	// there is no icon this simply waits for the server to finish.
	runTray(app, done)

	if err := app.err; err != nil {
		reportFatal("Local Media could not start:\n\n" + err.Error())
		os.Exit(1)
	}
	if app.next != nil {
		// An update or an undo: the files are in place, hand over.
		if err := app.next(); err != nil {
			reportFatal("Local Media could not restart after an update:\n\n" + err.Error())
			os.Exit(1)
		}
	}
}

// app is one run of the program.
type app struct {
	opts  options
	srv   *server.Server
	upd   *updater
	quit  chan struct{} // closed to stop the server
	ready chan struct{} // closed once the server is listening
	err   error
	// next, when set, runs after everything is shut down: it swaps the
	// program's files and starts the other version.
	next func() error
}

func (a *app) stop() {
	select {
	case <-a.quit:
	default:
		close(a.quit)
	}
}

func (a *app) localURL() string { return fmt.Sprintf("http://localhost:%d", a.opts.port) }

func (a *app) run() (err error) {
	defer func() { a.err = err }()
	o := a.opts
	d, err := db.Open(o.dataDir)
	if err != nil {
		return err
	}
	defer d.Close()

	a.upd = newUpdater(a, d)
	ffprobe := probe.Find()
	srv, err := server.New(server.Config{
		DB: d, DataDir: o.dataDir, Version: version, Port: o.port, Assets: web.Assets(), LogOut: os.Stdout, FFprobe: ffprobe,
		Update: a.upd.forServer(),
	})
	if err != nil {
		return err
	}
	defer srv.Close()
	a.srv = srv

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", o.port))
	if err != nil {
		return fmt.Errorf("port %d is in use. Is Local Media already running? Look for its icon beside the clock. (%w)", o.port, err)
	}
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}

	fmt.Printf("\nLocal Media %s is running.\n\n  On this PC:        %s\n", version, a.localURL())
	for _, u := range srv.LANURLs() {
		fmt.Printf("  On other devices:  %s\n", u)
	}
	if ffprobe == "" {
		fmt.Println("\nNote: ffprobe was not found, so files cannot be inspected. The app will not\nknow in advance which files your browser can play.")
	}
	fmt.Printf("\nData folder: %s\n\n", o.dataDir)

	go func() {
		srv.ScanAll()
		for range time.Tick(15 * time.Minute) {
			srv.ScanAll()
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	close(a.ready)
	// A note of which process is running, removed again on the way out.
	pidFile := filepath.Join(o.dataDir, "localmedia.pid")
	os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0o600)
	defer os.Remove(pidFile)
	a.upd.started()
	if !o.noBrowser && !o.after {
		go openBrowser(a.localURL())
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stop:
	case <-a.quit:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
	return nil
}

// defaultDataDir is a per-user folder on local disk. Its name, localmedia,
// is fixed and never changes, whatever the app ends up being called.
func defaultDataDir() string {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "localmedia")
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "localmedia")
	}
	return "localmedia-data"
}

func openBrowser(url string) {
	time.Sleep(400 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
