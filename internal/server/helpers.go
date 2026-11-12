package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
)

// Besides the converter that makes chunks (package hls), two things start
// helper programs on a viewer's request: taking a subtitle track out of a
// film (ffmpeg reads the whole file) and finding where a film can be cut
// (ffprobe jumps through it). Without care, every request starts its own,
// and one viewer's browser, or a script signed in as anyone, can start
// hundreds. So:
//
//   - requests for the same thing share one run;
//   - only a few runs of each kind go at once, and the rest wait;
//   - a search that failed is not tried again on every Play.
//
// The two kinds have a limit each, so that a film never waits to start
// because two long subtitle jobs are busy.
const (
	maxSubtitleJobs = 2
	maxCutSearches  = 2
	// cutSearchLimit is how long one search may take. Nearly every search
	// takes a moment; a long TV recording on a slow disk has to be read
	// through, which can take minutes.
	cutSearchLimit = 20 * time.Minute
	// cutRetryAfter is how long a failed search for a file is remembered.
	cutRetryAfter = 30 * time.Minute
	// maxSubtitleBytes bounds what one subtitle job may hold in memory. A
	// film's subtitles as text are well under a megabyte.
	maxSubtitleBytes = 32 << 20
)

// cutWait is how long a Play call waits for a search before starting the
// film with the plain list of cuts. The search carries on, and the next
// Play of the film finds its result. (A variable so that tests need not
// wait as long.)
var cutWait = 20 * time.Second

// helpers is the state behind the sharing and the limits.
type helpers struct {
	subMu    sync.Mutex
	subJobs  map[string]*subtitleJob // by the kept copy's name
	subSlots chan struct{}

	cutMu    sync.Mutex
	cutRuns  map[string]*cutSearch // by file and stamp
	cutFails map[string]cutFailure // by file
	cutSlots chan struct{}

	// clocks has nothing to do with helper programs. It is kept here with
	// the rest of what the server holds in memory about films being played
	// (see playerClocks).
	clocks playerClocks

	// convert is what converting pictures needs kept (see convert.go).
	convert *conversion

	// speeds is how fast this PC converts (see speeds.go), open the films
	// that devices have open, and trouble the count of what they reported
	// about them (see trouble.go).
	speeds  speedBook
	open    openFilms
	trouble troubleLog
}

func newHelpers() *helpers {
	return &helpers{
		subJobs: map[string]*subtitleJob{}, subSlots: make(chan struct{}, maxSubtitleJobs),
		cutRuns: map[string]*cutSearch{}, cutFails: map[string]cutFailure{}, cutSlots: make(chan struct{}, maxCutSearches),
		convert: newConversion(),
	}
}

// subtitleJob is one subtitle being taken out of a film. However many
// viewers ask for it meanwhile, ffmpeg reads the film once, and each of
// them is sent the same text as it comes.
type subtitleJob struct {
	cancel context.CancelFunc

	mu      sync.Mutex
	text    []byte        // what ffmpeg has produced so far
	caption bool          // text holds at least one caption, not just its first line
	done    bool          // ffmpeg is gone; text will not grow
	ok      bool          // text is the whole subtitle
	more    chan struct{} // closed, and replaced, whenever text grows or the job ends
	viewers int           // requests still listening
}

// grew wakes everyone waiting for more text. The caller holds j.mu.
func (j *subtitleJob) grew() {
	close(j.more)
	j.more = make(chan struct{})
}

// keptName is the name a subtitle made by ffmpeg is kept under: which file
// it came from and which subtitle of that file, with the file's size and
// date, so that a file that changes gets a fresh copy.
//
// The last part says how the copy was made. Copies kept by the first
// version could lack lines (see runSubtitleJob) and do not carry it, so
// none of them is used again.
func keptName(fileID, key string, size, mtime int64) string {
	return fmt.Sprintf("%s-%s-%d-%d-r2", fileID, key, size, mtime)
}

// subtitleByFFmpeg converts subtitle track n of path to WebVTT. The result
// is sent while it is being made, since reading a long film through takes
// a while, and kept under name for next time.
//
// The caller has said that a browser may keep the answer for a while. That
// holds for the kept copy, which is whole. An answer that is still being
// made may yet turn out cut short, so a browser is told not to keep it.
//
// When ffmpeg fails, the answer is an error if nothing has been sent yet,
// and is cut off if captions have (see below). It is never a success with
// nothing in it.
func (s *Server) subtitleByFFmpeg(w http.ResponseWriter, r *http.Request, path string, n int, name string) {
	dir := filepath.Join(s.dataDir, "cache", "subtitles")
	cached := filepath.Join(dir, name+".vtt")
	if b, err := os.ReadFile(cached); err == nil {
		w.Write(b)
		return
	}
	if s.ffmpeg == "" {
		writeErr(w, http.StatusNotFound, "This subtitle needs the converter, which is not installed.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	job := s.joinSubtitleJob(name, func(ctx context.Context, job *subtitleJob) {
		s.runSubtitleJob(ctx, job, path, n, dir, cached)
	})
	if job == nil {
		writeErr(w, http.StatusServiceUnavailable, "Local Media is shutting down.")
		return
	}
	defer s.leaveSubtitleJob(name, job)
	flusher, _ := w.(http.Flusher)
	sent := 0
	for {
		job.mu.Lock()
		text, caption, done, ok, more := job.text[sent:], job.caption, job.done, job.ok, job.more
		job.mu.Unlock()
		switch {
		case done && !ok && sent == 0:
			// Nothing has been sent, so the answer can still say that it
			// failed. That is why nothing is sent before the first caption:
			// ffmpeg writes the answer's first line ("WEBVTT") the moment it
			// starts, also when it then gives up on the whole subtitle, and
			// sent on at once that line made a success out of every failure.
			writeErr(w, http.StatusInternalServerError, "Could not prepare the subtitle.")
			return
		case done && !ok:
			// Captions have gone out under an answer that said "success",
			// which cannot be taken back. Ending it here would look like a
			// whole subtitle that simply stops. Cutting the connection tells
			// the browser the truth: this did not arrive whole.
			panic(http.ErrAbortHandler)
		case len(text) > 0 && (caption || done):
			if _, err := w.Write(text); err != nil {
				return // this viewer went away
			}
			if flusher != nil {
				flusher.Flush()
			}
			sent += len(text)
			continue
		case done:
			return
		}
		select {
		case <-more:
		case <-r.Context().Done():
			return
		}
	}
}

// joinSubtitleJob adds a viewer to the job that is making the subtitle
// kept under name, starting one if there is none. It returns nil when the
// server is closing.
func (s *Server) joinSubtitleJob(name string, run func(context.Context, *subtitleJob)) *subtitleJob {
	h := s.helpers
	h.subMu.Lock()
	defer h.subMu.Unlock()
	job := h.subJobs[name]
	if job == nil {
		if s.ctx.Err() != nil {
			return nil
		}
		ctx, cancel := context.WithCancel(s.ctx)
		job = &subtitleJob{cancel: cancel, more: make(chan struct{})}
		h.subJobs[name] = job
		s.background.Add(1)
		go func() {
			defer s.background.Done()
			run(ctx, job)
			// Whatever happened, nobody may wait on this job for ever, and
			// the next request must start afresh or find the kept copy.
			job.mu.Lock()
			job.done = true
			job.grew()
			job.mu.Unlock()
			h.subMu.Lock()
			if h.subJobs[name] == job {
				delete(h.subJobs, name)
			}
			h.subMu.Unlock()
			cancel()
		}()
	}
	job.mu.Lock()
	job.viewers++
	job.mu.Unlock()
	return job
}

// leaveSubtitleJob takes a viewer off a job. When the last one leaves an
// unfinished job, the job is stopped: nobody is waiting for it any more.
func (s *Server) leaveSubtitleJob(name string, job *subtitleJob) {
	h := s.helpers
	h.subMu.Lock()
	defer h.subMu.Unlock()
	job.mu.Lock()
	defer job.mu.Unlock()
	job.viewers--
	if job.viewers == 0 && !job.done {
		job.cancel()
		// A request arriving now must not join a job that is being stopped.
		if h.subJobs[name] == job {
			delete(h.subJobs, name)
		}
	}
}

// runSubtitleJob waits for its turn, runs ffmpeg, and collects what it
// writes. A finished subtitle is kept before the viewers are told it is
// finished, so whoever asks next finds the kept copy.
//
// ffmpeg is told not to check how the text is encoded (-sub_charenc_mode
// ignore). Left to check, it throws away every line that is not UTF-8 and
// still reports success: a subtitle saved in the old Western European
// encoding lost each line with an accented letter. Unchecked, every line
// comes out as it went in, and is put right here (see readable). Text in
// two bytes a letter (UTF-16) ffmpeg reads properly by itself.
//
// Whatever ffmpeg says while it works means something went wrong, since it
// is asked to report errors only. When it says something and still ends
// well (a film that is cut short gives up only its first captions), the
// viewers get what there is, but it is not kept: it may lack lines, and a
// kept copy is served from then on.
func (s *Server) runSubtitleJob(ctx context.Context, job *subtitleJob, path string, n int, dir, cached string) {
	select {
	case s.helpers.subSlots <- struct{}{}:
		defer func() { <-s.helpers.subSlots }()
	case <-ctx.Done():
		return
	}
	cmd := exec.CommandContext(ctx, s.ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-sub_charenc_mode", "ignore", "-i", path,
		"-map", "0:s:"+strconv.Itoa(n), "-f", "webvtt", "pipe:1")
	hideWindow(cmd)
	var said firstWords
	cmd.Stderr = &said
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		s.subtitleTrouble(path, "could not be prepared", err.Error())
		return
	}
	buf := make([]byte, 32<<10)
	var rest []byte // the start of a line whose end has not come yet
	tooLong := false
	for {
		k, err := out.Read(buf)
		rest = append(rest, buf[:k]...)
		whole := bytes.LastIndexByte(rest, '\n') + 1
		if err != nil {
			whole = len(rest) // the end: the last line is as whole as it will be
		}
		job.mu.Lock()
		if whole > 0 {
			// Every caption has a line with an arrow between its two times.
			job.caption = job.caption || bytes.Contains(rest[:whole], []byte("-->"))
			job.text = append(job.text, readable(rest[:whole])...)
			rest = rest[whole:]
			job.grew()
		}
		tooLong = len(job.text)+len(rest) > maxSubtitleBytes
		job.mu.Unlock()
		if tooLong {
			cmd.Process.Kill()
		}
		if err != nil || tooLong {
			break
		}
	}
	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return // nobody is waiting for it any more, or the program is closing
	case tooLong:
		s.subtitleTrouble(path, "could not be prepared", "they are longer than subtitles can be")
		return
	case err != nil:
		s.subtitleTrouble(path, "could not be prepared", said.orElse(err.Error()))
		return
	case len(job.text) == 0: // (only this function changes the text)
		s.subtitleTrouble(path, "could not be prepared", said.orElse("ffmpeg ended without writing anything"))
		return
	}
	if said.any() {
		s.subtitleTrouble(path, "may lack lines, so they were not kept for next time", said.orElse(""))
	} else {
		job.mu.Lock()
		text := job.text
		job.mu.Unlock()
		s.keepSubtitle(dir, cached, text)
	}
	job.mu.Lock()
	job.ok = true
	job.mu.Unlock()
}

// firstWords keeps the start of what a helper program says and lets the
// rest go by.
type firstWords struct{ b []byte }

func (f *firstWords) Write(p []byte) (int, error) {
	if room := 2048 - len(f.b); room > 0 {
		f.b = append(f.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// any reports whether the program said anything.
func (f *firstWords) any() bool { return len(bytes.TrimSpace(f.b)) > 0 }

// orElse is what the program said, on one line, or other if it said nothing.
func (f *firstWords) orElse(other string) string {
	if !f.any() {
		return other
	}
	return clip(strings.Join(strings.Fields(string(f.b)), " "), 400)
}

// subtitleTrouble notes in the log that a file's subtitles went wrong. The
// log names the file and never its folder (see pathless); ffmpeg names the
// file by its full path, which is cut down to the name first.
func (s *Server) subtitleTrouble(path, what, why string) {
	name := filepath.Base(path)
	s.log.Printf("the subtitles of %s %s: %s", name, what, s.pathless(strings.ReplaceAll(why, path, name)))
}

// keepSubtitle stores a finished subtitle for next time. The copy is kept
// only if it is whole: every write to it and its close succeeded. A short
// copy (the disk filled up) would be served from then on.
func (s *Server) keepSubtitle(dir, cached string, text []byte) {
	err := os.MkdirAll(dir, 0o700)
	if err == nil {
		var tmp *os.File
		if tmp, err = subtitleTemp(dir, "making-*.tmp"); err == nil {
			_, err = tmp.Write(text)
			if cerr := tmp.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = os.Rename(tmp.Name(), cached)
			}
			if err != nil {
				os.Remove(tmp.Name())
			}
		}
	}
	if err != nil {
		s.log.Printf("a subtitle could not be kept for next time: %s", s.pathless(err.Error()))
	}
}

// subtitleTemp makes the file a subtitle is written to before it is kept.
// Tests replace it to stand in for a full disk.
var subtitleTemp = os.CreateTemp

// cutSearch is one search for where a film can be cut. Every Play call
// for the file while it runs waits for the same one.
type cutSearch struct {
	done chan struct{} // closed when grid and ok are set
	grid hls.Grid      // the cuts found; when ok is false, the plain list to use instead
	ok   bool
}

// cutFailure remembers that a search failed for a file as it was then.
type cutFailure struct {
	stamp string
	at    time.Time
	plain hls.Grid // the list to play by meanwhile
}

// longestChunk is the longest chunk a list of cuts may hold and still be
// believed. Chunks are meant to last a few seconds (hls.SegmentSeconds) and
// are cut at key frames, which films have every few seconds. A list with a
// chunk longer than this came from a search that did not find the key
// frames, which has happened for whole kinds of file. Played by such a
// list, a film does not start until the converter has worked through the
// whole chunk, so the plain every-few-seconds list is used instead.
const longestChunk = time.Minute

// usableGrid reports whether a list of cuts is in order and has no
// absurdly long chunk.
func usableGrid(g hls.Grid) bool {
	if !g.Valid() {
		return false
	}
	for i, at := range g.Cuts {
		end := g.EndMs
		if i+1 < len(g.Cuts) {
			end = g.Cuts[i+1]
		}
		if end-at > longestChunk.Milliseconds() {
			return false
		}
	}
	return true
}

// plainGrid is the list of cuts used when a film's key frames could not be
// found: a cut every few seconds. The failed search hands one back that is
// on the film's own clock (a TV recording's clock can start hours in);
// when it could not even tell that much, the clock is taken to start at 0.
func plainGrid(fromSearch hls.Grid, durationMs int64) hls.Grid {
	if fromSearch.Valid() {
		return fromSearch
	}
	return hls.Uniform(durationMs)
}

// grid returns where a file is cut into chunks, working it out the first
// time the file is played this way. When that cannot be done (or not in
// time for this request) the answer is a cut every few seconds, with which
// the film plays but jumping in it is less exact.
func (s *Server) grid(ctx context.Context, fileID, path string, size, mtime, durationMs int64) hls.Grid {
	stamp := fmt.Sprintf("%d-%d-%d", size, mtime, durationMs)
	var have, text string
	if err := s.db.QueryRow(`SELECT stamp, grid FROM cuts WHERE file_id=?`, fileID).Scan(&have, &text); err == nil && have == stamp {
		var g hls.Grid
		if json.Unmarshal([]byte(text), &g) == nil && usableGrid(g) {
			return g
		}
	}
	h := s.helpers
	h.cutMu.Lock()
	if f, ok := h.cutFails[fileID]; ok && f.stamp == stamp && time.Since(f.at) < cutRetryAfter {
		h.cutMu.Unlock()
		return plainGrid(f.plain, durationMs) // it failed a moment ago; it would fail again
	}
	key := fileID + " " + stamp
	run := h.cutRuns[key]
	if run == nil {
		if s.ctx.Err() != nil {
			h.cutMu.Unlock()
			return hls.Uniform(durationMs)
		}
		run = &cutSearch{done: make(chan struct{})}
		h.cutRuns[key] = run
		s.background.Add(1)
		go s.findCuts(run, key, fileID, stamp, path, durationMs)
	}
	h.cutMu.Unlock()
	// The search belongs to no one request: if this viewer gives up, the
	// next Play for the film finds it still running, or finished.
	limit := time.NewTimer(cutWait)
	defer limit.Stop()
	select {
	case <-run.done:
		if run.ok {
			return run.grid
		}
		return plainGrid(run.grid, durationMs)
	case <-ctx.Done():
		return hls.Uniform(durationMs) // nobody is waiting for the answer
	case <-limit.C:
	}
	// Still searching. One quick look tells where the film's clock starts,
	// which the plain list has to agree with.
	quick, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return plainGrid(hls.PlainCuts(quick, s.ffprobe, path, durationMs), durationMs)
}

// findCuts runs one search, when its turn comes, and stores what it finds.
func (s *Server) findCuts(run *cutSearch, key, fileID, stamp, path string, durationMs int64) {
	defer s.background.Done()
	h := s.helpers
	var g hls.Grid
	var err error
	select {
	case h.cutSlots <- struct{}{}:
		ctx, cancel := context.WithTimeout(s.ctx, cutSearchLimit)
		g, err = hls.FindCuts(ctx, s.ffprobe, path, durationMs)
		cancel()
		<-h.cutSlots
		if err == nil && !usableGrid(g) {
			err = errors.New("the search did not find the film's key frames")
		}
	case <-s.ctx.Done():
		err = s.ctx.Err()
	}
	closing := s.ctx.Err() != nil
	switch {
	case err == nil:
		if b, merr := json.Marshal(g); merr == nil {
			s.db.Exec(`INSERT INTO cuts(file_id,stamp,grid) VALUES(?,?,?) ON CONFLICT(file_id) DO UPDATE SET stamp=excluded.stamp, grid=excluded.grid`, fileID, stamp, string(b))
		}
	case !closing:
		s.log.Printf("could not find where to cut %s; jumping in it will be less exact: %s", filepath.Base(path), s.pathless(err.Error()))
	}
	h.cutMu.Lock()
	delete(h.cutRuns, key)
	switch {
	case err == nil:
		delete(h.cutFails, fileID)
	case !closing:
		// Remembered for a while, so that not every Play of this film
		// starts the same doomed search. Old entries go when a new one comes.
		for id, f := range h.cutFails {
			if time.Since(f.at) >= cutRetryAfter {
				delete(h.cutFails, id)
			}
		}
		h.cutFails[fileID] = cutFailure{stamp: stamp, at: time.Now(), plain: g}
	}
	run.grid, run.ok = g, err == nil
	h.cutMu.Unlock()
	close(run.done)
}
