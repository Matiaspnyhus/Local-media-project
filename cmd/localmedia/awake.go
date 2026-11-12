package main

import (
	"context"
	"time"
)

// A PC that nobody touches goes to sleep, and Windows does not count
// sending a film to a phone as being used: a laptop left alone falls
// asleep in the middle of the film. So while someone is watching, the
// program asks the system to stay awake, and stops asking when they are
// done.
//
// What this does not do, on purpose: keep the screen on, or change any of
// the owner's settings. And what it cannot do: stop the sleep that follows
// closing a laptop's lid, pressing the power button or choosing Sleep, and
// on many laptops running on battery the system puts the request aside
// after a few minutes. The checklist tells the owner about both.

const (
	// awakeFor is how long after the last sign of watching the PC is still
	// asked to stay awake. A player sends such signs every few seconds
	// while a film plays, so this is the grace for a short pause, the gap
	// between two episodes, or a phone that was locked for a moment. A
	// film paused for longer lets the PC sleep; it plays on after a wake.
	awakeFor = 5 * time.Minute
	// awakeLook is how often the question "is anyone watching?" is asked.
	awakeLook = 20 * time.Second
	// A look that comes this much later than it should means the PC slept
	// meanwhile (or the program was held up for as long).
	awakeGap = 90 * time.Second
)

// awakeHolder is the system's side: one request to stay awake, held or not.
// hold and release may each be called when already in that state.
type awakeHolder interface {
	// hold asks the system to stay awake. The words it returns say what
	// the PC runs on, where that matters ("on battery"), or "".
	hold() (how string, err error)
	release()
	close()
}

// stayAwakeWhileWatched holds the request for as long as active says that
// someone is watching, until ctx ends. note writes a line into the
// program's log: when the request was taken and let go, and when the PC
// was found to have slept all the same, which is how a support bundle
// shows why a film stopped.
func stayAwakeWhileWatched(ctx context.Context, active func() int, h awakeHolder, note func(string, ...any), look time.Duration, now func() time.Time) {
	if h == nil {
		return
	}
	defer h.close()
	held, failed := false, false
	last := now().Round(0)
	tick := time.NewTicker(look)
	defer tick.Stop()
	for {
		at := now().Round(0) // the PC's own clock, which keeps going while it sleeps
		slept := at.Sub(last) > look+awakeGap
		if slept {
			was := "it was not being kept awake"
			if held {
				was = "although it had been asked to stay awake"
			}
			note("this PC was asleep, or the program was held up, from %s to %s, %s", last.UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339), was)
		}
		last = at
		watching := active() > 0
		switch {
		case watching && (!held || slept):
			// After a sleep the system may have dropped the request
			// without a word, so it is taken afresh.
			if held {
				h.release()
			}
			how, err := h.hold()
			switch {
			case err != nil && !failed:
				note("this PC could not be asked to stay awake while a film is watched: %v", err)
				failed = true
			case err == nil && !held:
				if how != "" {
					how = " (" + how + ")"
				}
				note("keeping this PC awake: someone is watching%s", how)
			}
			held = err == nil
		case !watching && held:
			h.release()
			held = false
			note("no longer keeping this PC awake: nobody is watching")
		}
		select {
		case <-ctx.Done():
			if held {
				h.release()
			}
			return
		case <-tick.C:
		}
	}
}
