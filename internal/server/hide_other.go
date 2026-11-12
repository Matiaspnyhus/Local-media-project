//go:build !windows

package server

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
