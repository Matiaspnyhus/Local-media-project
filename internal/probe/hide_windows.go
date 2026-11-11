//go:build windows

package probe

import (
	"os/exec"
	"syscall"
)

// hideWindow stops a console window flashing up for every file inspected.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
