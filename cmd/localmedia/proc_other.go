//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// spawn starts another copy of the program that outlives this one.
func spawn(exe string, args []string) (*os.Process, error) {
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

func reportFatal(msg string) { os.Stderr.WriteString(msg + "\n") }
