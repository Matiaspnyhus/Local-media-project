package hls

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Grid says where a film is cut into chunks: the time each chunk begins, in
// milliseconds on the file's own clock. Every cut after the first is a key
// frame of the picture (a frame that can be shown without the ones before
// it), because an untouched picture can only be cut there.
//
// The player is told these exact times, so its timeline matches the film
// wherever it starts: subtitles stay in step and a resumed film resumes at
// the right moment.
type Grid struct {
	StartMs int64   `json:"start"` // where the file's clock starts, usually 0
	EndMs   int64   `json:"end"`
	Cuts    []int64 `json:"cuts"` // Cuts[0] == StartMs
	// From says how the cuts were found: "index" (the file's own list of
	// key frames), "probe" (ffprobe visited the film every few seconds),
	// "read" (ffprobe read the film through) or "uniform".
	From string `json:"from,omitempty"`
}

// maxChunkMs is the longest a chunk may be. A chunk is made, sent and held
// by the player in one piece, so one of several minutes is never ready in
// time and is too big to handle. A list with such a chunk in it is a sign
// that the key frames were not found, not a list to play from.
const maxChunkMs = 10 * SegmentSeconds * 1000

// Uniform is the grid used when the key frames could not be found: a cut
// every few seconds. Chunks then begin at the first key frame after each
// cut, so the timeline can be off by a few seconds after a jump.
func Uniform(durationMs int64) Grid {
	return uniform(0, durationMs)
}

// uniform is Uniform for a film whose clock starts at startMs.
func uniform(startMs, durationMs int64) Grid {
	g := Grid{StartMs: startMs, EndMs: startMs + max(durationMs, 1000), From: "uniform"}
	for t := g.StartMs; t < g.EndMs; t += SegmentSeconds * 1000 {
		g.Cuts = append(g.Cuts, t)
	}
	return g
}

// Valid reports whether a grid, read back from storage or just worked out,
// can be used: its cuts are in order and inside the film, and no chunk is
// longer than a chunk can be.
func (g Grid) Valid() bool {
	if len(g.Cuts) == 0 || g.Cuts[0] != g.StartMs || g.EndMs <= g.StartMs {
		return false
	}
	for i := 1; i < len(g.Cuts); i++ {
		if g.Cuts[i] <= g.Cuts[i-1] || g.Cuts[i] >= g.EndMs || g.Cuts[i]-g.Cuts[i-1] > maxChunkMs {
			return false
		}
	}
	return g.EndMs-g.Cuts[len(g.Cuts)-1] <= maxChunkMs
}

// index is the chunk a fragment starting at ms belongs to.
func (g Grid) index(ms float64) int {
	// The largest k with Cuts[k] <= ms, allowing for rounding.
	k := sort.Search(len(g.Cuts), func(i int) bool { return float64(g.Cuts[i]) > ms+10 }) - 1
	return max(k, 0)
}

// seconds is how long chunk k lasts.
func (g Grid) seconds(k int) float64 {
	end := g.EndMs
	if k+1 < len(g.Cuts) {
		end = g.Cuts[k+1]
	}
	return max(float64(end-g.Cuts[k])/1000, 0.1)
}

// maxProbes bounds how many places in a film are looked at, which also
// keeps the command line a sensible length.
const maxProbes = 2000

// addCuts appends to g every key frame that is at least gap after the cut
// before it. keys must be sorted.
func (g *Grid) addCuts(keys []int64, gap int64) {
	for _, k := range keys {
		if k >= g.Cuts[len(g.Cuts)-1]+gap && k < g.EndMs-500 {
			g.Cuts = append(g.Cuts, k)
		}
	}
}

// ErrKeyFramesTooFar means a film's key frames are so far apart that it
// cannot be cut into chunks of a usable length.
var ErrKeyFramesTooFar = errors.New("the film's key frames are too far apart to cut it into chunks")

// FindCuts works out where to cut a film, the quickest way that works:
//
//   - An MKV file's own list of key frames is used when it has a good one,
//     which takes one small read.
//   - Otherwise ffprobe is asked for a key frame near every few seconds of
//     the film; it jumps from place to place rather than reading the file
//     through, so that takes a moment, not minutes.
//   - In some files a jump does not land on a key frame (MPEG-TS, the form
//     of TV recordings and disc streams, has no list to jump by). Then
//     ffprobe reads the film through once and reports every key frame. That
//     is as slow as the disk: a second for a small file, a minute or more
//     for a long recording on a hard disk.
//
// The result is worth keeping. When no good cuts can be found the error
// says why, and the grid returned with it is the one to fall back on: a cut
// every few seconds on the film's own clock (see Uniform), which plays but
// is not worth keeping.
func FindCuts(ctx context.Context, ffprobe, path string, durationMs int64) (Grid, error) {
	if ffprobe == "" {
		return Grid{}, fmt.Errorf("ffprobe is not available")
	}
	if durationMs <= 0 {
		return Grid{}, fmt.Errorf("the film's length is not known")
	}
	start, format := clockStart(ctx, ffprobe, path)
	g := Grid{StartMs: start, EndMs: start + durationMs, Cuts: []int64{start}, From: "probe"}
	if isMatroska(path) {
		if keys, err := matroskaKeyFrames(path); err == nil && denseEnough(keys, g.StartMs, g.EndMs) {
			g.From = "index"
			g.addCuts(keys, SegmentSeconds*1000)
			return g, nil
		}
	}
	// Jumping is not tried on MPEG-TS, where it cannot work and each jump is
	// itself a search through the file.
	if !strings.Contains(format, "mpegts") {
		step := int64(SegmentSeconds * 1000)
		if durationMs/step > maxProbes {
			step = (durationMs/maxProbes/1000 + 1) * 1000
		}
		var iv strings.Builder
		for t := start + step; t < g.EndMs; t += step {
			if iv.Len() > 0 {
				iv.WriteByte(',')
			}
			// "Go to this time and show one packet": ffprobe lands on the key
			// frame at or before it, as ffmpeg will when it starts there.
			fmt.Fprintf(&iv, "%d.%03d%%+#1", t/1000, t%1000)
		}
		if iv.Len() == 0 {
			return g, nil
		}
		keys, err := keyFrames(ctx, ffprobe, path, iv.String())
		if err != nil {
			return uniform(start, durationMs), fmt.Errorf("ffprobe could not find the key frames: %w", err)
		}
		// Skip repeats (two places that share a key frame) and cuts that would
		// make a chunk too short to be worth a request.
		g.addCuts(keys, 2000)
		if denseEnough(g.Cuts, g.StartMs, g.EndMs) {
			return g, nil
		}
		// Long stretches without a cut: either jumps in this file do not land
		// on key frames, or it really has few. Reading it through tells.
	}
	keys, err := keyFrames(ctx, ffprobe, path, "")
	if err != nil {
		return uniform(start, durationMs), fmt.Errorf("ffprobe could not read the film through for its key frames: %w", err)
	}
	g = Grid{StartMs: start, EndMs: start + durationMs, Cuts: []int64{start}, From: "read"}
	g.addCuts(keys, SegmentSeconds*1000)
	if !g.Valid() {
		return uniform(start, durationMs), fmt.Errorf("%w (%d found in %d seconds)", ErrKeyFramesTooFar, len(keys), durationMs/1000)
	}
	return g, nil
}

// clockStart asks where the picture's own clock starts, in milliseconds
// (usually 0, but hours in for some TV recordings), and how ffprobe names
// the kind of file.
func clockStart(ctx context.Context, ffprobe, path string) (startMs int64, format string) {
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=start_time:format=format_name", "-of", "default=noprint_wrappers=1", path)
	hideWindow(cmd)
	out, _ := cmd.Output()
	started := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		name, value, _ := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		switch name {
		case "start_time":
			// For MPEG-TS the line is printed twice (once for the programme
			// the picture belongs to); the first is as good as the second.
			if v, err := strconv.ParseFloat(value, 64); err == nil && !started {
				startMs, started = int64(math.Round(v*1000)), true
			}
		case "format_name":
			format = value
		}
	}
	return startMs, format
}

// keyFrames asks ffprobe when the picture's key frames are shown, in
// milliseconds, in order. With intervals it looks only where they say (see
// ffprobe's -read_intervals); without, it reads the whole film.
func keyFrames(ctx context.Context, ffprobe, path, intervals string) ([]int64, error) {
	a := []string{"-v", "error", "-select_streams", "v:0"}
	if intervals != "" {
		a = append(a, "-read_intervals", intervals)
	}
	cmd := exec.CommandContext(ctx, ffprobe, append(a, "-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", path)...)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var keys []int64
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 2 || !strings.HasPrefix(f[1], "K") {
			continue
		}
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			keys = append(keys, int64(math.Round(v*1000)))
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys, nil
}

// denseEnough says whether a file's own list of key frames is fit to cut
// by. Some files list only one key frame in many; cutting by such a list
// would make chunks so long that a film takes an age to start. Asking
// ffprobe finds the key frames in between.
func denseEnough(keys []int64, start, end int64) bool {
	const longest = 20000
	prev := start
	for _, k := range keys {
		if k < start || k > end {
			continue
		}
		if k-prev > longest {
			return false
		}
		prev = k
	}
	return end-prev <= longest+10000
}
