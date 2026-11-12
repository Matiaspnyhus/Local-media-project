//go:build windows

package main

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows keeps a list of programs that have asked it not to go to sleep,
// each with a reason a person can read ("powercfg /requests" shows the
// list). A program gets on it with a power request: made once, then set
// and cleared as often as needed. Unlike the older way of asking
// (SetThreadExecutionState) a request does not belong to the thread that
// made it, which matters in Go, where work moves between threads.
var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procPowerCreateRequest = kernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest    = kernel32.NewProc("PowerSetRequest")
	procPowerClearRequest  = kernel32.NewProc("PowerClearRequest")
	procGetPowerStatus     = kernel32.NewProc("GetSystemPowerStatus")
)

// The two kinds of request that are made. "System required" stops the
// sleep that follows nobody touching the PC, and leaves the screen free to
// turn off. "Execution required" is what the newer way of sleeping that
// most laptops have (the PC looks off but is not) waits for. The kinds
// that keep the screen on, or that turn "sleep" into a pretend sleep, are
// never asked for.
const (
	powerRequestSystemRequired    = 1
	powerRequestExecutionRequired = 3
)

// reasonContext is Windows' REASON_CONTEXT with a plain sentence as the
// reason. The space after the sentence is what the other form of the
// structure would use; on the 64-bit systems the program is built for the
// whole is 32 bytes, as Windows expects.
type reasonContext struct {
	Version uint32 // POWER_REQUEST_CONTEXT_VERSION, 0
	Flags   uint32 // POWER_REQUEST_CONTEXT_SIMPLE_STRING, 1
	Reason  *uint16
	_       [2]uintptr
}

type powerRequest struct {
	mu     sync.Mutex
	handle uintptr
	held   bool
}

// awakeReason is what the owner sees beside the program's name in Windows'
// list of what is keeping the PC awake.
const awakeReason = "Local Media: someone is watching a film"

// newAwakeHolder makes the request, not yet held. It returns nil on a
// Windows that has no such thing, and the program then runs as before.
func newAwakeHolder() awakeHolder {
	h, err := newPowerRequest(awakeReason)
	if err != nil {
		return nil
	}
	return h
}

func newPowerRequest(reason string) (*powerRequest, error) {
	for _, p := range []*windows.LazyProc{procPowerCreateRequest, procPowerSetRequest, procPowerClearRequest} {
		if err := p.Find(); err != nil {
			return nil, err
		}
	}
	text, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return nil, err
	}
	rc := reasonContext{Version: 0, Flags: 1, Reason: text}
	h, _, callErr := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&rc)))
	if h == 0 || h == ^uintptr(0) {
		return nil, fmt.Errorf("Windows would not make the request: %w", callErr)
	}
	return &powerRequest{handle: h}, nil
}

func (p *powerRequest) hold() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 {
		return "", errors.New("the request has been given back")
	}
	if !p.held {
		// Windows counts: two sets would need two clears. So it is set
		// once, and cleared once.
		ok, _, callErr := procPowerSetRequest.Call(p.handle, powerRequestSystemRequired)
		if ok == 0 {
			return "", fmt.Errorf("Windows refused the request: %w", callErr)
		}
		// Older Windows does not know this kind; the first is enough there.
		procPowerSetRequest.Call(p.handle, powerRequestExecutionRequired)
		p.held = true
	}
	return powerSource(), nil
}

func (p *powerRequest) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle == 0 || !p.held {
		return
	}
	procPowerClearRequest.Call(p.handle, powerRequestExecutionRequired)
	procPowerClearRequest.Call(p.handle, powerRequestSystemRequired)
	p.held = false
}

func (p *powerRequest) close() {
	p.release()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handle != 0 {
		windows.CloseHandle(windows.Handle(p.handle))
		p.handle = 0
	}
}

// powerSource says "on battery" when the PC runs on its battery, where
// many laptops put a request aside after a few minutes, and "" otherwise
// or when Windows does not say.
func powerSource() string {
	// SYSTEM_POWER_STATUS: the first byte is 0 on battery, 1 on mains, 255
	// when not known.
	var status struct {
		ACLineStatus, BatteryFlag, BatteryLifePercent, SystemStatusFlag byte
		BatteryLifeTime, BatteryFullLifeTime                            uint32
	}
	if procGetPowerStatus.Find() != nil {
		return ""
	}
	if ok, _, _ := procGetPowerStatus.Call(uintptr(unsafe.Pointer(&status))); ok == 0 || status.ACLineStatus != 0 {
		return ""
	}
	return "this PC is on battery, where Windows may still put it to sleep after a few minutes: plug it in"
}
