//go:build !windows

package subs

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
