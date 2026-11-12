//go:build !windows

package main

// newAwakeHolder: on systems other than Windows the program does not ask
// the system to stay awake yet. (The PC this app is built for first runs
// Windows. A Mac and a Linux desktop each have a small program that does
// it, caffeinate and systemd-inhibit; using them is not built.)
func newAwakeHolder() awakeHolder { return nil }
