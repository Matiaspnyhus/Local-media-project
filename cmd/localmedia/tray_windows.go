//go:build windows

package main

import (
	"context"
	_ "embed"
	"os"

	"fyne.io/systray"
)

//go:embed icon.ico
var trayIcon []byte

// runTray shows the icon beside the clock and its menu. It must run on the
// program's first thread, and returns when the server has stopped.
func runTray(a *app, done <-chan error) {
	if a.opts.noTray {
		<-done
		return
	}
	finished := make(chan struct{})
	go func() {
		<-done
		close(finished)
		systray.Quit()
	}()
	// If the icon cannot be shown the server still runs; the icon is a
	// convenience, not the program.
	defer func() {
		if recover() != nil {
			<-finished
		}
	}()
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("Local Media")
		systray.SetTooltip("Local Media is running")
		open := systray.AddMenuItem("Open Local Media", "Open the library in your browser")
		systray.AddSeparator()
		auto := systray.AddMenuItemCheckbox("Start with Windows", "Start Local Media when you sign in", autostartEnabled())
		check := systray.AddMenuItem("Check for updates", "Look for a newer version now")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit Local Media", "Stop serving your library")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					openBrowser(a.localURL())
				case <-auto.ClickedCh:
					exe, _ := os.Executable()
					if auto.Checked() {
						if setAutostart(exe, false) == nil {
							auto.Uncheck()
						}
					} else if setAutostart(exe, true) == nil {
						auto.Check()
					}
				case <-check.ClickedCh:
					if a.upd != nil {
						go func() {
							if st := a.upd.Check(context.Background()); st.Available != "" {
								openBrowser(a.localURL() + "/?updates")
							}
						}()
					}
				case <-quit.ClickedCh:
					a.stop()
					return
				case <-finished:
					return
				}
			}
		}()
	}, nil)
	<-finished
}
