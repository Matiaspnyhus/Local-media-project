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
	// LeadMs is how long before StartMs the file's first frame lies. It is
	// 0 for nearly every file. A clip cut out of a longer film without
	// re-encoding keeps the frames from the key frame before the cut,
	// marked as not to be shown, because the first frames that are shown
	// cannot be decoded without them. They are sent on for the same
	// reason: the first chunk carries them and is this much longer, and
	// every time the player is told is this much later than the film's own.
	LeadMs int64 `json:"lead,omitempty"`
}

// maxChunkMs is the longest a chunk may be. A chunk is made, sent and held
// by the player in one piece, and a request waits half a minute for it. In
// that time ffmpeg gets through about five minutes of film (the first
// minute at once, the rest eight times as fast as it plays), so a chunk
// longer than four minutes is never ready in time, and a list with one in
// it is not a list to play from. A chunk somewhat shorter than that can be
// late as well, because it is known to be whole only when ffmpeg has sent
// the piece that follows it; it is there for a player that asks again.
const maxChunkMs = 4 * 60 * 1000

// doubtMs is how long a chunk may be before the list it is in is doubted.
// Nearly every film has a key frame every few seconds, so a chunk of over a
// minute usually means that key frames were not found. But not always:
// where little moves (a recording of a screen, of slides) they can truly be
// that far apart. So a chunk that long is believed only in a list made by
// reading the film through, which looks at every frame. Found any other
// way, the list is not used; FindCuts then reads the film through.
const doubtMs = 60 * 1000

// maxLeadMs is the most a film may keep from before its start (see
// Grid.LeadMs). More than that is some other oddity of the file.
const maxLeadMs = 60 * 1000

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
// longer than a chunk can be, or longer than is believed of a list found
// the way this one was (see doubtMs).
func (g Grid) Valid() bool {
	if len(g.Cuts) == 0 || g.Cuts[0] != g.StartMs || g.EndMs <= g.StartMs || g.LeadMs < 0 || g.LeadMs > maxLeadMs {
		return false
	}
	for i := 1; i < len(g.Cuts); i++ {
		if g.Cuts[i] <= g.Cuts[i-1] || g.Cuts[i] >= g.EndMs {
			return false
		}
	}
	if g.From == "read" {
		return g.longest() <= maxChunkMs
	}
	return g.longest() <= doubtMs
}

// longest is the length of the longest chunk, in milliseconds.
func (g Grid) longest() int64 {
	var most int64
	for k, at := range g.Cuts {
		end := g.EndMs
		if k+1 < len(g.Cuts) {
			end = g.Cuts[k+1]
		}
		most = max(most, end-at)
	}
	return most
}

// index is the chunk a fragment starting at ms belongs to.
func (g Grid) index(ms float64) int {
	// The largest k with Cuts[k] <= ms, allowing for rounding.
	k := sort.Search(len(g.Cuts), func(i int) bool { return float64(g.Cuts[i]) > ms+10 }) - 1
	return max(k, 0)
}

// shift is how much later than the film's own every time in the chunks is,
// in milliseconds: enough that none falls below zero.
func (g Grid) shift() int64 {
	return clockShiftMs + g.LeadMs
}

// seconds is how long chunk k lasts.
func (g Grid) seconds(k int) float64 {
	end := g.EndMs
	if k+1 < len(g.Cuts) {
		end = g.Cuts[k+1]
	}
	if k == 0 {
		end += g.LeadMs // the frames before the start are in the first chunk
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
//     for a long recording on a hard disk. The same is done when jumping
//     leaves a chunk of over a minute, to see whether key frames were
//     missed. If none were, the film is cut at the key frames it has: a
//     long chunk takes longer to make, but it is the only kind such a film
//     can be cut into.
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
	start, lead, format := clockStart(ctx, ffprobe, path)
	g := Grid{StartMs: start, EndMs: start + durationMs, Cuts: []int64{start}, From: "probe", LeadMs: lead}
	even := func() Grid {
		u := uniform(start, durationMs)
		u.LeadMs = lead
		return u
	}
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
		keys, missed, err := keyFrames(ctx, ffprobe, path, iv.String())
		if err != nil {
			return even(), fmt.Errorf("ffprobe could not find the key frames: %w", err)
		}
		// Skip repeats (two places that share a key frame) and cuts that would
		// make a chunk too short to be worth a request.
		g.addCuts(keys, 2000)
		// Good enough if no chunk is long enough to be doubted (Valid sees
		// to that), and if either every jump landed on a key frame (then
		// the long stretches there are have no key frame in them) or there
		// are no long stretches.
		if g.Valid() && (missed == 0 || denseEnough(g.Cuts, g.StartMs, g.EndMs)) {
			return g, nil
		}
		// Otherwise jumps in this file do not land on key frames, or its
		// own list of them is thin, or its key frames really are that far
		// apart. Reading it through tells which.
	}
	keys, _, err := keyFrames(ctx, ffprobe, path, "")
	if err != nil {
		return even(), fmt.Errorf("ffprobe could not read the film through for its key frames: %w", err)
	}
	if len(keys) == 0 {
		// Every film begins with a key frame. Not one means that ffprobe
		// could not tell when this film's frames are shown (H.264 in an AVI
		// file is like that), not that the film is one long chunk.
		return even(), errors.New("ffprobe read the film through and could not say when any key frame is shown")
	}
	g = Grid{StartMs: start, EndMs: start + durationMs, Cuts: []int64{start}, From: "read", LeadMs: lead}
	g.addCuts(keys, SegmentSeconds*1000)
	if !g.Valid() {
		return even(), fmt.Errorf("%w (%d found in %d seconds)", ErrKeyFramesTooFar, len(keys), durationMs/1000)
	}
	return g, nil
}

// PlainCuts is the list to play by when FindCuts is not to be waited for
// (reading a long TV recording through can take minutes): a cut every few
// seconds, on the film's own clock. It takes one quick look at the file.
func PlainCuts(ctx context.Context, ffprobe, path string, durationMs int64) Grid {
	if ffprobe == "" || durationMs <= 0 {
		return Uniform(durationMs)
	}
	start, lead, _ := clockStart(ctx, ffprobe, path)
	g := uniform(start, durationMs)
	g.LeadMs = lead
	return g
}

// clockStart asks where the picture's own clock starts, in milliseconds
// (usually 0, but hours in for some TV recordings), how long before that
// the file's first frame lies (see Grid.LeadMs), and how ffprobe names the
// kind of file.
func clockStart(ctx context.Context, ffprobe, path string) (startMs, leadMs int64, format string) {
	// The start as the file states it, and the first frame stored in it.
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0", "-read_intervals", "%+#1",
		"-show_entries", "packet=pts_time:stream=start_time:format=format_name", "-of", "default=noprint_wrappers=1", path)
	hideWindow(cmd)
	out, _ := cmd.Output()
	started, first := false, math.NaN()
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		name, value, _ := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		switch name {
		case "pts_time":
			if v, err := strconv.ParseFloat(value, 64); err == nil && math.IsNaN(first) {
				first = v * 1000
			}
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
	if started && !math.IsNaN(first) {
		if lead := startMs - int64(math.Round(first)); lead > 0 && lead <= maxLeadMs {
			leadMs = lead
		}
	}
	return startMs, leadMs, format
}

// keyFrames asks ffprobe when the picture's key frames are shown, in
// milliseconds, in order. With intervals it looks only where they say (see
// ffprobe's -read_intervals); without, it reads the whole film. others is
// how many of the frames it was shown were not key frames: when jumping,
// how many jumps did not land on one.
func keyFrames(ctx context.Context, ffprobe, path, intervals string) (keys []int64, others int, err error) {
	a := []string{"-v", "error", "-select_streams", "v:0"}
	if intervals != "" {
		a = append(a, "-read_intervals", intervals)
	}
	cmd := exec.CommandContext(ctx, ffprobe, append(a, "-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", path)...)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, err
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 2 {
			continue
		}
		if !strings.HasPrefix(f[1], "K") {
			others++
			continue
		}
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			keys = append(keys, int64(math.Round(v*1000)))
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys, others, nil
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
