package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The program starts helper programs (ffmpeg, ffprobe). None of them may
// outlive it: on Windows a helper that is still running blocks its own
// file, which is the installer's or the next update's to replace.
//
// These tests start the real program with a stand-in for ffprobe that does
// nothing but stay alive, as a helper with a long job would (a long film
// being read through). The stand-in is this very test program, started
// again with LM_TEST_STANDIN set: see TestMain.

// standInEnv names the folder a stand-in helper reports to. The program
// under test passes it on to the helpers it starts without knowing.
const standInEnv = "LM_TEST_STANDIN"

func TestMain(m *testing.M) {
	if dir := os.Getenv(standInEnv); dir != "" {
		standIn(dir)
		return
	}
	os.Exit(m.Run())
}

// standIn is the helper: it says that it has started, by leaving a file
// named after its process number, then stays alive. Where the system can
// tie a helper's life to the program's, it also says when that was done.
// It ends by itself after ten minutes, so that a test gone wrong leaves
// nothing behind for long.
func standIn(dir string) {
	pid := strconv.Itoa(os.Getpid())
	os.WriteFile(filepath.Join(dir, "started-"+pid), nil, 0o600)
	end := time.Now().Add(10 * time.Minute)
	tied := false
	for time.Now().Before(end) {
		if !tied && tiedToTheProgram() {
			tied = true
			os.WriteFile(filepath.Join(dir, "tied-"+pid), nil, 0o600)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// helped is the program, running, with one helper it has started.
type helped struct {
	t         *testing.T
	cmd       *exec.Cmd
	out       *bytes.Buffer
	exited    chan struct{}
	dir       string // where the stand-in reports
	helper    int    // the helper's process number
	helperAt  time.Time
	helperEnd func(limit time.Duration) bool // waits for the helper to be gone
}

// startHelped builds and starts the program, sets it up with one film, and
// waits until it has a helper running: it inspects a new film with ffprobe.
func startHelped(t *testing.T) *helped {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the program")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	h := &helped{t: t, out: &bytes.Buffer{}, exited: make(chan struct{}), dir: filepath.Join(work, "helpers")}
	media, data := filepath.Join(work, "media"), filepath.Join(work, "data")
	for _, d := range []string{h.dir, media} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(media, "A Film (2001).mkv"), []byte("not really a film"), 0o644)
	exe := filepath.Join(work, exeName())
	build(t, exe, "dev", false)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	h.cmd = exec.Command(exe, "--data", data, "--port", strconv.Itoa(port), "--no-browser", "--no-tray", "--no-update")
	h.cmd.Env = append(os.Environ(), "LOCALMEDIA_FFPROBE="+self, standInEnv+"="+h.dir)
	h.cmd.Stdout, h.cmd.Stderr = h.out, h.out
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { h.cmd.Wait(); close(h.exited) }()
	t.Cleanup(func() {
		h.cmd.Process.Kill()
		<-h.exited
		// A helper that the program failed to stop must not be left to
		// the next test, or to the machine.
		if p, err := os.FindProcess(h.helper); err == nil && h.helper != 0 {
			p.Kill()
		}
	})

	jar, _ := cookiejar.New(nil)
	s := &site{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), c: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	s.waitVersion("dev", 60*time.Second)
	if code := s.call("POST", "/api/setup", map[string]string{"name": "owner", "password": "a good password", "folder": media}, nil); code != 200 {
		t.Fatalf("setup: %d\n%s", code, h.out.String())
	}
	h.helper = h.waitFor("started-", "the program never started its helper to inspect the new film")
	h.helperAt = time.Now()
	h.helperEnd = watchProcess(t, h.helper)
	return h
}

// waitFor waits for the stand-in to leave a file with this prefix and
// returns the process number in its name.
func (h *helped) waitFor(prefix, orElse string) int {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if m, _ := filepath.Glob(filepath.Join(h.dir, prefix+"*")); len(m) > 0 {
			pid, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(m[0]), prefix))
			if err != nil {
				h.t.Fatal(err)
			}
			return pid
		}
		select {
		case <-h.exited:
			h.t.Fatalf("the program stopped by itself\n%s", h.out.String())
		default:
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s (waited 60 seconds)\n%s", orElse, h.out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ended checks, once the program has been made to end in some way, that it
// is gone and that its helper went with it.
func (h *helped) ended(how string) {
	h.t.Helper()
	// The program itself gives up on a file it is inspecting after a
	// minute, and stops the helper then. A machine so slow that this
	// test took most of that minute cannot tell the two apart.
	if took := time.Since(h.helperAt); took > 40*time.Second {
		h.t.Fatalf("this machine took %v to get from the helper starting to ending the program; too slow for this test to mean anything, run it again", took.Round(time.Second))
	}
	select {
	case <-h.exited:
	case <-time.After(60 * time.Second):
		h.t.Fatalf("%s: the program was still running after 60 seconds\n%s", how, h.out.String())
	}
	if !h.helperEnd(20 * time.Second) {
		h.t.Fatalf("%s: the program is gone, but the helper it started (process %d) is still running 20 seconds later", how, h.helper)
	}
}

// Every way of ending that the program can catch must stop its helpers
// before it is gone.
func TestHelpersEndWhenTheProgramIsToldToStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no signal a test can send to a program without a
		// console. There the stricter test below covers this: a program
		// that is killed can do nothing, and its helpers still end.
		t.Skip("no way to ask a program to stop from a test on Windows; see TestHelpersEndWhenTheProgramIsKilled")
	}
	for _, how := range polite {
		t.Run(how.name, func(t *testing.T) {
			h := startHelped(t)
			if err := h.cmd.Process.Signal(how.signal); err != nil {
				t.Fatal(err)
			}
			h.ended(how.name)
		})
	}
}

// A program that is killed (the installer does that, and so does "End
// task") catches nothing. Where the system can tie a helper's life to the
// program's, the helper must end all the same.
func TestHelpersEndWhenTheProgramIsKilled(t *testing.T) {
	if !helpersAreTied {
		t.Skip("only Windows, where a running helper blocks its own file, ties helpers to the program")
	}
	h := startHelped(t)
	h.waitFor("tied-", "the helper was never tied to the program")
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	h.ended("killed")
}
