package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAwake stands in for the system's side of staying awake and writes
// down what it was asked.
type fakeAwake struct {
	mu     sync.Mutex
	did    []string
	held   bool
	refuse bool
	notes  []string
	clock  time.Time
	active int
}

func (f *fakeAwake) hold() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse {
		f.did = append(f.did, "hold refused")
		return "", errors.New("no")
	}
	f.did, f.held = append(f.did, "hold"), true
	return "", nil
}

func (f *fakeAwake) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.did, f.held = append(f.did, "release"), false
}

func (f *fakeAwake) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.did = append(f.did, "close")
}

func (f *fakeAwake) note(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, fmt.Sprintf(format, args...))
}

func (f *fakeAwake) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *fakeAwake) watching() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fakeAwake) set(change func(*fakeAwake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// wait waits until what was done so far, joined by commas, is want.
func (f *fakeAwake) wait(t *testing.T, want string) {
	t.Helper()
	var got string
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		f.mu.Lock()
		got = strings.Join(f.did, ",")
		f.mu.Unlock()
		if got == want {
			return
		}
	}
	t.Fatalf("asked of the system: %q, want %q", got, want)
}

func (f *fakeAwake) noted(part string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, line := range f.notes {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

// The PC is asked to stay awake when someone starts watching and let go
// when they stop, once each however often the question is asked, and it is
// let go when the program ends.
func TestThePCIsKeptAwakeOnlyWhileSomeoneIsWatching(t *testing.T) {
	f := &fakeAwake{clock: time.Unix(1_800_000_000, 0)}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		stayAwakeWhileWatched(ctx, f.watching, f, f.note, time.Millisecond, f.now)
	}()
	time.Sleep(30 * time.Millisecond) // many looks with nobody watching
	f.wait(t, "")
	f.set(func(f *fakeAwake) { f.active = 2 })
	f.wait(t, "hold")
	time.Sleep(30 * time.Millisecond) // many looks with someone watching
	f.wait(t, "hold")
	f.set(func(f *fakeAwake) { f.active = 0 })
	f.wait(t, "hold,release")
	f.set(func(f *fakeAwake) { f.active = 1 })
	f.wait(t, "hold,release,hold")
	stop()
	<-done
	f.wait(t, "hold,release,hold,release,close")
	if a, b := f.noted("keeping this PC awake: someone is watching"), f.noted("no longer keeping this PC awake"); a != 2 || b != 1 {
		t.Errorf("the log says 'keeping awake' %d times and 'no longer' %d times, want 2 and 1: %q", a, b, f.notes)
	}
}

// A PC that slept although it had been asked not to (a closed lid, a
// laptop on battery) may have dropped the request without a word. When the
// program finds the clock has jumped, it says so in the log and asks
// afresh.
func TestTheRequestIsTakenAfreshAfterThePCSlept(t *testing.T) {
	f := &fakeAwake{clock: time.Unix(1_800_000_000, 0), active: 1}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		stayAwakeWhileWatched(ctx, f.watching, f, f.note, time.Millisecond, f.now)
	}()
	f.wait(t, "hold")
	f.set(func(f *fakeAwake) { f.clock = f.clock.Add(40 * time.Minute) })
	f.wait(t, "hold,release,hold")
	if f.noted("was asleep") != 1 || f.noted("although it had been asked to stay awake") != 1 {
		t.Errorf("the log should say once that the PC slept while asked to stay awake: %q", f.notes)
	}
	if f.noted("keeping this PC awake") != 1 {
		t.Errorf("asking afresh is not news for the log: %q", f.notes)
	}
	// A sleep with nobody watching is noted too, and nothing is asked.
	f.set(func(f *fakeAwake) { f.active = 0 })
	f.wait(t, "hold,release,hold,release")
	f.set(func(f *fakeAwake) { f.clock = f.clock.Add(8 * time.Hour) })
	for end := time.Now().Add(20 * time.Second); f.noted("it was not being kept awake") == 0 && time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
	}
	if f.noted("it was not being kept awake") != 1 {
		t.Errorf("a sleep with nobody watching should be noted once: %q", f.notes)
	}
	stop()
	<-done
	f.wait(t, "hold,release,hold,release,close")
}

// A system that refuses is asked again at the next look, since it may
// change its mind, but the log is told once, not every twenty seconds.
func TestARefusalIsLoggedOnce(t *testing.T) {
	f := &fakeAwake{clock: time.Unix(1_800_000_000, 0), active: 1, refuse: true}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		stayAwakeWhileWatched(ctx, f.watching, f, f.note, time.Millisecond, f.now)
	}()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		f.mu.Lock()
		n := len(f.did)
		f.mu.Unlock()
		if n >= 5 {
			break
		}
	}
	f.set(func(f *fakeAwake) { f.refuse = false })
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		f.mu.Lock()
		held := f.held
		f.mu.Unlock()
		if held {
			break
		}
	}
	stop()
	<-done
	if n := f.noted("could not be asked to stay awake"); n != 1 {
		t.Errorf("the refusal is in the log %d times, want once: %q", n, f.notes)
	}
	if n := f.noted("keeping this PC awake"); n != 1 {
		t.Errorf("once the system agreed, the log should say so once: %q", f.notes)
	}
	f.mu.Lock()
	last := f.did[len(f.did)-2:]
	f.mu.Unlock()
	if last[0] != "release" || last[1] != "close" {
		t.Errorf("at the end the request should be let go and given back, got %q", last)
	}
}

// With nothing to ask the system through (a system this is not built
// for), the program simply does not ask.
func TestWithoutAWayToAskNothingHappens(t *testing.T) {
	asked := 0
	stayAwakeWhileWatched(context.Background(), func() int { asked++; return 1 }, nil, func(string, ...any) { asked++ }, time.Millisecond, time.Now)
	if asked != 0 {
		t.Errorf("asked %d times", asked)
	}
}
