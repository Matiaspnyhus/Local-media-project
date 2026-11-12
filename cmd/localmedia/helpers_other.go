//go:build !windows

package main

// helpersAreTied says whether this system can end the helper programs
// when the program itself is killed (see helpers_windows.go). Here it
// cannot: every way of ending that the program can catch stops them (the
// server does that when it closes), and after a kill a helper runs on
// until its own work is done. That is harmless here: unlike on Windows, a
// program that is running does not stop its file from being replaced.
const helpersAreTied = false

type helperTie struct{}

func tieHelpers(programs ...string) *helperTie { return nil }

func (h *helperTie) end() {}
