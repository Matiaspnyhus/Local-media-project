// Command localmedia is the whole program: the server and, inside it, the
// web app.
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
	"github.com/Matiaspnyhus/Local-media-project/internal/server"
	"github.com/Matiaspnyhus/Local-media-project/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Local Media could not start:", err)
		if runtime.GOOS == "windows" {
			fmt.Fprintln(os.Stderr, "\nPress Enter to close this window.")
			fmt.Scanln()
		}
		os.Exit(1)
	}
}

func run() error {
	var (
		dataDir   = flag.String("data", defaultDataDir(), "folder for the database and logs (never inside a media folder)")
		port      = flag.Int("port", 8177, "port to listen on")
		noBrowser = flag.Bool("no-browser", false, "do not open the browser at start")
		showVer   = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return nil
	}

	d, err := db.Open(*dataDir)
	if err != nil {
		return err
	}
	defer d.Close()

	srv, err := server.New(server.Config{DB: d, DataDir: *dataDir, Version: version, Port: *port, Assets: web.Assets(), LogOut: os.Stdout})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		return fmt.Errorf("port %d is in use; is Local Media already running? (%w)", *port, err)
	}
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}

	localURL := fmt.Sprintf("http://localhost:%d", *port)
	fmt.Printf("\nLocal Media %s is running.\n\n  On this PC:        %s\n", version, localURL)
	for _, u := range srv.LANURLs() {
		fmt.Printf("  On other devices:  %s\n", u)
	}
	fmt.Printf("\nData folder: %s\nKeep this window open. Close it to stop Local Media.\n\n", *dataDir)

	go func() {
		srv.ScanAll()
		for range time.Tick(15 * time.Minute) {
			srv.ScanAll()
		}
	}()
	if !*noBrowser {
		go openBrowser(localURL)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(ctx)
	}
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
