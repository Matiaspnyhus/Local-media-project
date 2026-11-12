package hls

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
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
	// key frames), "probe" (ffprobe visited the film) or "uniform".
	From string `json:"from,omitempty"`
}

// Uniform is the grid used when the key frames could not be found: a cut
// every few seconds. Chunks then begin at the first key frame after each
// cut, so the timeline can be off by a few seconds after a jump.
func Uniform(durationMs int64) Grid {
	g := Grid{EndMs: max(durationMs, 1000), From: "uniform"}
	for t := int64(0); t < g.EndMs; t += SegmentSeconds * 1000 {
		g.Cuts = append(g.Cuts, t)
	}
	return g
}

// Valid reports whether a grid read back from storage can be used.
func (g Grid) Valid() bool {
	if len(g.Cuts) == 0 || g.Cuts[0] != g.StartMs || g.EndMs <= g.StartMs {
		return false
	}
	for i := 1; i < len(g.Cuts); i++ {
		if g.Cuts[i] <= g.Cuts[i-1] || g.Cuts[i] >= g.EndMs {
			return false
		}
	}
	return true
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

// FindCuts works out where to cut a film. An MKV file's own list of key
// frames is used when it has a good one, which takes one small read.
// Otherwise ffprobe is asked for a key frame near every few seconds of the
// film; it jumps from place to place rather than reading the file through,
// so that takes a moment, not minutes. Either way the result is worth
// keeping.
func FindCuts(ctx context.Context, ffprobe, path string, durationMs int64) (Grid, error) {
	if ffprobe == "" {
		return Grid{}, fmt.Errorf("ffprobe is not available")
	}
	if durationMs <= 0 {
		return Grid{}, fmt.Errorf("the film's length is not known")
	}
	start := int64(0)
	// Where the picture's own clock starts: usually 0, but not always.
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=start_time", "-of", "csv=p=0", path)
	hideWindow(cmd)
	if out, err := cmd.Output(); err == nil {
		if v, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(string(out)), ","), 64); err == nil {
			start = int64(v*1000 + 0.5)
		}
	}
	g := Grid{StartMs: start, EndMs: start + durationMs, Cuts: []int64{start}, From: "probe"}
	if isMatroska(path) {
		if keys, err := matroskaKeyFrames(path); err == nil && denseEnough(keys, g.StartMs, g.EndMs) {
			g.From = "index"
			g.addCuts(keys, SegmentSeconds*1000)
			return g, nil
		}
	}
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
	cmd = exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0", "-read_intervals", iv.String(),
		"-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", path)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return Grid{}, fmt.Errorf("ffprobe could not find the key frames: %w", err)
	}
	var keys []int64
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 2 || !strings.HasPrefix(f[1], "K") {
			continue
		}
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			keys = append(keys, int64(v*1000+0.5))
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	// Skip repeats (two places that share a key frame) and cuts that would
	// make a chunk too short to be worth a request.
	g.addCuts(keys, 2000)
	return g, nil
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
