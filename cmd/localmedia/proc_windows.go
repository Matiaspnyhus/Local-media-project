//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// spawn starts another copy of the program that outlives this one.
func spawn(exe string, args []string) (*os.Process, error) {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

// reportFatal shows a problem that stops the program. The packaged program
// has no console window, so a message box is the only place to say it.
func reportFatal(msg string) {
	os.Stderr.WriteString(msg + "\n")
	text, _ := windows.UTF16PtrFromString(msg)
	title, _ := windows.UTF16PtrFromString("Local Media")
	windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
	_ = unsafe.Pointer(nil)
}
