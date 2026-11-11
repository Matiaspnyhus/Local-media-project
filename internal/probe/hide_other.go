//go:build !windows

package probe

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
