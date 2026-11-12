//go:build windows

package server

import (
	"path/filepath"
	"testing"
	"time"
)

// The real call to Windows, with a file that is not there: nothing can
// open, so no player starts on the machine running the test, and the
// launcher must say that it failed.
func TestWindowsReportsAFileItCannotOpen(t *testing.T) {
	limit := openWait
	openWait = 2 * time.Minute // a slow machine must not be taken for a silent one
	t.Cleanup(func() { openWait = limit })
	if err := openWithSystem(filepath.Join(t.TempDir(), "not there.mkv")); err == nil {
		t.Fatal("Windows could not have opened a file that does not exist, and the launcher said the film was opened")
	}
}

func TestOnlyAFullPathIsHandedToWindows(t *testing.T) {
	for _, path := range []string{"film.mkv", `.\film.mkv`, `..\film.mkv`, "calc.exe", "http://example.com/film.mkv", ""} {
		if err := openWithSystem(path); err == nil {
			t.Errorf("%q was handed over", path)
		}
	}
}
