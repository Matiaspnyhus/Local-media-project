//go:build windows

package main

import "testing"

func TestStartWithWindowsCanBeSwitchedOnAndOff(t *testing.T) {
	was := autostartEnabled()
	defer setAutostart(`C:\somewhere\localmedia.exe`, was)
	if err := setAutostart(`C:\Program Files\Local Media\localmedia.exe`, true); err != nil {
		t.Fatal(err)
	}
	if !autostartEnabled() {
		t.Fatal("switched on, but not reported as on")
	}
	if err := setAutostart("", false); err != nil {
		t.Fatal(err)
	}
	if autostartEnabled() {
		t.Fatal("switched off, but still reported as on")
	}
	if err := setAutostart("", false); err != nil {
		t.Fatalf("switching off twice: %v", err)
	}
}
