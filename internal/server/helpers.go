package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
}

func newHelpers() *helpers {
	return &helpers{
		subJobs: map[string]*subtitleJob{}, subSlots: make(chan struct{}, maxSubtitleJobs),
		cutRuns: map[string]*cutSearch{}, cutFails: map[string]cutFailure{}, cutSlots: make(chan struct{}, maxCutSearches),
	}
}

// subtitleJob is one subtitle being taken out of a film. However many
// viewers ask for it meanwhile, ffmpeg reads the film once, and each of
// them is sent the same text as it comes.
type subtitleJob struct {
	cancel context.CancelFunc

	mu      sync.Mutex
	text    []byte        // what ffmpeg has produced so far
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

// subtitleByFFmpeg converts subtitle track n of path to WebVTT. The result
// is sent while it is being made, since reading a long film through takes
// a while, and kept under name for next time.
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
		text, done, ok, more := job.text[sent:], job.done, job.ok, job.more
		job.mu.Unlock()
		if len(text) > 0 {
			if _, err := w.Write(text); err != nil {
				return // this viewer went away
			}
			if flusher != nil {
				flusher.Flush()
			}
			sent += len(text)
			continue
		}
		if done {
			if !ok && sent == 0 {
				writeErr(w, http.StatusInternalServerError, "Could not prepare the subtitle.")
			}
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
func (s *Server) runSubtitleJob(ctx context.Context, job *subtitleJob, path string, n int, dir, cached string) {
	select {
	case s.helpers.subSlots <- struct{}{}:
		defer func() { <-s.helpers.subSlots }()
	case <-ctx.Done():
		return
	}
	cmd := exec.CommandContext(ctx, s.ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-i", path,
		"-map", "0:s:"+strconv.Itoa(n), "-f", "webvtt", "pipe:1")
	hideWindow(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return
	}
	buf := make([]byte, 32<<10)
	tooLong := false
	for {
		k, err := out.Read(buf)
		if k > 0 {
			job.mu.Lock()
			job.text = append(job.text, buf[:k]...)
			tooLong = len(job.text) > maxSubtitleBytes
			job.grew()
			job.mu.Unlock()
		}
		if tooLong {
			cmd.Process.Kill()
		}
		if err != nil || tooLong {
			break
		}
	}
	if cmd.Wait() != nil || tooLong || ctx.Err() != nil {
		return
	}
	job.mu.Lock()
	text := job.text
	job.mu.Unlock()
	s.keepSubtitle(dir, cached, text)
	job.mu.Lock()
	job.ok = true
	job.mu.Unlock()
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
