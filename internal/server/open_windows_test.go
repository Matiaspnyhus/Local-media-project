//go:build windows

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
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

// The test below runs the real launcher on files that exist, with this
// test program standing in for a video player, so no player is started.
// Windows is told, for this user only and for the length of the test, that
// a made-up kind of file is opened by this program. Started that way, the
// program writes down what it was given and leaves.
const standInWord = "lm-test-stand-in-player" // the first thing on its command line when it stands in

func init() {
	if len(os.Args) < 3 || os.Args[1] != standInWord {
		return // an ordinary run of the tests
	}
	record := os.Args[2]
	args, _ := json.Marshal(os.Args[3:])
	os.WriteFile(record+".part", args, 0o600)
	os.Rename(record+".part", record) // all of it or none: the test may be looking
	os.Exit(0)
}

// standInPlayer registers the made-up kind of file and returns its ending
// (".lmtest…") and the file the stand-in writes to. The registration is
// taken out again when the test ends.
func standInPlayer(t *testing.T) (ending, record string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record = filepath.Join(t.TempDir(), "given.json")
	if strings.ContainsAny(self+record, `"%`) {
		t.Skipf("this test cannot be set up from a folder with a quote or a percent sign in its name: %s", self)
	}
	kind := "lmtest" + ids.New()[:12]
	ending = "." + kind
	const classes = `Software\Classes\`
	class := "LocalMediaTest." + kind
	set := func(key, value string) {
		t.Helper()
		k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
		if err != nil {
			t.Fatalf("could not tell Windows about the test's kind of file (%s): %v", key, err)
		}
		defer k.Close()
		if err := k.SetStringValue("", value); err != nil {
			t.Fatalf("could not tell Windows about the test's kind of file (%s): %v", key, err)
		}
	}
	t.Cleanup(func() {
		// From the innermost key outwards: a key with keys in it is not
		// removed.
		for _, key := range []string{class + `\shell\open\command`, class + `\shell\open`, class + `\shell`, class, ending} {
			registry.DeleteKey(registry.CURRENT_USER, classes+key)
		}
	})
	set(classes+ending, class)
	// How Windows is told to start a program for a file: the file's path
	// goes where %1 stands.
	set(classes+class+`\shell\open\command`, `"`+self+`" `+standInWord+` "`+record+`" "%1"`)
	return ending, record
}

// givenTo waits for the stand-in to have written down what it was given.
func givenTo(record string, limit time.Duration) ([]string, bool) {
	for end := time.Now().Add(limit); ; {
		var args []string
		if b, err := os.ReadFile(record); err == nil && json.Unmarshal(b, &args) == nil {
			return args, true
		}
		if time.Now().After(end) {
			return nil, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// What Windows is handed for a film that exists: the film's file and
// nothing else, whatever its name holds. There is no command line of ours
// for anything in a name to be read into; this checks that Windows does not
// read anything into it either on the way to the player.
func TestWindowsIsGivenTheFileAndNothingElse(t *testing.T) {
	ending, record := standInPlayer(t)
	limit := openWait
	openWait = 2 * time.Minute // a slow machine must not be taken for a silent one
	t.Cleanup(func() { openWait = limit })
	dir := t.TempDir()
	for _, name := range []string{
		"plain",
		"with spaces  and two in a row",
		"It's 100% %PATH% %TEMP% %1 %L & more ^ !now! (x86); $(x) `y`",
		"-leading dash",
		"--fullscreen --play-and-exit",
		"comma, equals= plus+ [brackets] {braces} @at #hash ~tilde",
		"ünïcödé 日本語 🎬",
	} {
		path := filepath.Join(dir, name+ending)
		if err := os.WriteFile(path, []byte("not really a film"), 0o644); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		os.Remove(record)
		if err := openWithSystem(path); err != nil {
			t.Errorf("%q: the launcher reports: %v", name, err)
			continue
		}
		args, ok := givenTo(record, 2*time.Minute)
		if !ok {
			t.Errorf("%q: the launcher said the film was opened, and the stand-in for the player never started", name)
			continue
		}
		if len(args) != 1 || args[0] != path {
			t.Errorf("%q: the player was given %q, want only %q", name, args, path)
		}
	}

	// A film deep inside folders: a path longer than the 260 characters
	// that older parts of Windows stop at. Either the player gets that
	// very file, or the launcher says it could not open it (and the page
	// then offers the file for VLC). Saying "opened" with nothing opened,
	// or opening something else, would be faults.
	deep := dir
	for len(deep) < 300 {
		deep = filepath.Join(deep, "a folder with quite a long name")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(deep, "deep film"+ending)
	if err := os.WriteFile(path, []byte("not really a film"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Remove(record)
	if err := openWithSystem(path); err != nil {
		t.Logf("a path of %d characters: Windows says it cannot open it (%v)", len(path), err)
		return
	}
	args, ok := givenTo(record, 2*time.Minute)
	if !ok {
		t.Fatalf("a path of %d characters: the launcher said the film was opened, and the stand-in for the player never started", len(path))
	}
	// Windows may hand the player another spelling of a long path (a short
	// form of each folder's name), so it is the file that is compared.
	want, _ := os.Stat(path)
	if len(args) != 1 {
		t.Fatalf("a path of %d characters: the player was given %q", len(path), args)
	}
	if got, err := os.Stat(args[0]); err != nil || !os.SameFile(got, want) {
		t.Fatalf("a path of %d characters: the player was given %q, which is not the film (%v)", len(path), args, err)
	}
	t.Logf("a path of %d characters reached the player as %q", len(path), args[0])
}
