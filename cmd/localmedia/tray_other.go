//go:build !windows

package main

// runTray has nothing to show on this system; it waits for the server.
func runTray(a *app, done <-chan error) { <-done }
