//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Start with Windows is one value under the signed-in user's own "Run"
// key. It needs no administrator rights and affects no other user.
const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "LocalMedia"
)

func autostartCommand(exe string) string { return `"` + exe + `" --no-browser` }

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	return err == nil && strings.TrimSpace(v) != ""
}

func setAutostart(exe string, on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(runValue)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	return k.SetStringValue(runValue, autostartCommand(exe))
}
