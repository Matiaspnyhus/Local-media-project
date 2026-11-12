//go:build windows

package server

import (
	"errors"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// openWithSystem asks Windows to open one file the way a double-click
// would: with the program set up for that kind of file. The path is handed
// over as it is, as the thing to open; there is no command line for
// anything to be read into.
func openWithSystem(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("not a full path")
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		// Windows asks for this call to be made on a thread that has been
		// prepared for it (some players are started through COM), so the
		// work stays on one thread from the preparing to the tidying up.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		const alreadyPrepared = syscall.Errno(1) // S_FALSE: still to be undone
		if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil || err == alreadyPrepared {
			defer windows.CoUninitialize()
		}
		done <- windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
	}()
	// Windows normally answers at once. A drive that has to wake up first
	// can take longer; the page is not kept waiting for it (see openWait).
	select {
	case err := <-done:
		return err
	case <-time.After(openWait):
		return nil
	}
}
