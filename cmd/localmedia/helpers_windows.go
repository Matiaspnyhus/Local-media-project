//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The program starts helper programs: ffmpeg to convert and to take
// subtitles out, ffprobe to inspect files. On Windows a program that is
// running cannot have its file replaced or deleted, so a helper that
// outlives the program stands in the way of the installer, the
// uninstaller and the next update, all of which replace ffmpeg.exe and
// ffprobe.exe.
//
// Every way of ending that the program can catch stops the helpers it
// knows of (the server does that when it closes). Two things are left: a
// helper that slipped through, and a program that is killed (the
// installer does that, and so does "End task"), which catches nothing.
// For both, Windows is asked to do the ending: the helpers are put in a
// group (a job object) that Windows ends, every member of it, the moment
// the program is gone, however it went.
//
// Only helpers are put in the group, never the program itself. A group
// that held the program would hold everything it starts, and some of that
// must outlive it: the new version it hands over to after an update, the
// browser it opens, a player it opens a film in.
//
// The helpers are found by looking, not by being told: every tieEvery the
// program's own children are listed, and those running one of the helper
// programs' files are added to the group. That way every place that starts
// a helper is covered, also ones written later, and nothing else ever is.
// The price is that a helper is on its own for its first moments. One
// killed in exactly those moments is the installer's to stop (it stops
// helpers running from the install folder by itself).

// helpersAreTied says that this system ends the helper programs when the
// program itself is killed.
const helpersAreTied = true

// tieEvery is how often the program looks for helpers it has started.
// Looking takes a few thousandths of a second. A helper that lives for
// less than this needs no tying: it is gone before anyone could mind.
const tieEvery = 2 * time.Second

// helperTie is the group and the loop that fills it.
type helperTie struct {
	job   windows.Handle
	names map[string]bool // the helper programs' file names, in lower case
	self  uint32
	born  int64 // when this program started, as Windows counts time
	// tied remembers which helpers are in the group already: by process
	// number, with the moment that process began. The number alone would
	// not do. Windows gives a number that has come free to the next
	// process it starts, so between two looks one helper can end and
	// another be given its number; it would be taken for the one that was
	// tied, and never be tied itself.
	tied map[uint32]int64
	stop chan struct{}
	done sync.WaitGroup
	once sync.Once
}

// tieHelpers starts tying helpers that run one of the given programs to
// the life of this program. It returns nil, which is safe to use, when
// there is nothing to tie or Windows will not make the group; the helpers
// are then stopped only by the ways the program can catch, as before.
func tieHelpers(programs ...string) *helperTie {
	h := &helperTie{names: map[string]bool{}, self: windows.GetCurrentProcessId(), tied: map[uint32]int64{}, stop: make(chan struct{})}
	for _, p := range programs {
		if p != "" {
			h.names[strings.ToLower(filepath.Base(p))] = true
		}
	}
	if len(h.names) == 0 {
		return nil
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return nil
	}
	h.born = created.Nanoseconds()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil
	}
	// The handle is this program's alone (it is not passed on to anything
	// the program starts), so it closes when the program ends, and that
	// is what makes Windows end the members.
	h.job = job
	h.done.Add(1)
	go func() {
		defer h.done.Done()
		tick := time.NewTicker(tieEvery)
		defer tick.Stop()
		for {
			h.look()
			select {
			case <-h.stop:
				return
			case <-tick.C:
			}
		}
	}()
	return h
}

// look adds to the group every child of this program that runs one of the
// helper programs and is not in it yet.
func (h *helperTie) look() {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	// Made afresh at every look, so that helpers that have ended are
	// forgotten.
	tied := map[uint32]int64{}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ParentProcessID != h.self || !h.names[strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))] {
			continue
		}
		if began, in := h.tie(entry.ProcessID); in {
			tied[entry.ProcessID] = began
		}
	}
	h.tied = tied
}

// tie puts one process in the group, unless that very process is in it
// already. It reports when the process began and whether it is in the
// group now.
func (h *helperTie) tie(pid uint32) (began int64, in bool) {
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, false // it has ended already
	}
	defer windows.CloseHandle(p)
	// Windows remembers who started a process only as a number, and
	// numbers are used again. A process that began before this program
	// did cannot have been started by it, whatever the number says.
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p, &created, &exited, &kernel, &user); err != nil || created.Nanoseconds() < h.born {
		return 0, false
	}
	began = created.Nanoseconds()
	if at, known := h.tied[pid]; known && at == began {
		return began, true
	}
	return began, windows.AssignProcessToJobObject(h.job, p) == nil
}

// end stops every helper that is still running and stops looking for
// more. The program calls it on its way out, after the server has closed
// and stopped the helpers it knows of; this catches any that slipped
// through, before the files they run from are swapped by an update.
func (h *helperTie) end() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		close(h.stop)
		h.done.Wait()
		h.look() // one that started since the last look
		windows.TerminateJobObject(h.job, 1)
		windows.CloseHandle(h.job)
	})
}
