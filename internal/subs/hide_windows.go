//go:build windows

package subs

import (
	"os/exec"
	"syscall"
)

// hideWindow stops a console window flashing up when a helper is started.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
