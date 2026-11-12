//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// signalsEnv marks the copy of the test program that the test below starts.
const signalsEnv = "LM_TEST_SIGNALS"

// The tests in update_wait_test.go run the whole program inside the test
// program, and the program catches the signals that ask it to stop. What
// it had caught stayed caught after it had stopped: from the first such
// test on, the test program no longer ended when it was interrupted, so a
// long test run could not be stopped by hand. startQuiet now gives the
// signals back once its program has stopped.
//
// The proof is a process that ends when it is asked to, so it is made in a
// copy of the test program: the copy runs the program inside itself, stops
// it, and then asks itself to stop.
func TestRunningTheProgramInsideATestGivesTheSignalsBack(t *testing.T) {
	if os.Getenv(signalsEnv) != "" {
		// The copy. This runs last of all, after startQuiet has stopped the
		// program and tidied up after it.
		t.Cleanup(func() {
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
			// Only reached if the request was caught and dropped.
			time.Sleep(10 * time.Second)
		})
		startQuiet(t, nil)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copy := exec.Command(self, "-test.run=^TestRunningTheProgramInsideATestGivesTheSignalsBack$")
	copy.Env = append(os.Environ(), signalsEnv+"=1")
	out, err := copy.CombinedOutput()
	var ended *exec.ExitError
	if errors.As(err, &ended) {
		if how, ok := ended.Sys().(syscall.WaitStatus); ok && how.Signaled() && how.Signal() == syscall.SIGTERM {
			return
		}
	}
	t.Fatalf("a test program that had run the program inside itself was asked to stop, and did not end for it (it ended with: %v)\n%s", err, out)
}
