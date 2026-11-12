//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

// hideWindow stops a console window flashing up when a helper program runs.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
