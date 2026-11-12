//go:build !windows

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The tests below run the real launcher, with this test program standing
// in for the desktop's "open" command, so no video player is started. When
// it is started as that stand-in it writes down what it was given and
// leaves the way the test asked it to.
const (
	standInRecord = "LM_TEST_OPENER_RECORD" // where to write the arguments
	standInExit   = "LM_TEST_OPENER_EXIT"   // the exit status to leave with
	standInStay   = "LM_TEST_OPENER_STAY"   // stay until this file exists, like a player that is being watched
)

func init() {
	record := os.Getenv(standInRecord)
	if record == "" {
		return // an ordinary run of the tests
	}
	args, _ := json.Marshal(os.Args[1:])
	os.WriteFile(record+".part", args, 0o600)
	os.Rename(record+".part", record) // all of it or none: the test may be looking
	if until := os.Getenv(standInStay); until != "" {
		// Leaves when told to, or when the test's folder has gone, and in
		// any case after ten minutes.
		for i := 0; i < 30000; i++ {
			if _, err := os.Stat(until); err == nil {
				break
			}
			if _, err := os.Stat(record); err != nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	code, _ := strconv.Atoi(os.Getenv(standInExit))
	os.Exit(code)
}

// standIn makes the launcher start this test program in place of the
// desktop's command, and returns the file its arguments are written to.
func standIn(t *testing.T, wait time.Duration) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command, limit := openCommand, openWait
	openCommand, openWait = self, wait
	t.Cleanup(func() { openCommand, openWait = command, limit })
	record := filepath.Join(t.TempDir(), "given.json")
	t.Setenv(standInRecord, record)
	return record
}

func given(t *testing.T, record string) []string {
	t.Helper()
	var args []string
	b, err := os.ReadFile(record)
	if err != nil || json.Unmarshal(b, &args) != nil {
		t.Fatalf("the stand-in for the desktop's command left no note: %v %q", err, b)
	}
	return args
}

func TestTheDesktopIsGivenTheFileAndNothingElse(t *testing.T) {
	// A name with everything a shell would read something into. There is
	// no shell, so it must arrive as it is, as one argument.
	path := filepath.Join(t.TempDir(), `It's a "film" & $(more) `+"`then`"+`; -rf * --help %s \ <end>.mkv`)
	record := standIn(t, 10*time.Minute) // the stand-in answers at once; a slow machine must not be taken for a silent one
	if err := openWithSystem(path); err != nil {
		t.Fatalf("the launcher reports: %v", err)
	}
	if args := given(t, record); len(args) != 1 || args[0] != path {
		t.Fatalf("the desktop's command was given %q, want only %q", args, path)
	}
}

func TestADesktopThatCannotOpenTheFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "film.mkv")
	record := standIn(t, 10*time.Minute)
	t.Setenv(standInExit, "3") // xdg-open's "a required tool could not be found"
	if err := openWithSystem(path); err == nil {
		t.Fatal("the command failed, and the launcher said the film was opened")
	}
	given(t, record) // it did run

	// No such command at all, as on a PC with no desktop.
	openCommand = filepath.Join(t.TempDir(), "no-such-command")
	if err := openWithSystem(path); err == nil {
		t.Fatal("there was no command to run, and the launcher said the film was opened")
	}
}

func TestOnlyAFullPathIsHandedToTheDesktop(t *testing.T) {
	record := standIn(t, 10*time.Minute)
	for _, path := range []string{"film.mkv", "-film.mkv", "--help", "./film.mkv", "../film.mkv", ""} {
		if err := openWithSystem(path); err == nil {
			t.Errorf("%q was handed over", path)
		}
	}
	if _, err := os.Stat(record); err == nil {
		t.Fatalf("the desktop's command was run: %q", given(t, record))
	}
}

// On some desktops the command only returns when the player is closed. The
// page must not wait for the end of the film to hear that it started.
func TestAPlayerThatStaysOpenCountsAsOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "film.mkv")
	record := standIn(t, 50*time.Millisecond)
	closed := filepath.Join(t.TempDir(), "closed")
	t.Setenv(standInStay, closed)
	t.Setenv(standInExit, "1") // what it leaves with in the end no longer matters
	t.Cleanup(func() { os.WriteFile(closed, nil, 0o600) })
	if err := openWithSystem(path); err != nil {
		t.Fatalf("the launcher reports: %v", err)
	}
	// It was started, and is still there: it cannot leave before the test
	// says the player was closed.
	for i := 0; ; i++ {
		if _, err := os.Stat(record); err == nil {
			break
		}
		if i > 6000 {
			t.Fatal("the stand-in for the desktop's command never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if args := given(t, record); len(args) != 1 || args[0] != path {
		t.Fatalf("given %q", args)
	}
}
