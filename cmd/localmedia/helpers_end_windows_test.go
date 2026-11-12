//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// polite is empty: a test has no way to ask a program without a console
// to stop on Windows.
var polite []struct {
	name   string
	signal os.Signal
}

// tiedToTheProgram is for the stand-in helper: it reports whether it has
// been put in the group of helpers that Windows ends when the program
// ends (see helpers_windows.go). That group is told apart from any other
// the test may be running in (a build machine may keep everything it
// starts in one) by two things: it is set to end its members when it is
// closed, and the program that started this helper is not itself in it.
func tiedToTheProgram() bool {
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	// A job handle of 0 asks about the job this process is in.
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
		return false
	}
	if limits.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		return false
	}
	var members struct {
		Assigned uint32
		Listed   uint32
		IDs      [1024]uintptr
	}
	if err := windows.QueryInformationJobObject(0, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&members)), uint32(unsafe.Sizeof(members)), nil); err != nil {
		return false
	}
	parent := uintptr(os.Getppid())
	for _, id := range members.IDs[:min(int(members.Listed), len(members.IDs))] {
		if id == parent {
			return false
		}
	}
	return true
}

// watchProcess returns a function that waits for a process to be gone. It
// takes hold of the process now, so that its number being given to another
// process later cannot mislead it.
func watchProcess(t *testing.T, pid int) func(limit time.Duration) bool {
	t.Helper()
	p, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("the helper (process %d) could not be watched: %v", pid, err)
	}
	t.Cleanup(func() { windows.CloseHandle(p) })
	return func(limit time.Duration) bool {
		event, err := windows.WaitForSingleObject(p, uint32(limit.Milliseconds()))
		return err == nil && event == windows.WAIT_OBJECT_0
	}
}

// Every ending the program can catch (Quit from the icon beside the clock,
// the hand-over to a new version) finishes by ending whatever helpers are
// left, and an update swaps ffmpeg.exe and ffprobe.exe straight after. A
// test cannot ask a program without a console to stop, so this one does
// the tying and the ending itself: a stand-in helper is started by this
// test program, tied to it the way the program ties its helpers, and must
// be gone once the program's way out has run.
func TestTheWayOutStopsTheHelpersThatWereTied(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	helper := exec.Command(self)
	helper.Env = append(os.Environ(), standInEnv+"="+dir)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { helper.Wait(); close(exited) }()
	t.Cleanup(func() {
		helper.Process.Kill()
		<-exited
	})

	tie := tieHelpers(self)
	if tie == nil {
		t.Fatal("Windows would not make the group that helpers are tied in")
	}
	defer tie.end()
	// The stand-in says so itself once it finds that it is in the group.
	tied := filepath.Join(dir, "tied-"+strconv.Itoa(helper.Process.Pid))
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(tied); err == nil {
			break
		}
		select {
		case <-exited:
			t.Fatal("the stand-in helper stopped by itself")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper was never tied to the program (waited 60 seconds)")
		}
	}
	tie.end()
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal("the program's way out has run, but the helper it had tied is still running 20 seconds later")
	}
}
