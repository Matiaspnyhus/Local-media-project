//go:build !windows

package server

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// openCommand is the program that opens a file with whatever the desktop
// uses for that kind of file: "open" on a Mac, "xdg-open" elsewhere.
var openCommand = func() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}()

// openWithSystem asks the desktop to open one file. The path is handed
// over as a single argument, with no shell in between to read anything
// into it.
func openWithSystem(path string) error {
	if !filepath.IsAbs(path) {
		// A full path can never be taken for an option.
		return errors.New("not a full path")
	}
	cmd := exec.Command(openCommand, path)
	// Its own session, so that a player started from here is not closed
	// along with Local Media.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Most desktops answer at once, and a failure is reported. On some,
	// the command only returns when the player is closed (see openWait).
	select {
	case err := <-done:
		return err
	case <-time.After(openWait):
		return nil
	}
}
