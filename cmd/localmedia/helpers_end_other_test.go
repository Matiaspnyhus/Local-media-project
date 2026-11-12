//go:build !windows

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// polite lists the ways a program can be asked to stop here.
var polite = []struct {
	name   string
	signal os.Signal
}{
	{"asked to stop (SIGTERM)", syscall.SIGTERM},
	{"interrupted (Ctrl+C)", os.Interrupt},
	{"its terminal was closed (SIGHUP)", syscall.SIGHUP},
}

// tiedToTheProgram is for the stand-in helper: only Windows ties helpers.
func tiedToTheProgram() bool { return false }

// watchProcess returns a function that waits for a process to be gone.
func watchProcess(t *testing.T, pid int) func(limit time.Duration) bool {
	return func(limit time.Duration) bool {
		deadline := time.Now().Add(limit)
		for {
			if gone(pid) {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// gone reports whether no process with this number is running. One that
// has ended but has not been collected yet by whoever started it (Linux
// shows it as state Z) is gone for every purpose here.
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return true
	}
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		// The state is the first field after the name, which is in brackets.
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 && strings.HasPrefix(string(b[i+1:]), " Z") {
			return true
		}
	}
	return false
}
