package hls

// More of the independent checker's measurements: long films, cuts late in
// the film, clocks that start late.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A film of few frames is cheap to make and to read however long it is, and
// the converter fills it to the rate it is told all the same: so the
// arithmetic late in a long film can be asked of ffmpeg at every rate.
// (No sound: a film this still would have its sound misfiled, which is
// another matter.)
func TestCheckLateCuts(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	// (Films whose clock starts hours in were tried as well, made with
	// ffmpeg's -output_ts_offset into Matroska. ffprobe gives no start for
	// such a file as a whole, ffmpeg then takes "-ss" as a time on the file's
	// own clock, and every jump reads the film from the top: see the
	// checker's report. They are left out here because that takes hours with
	// the converter that is shipped.)
	for _, film := range []struct {
		name    string
		seconds int
	}{
		{"3h20", 12000},
		{"4h10", 15000},
		{"11h", 39600},
	} {
		src := filepath.Join(dir, "sparse-"+film.name+".mkv")
		checkMake(t, ffmpeg, src, "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=64x36:rate=1/4:duration=%d", film.seconds),
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "2", "-pix_fmt", "yuv420p")
		facts, err := ProbePicture(context.Background(), ffprobe, src)
		if err != nil {
			t.Fatalf("%s: %v", film.name, err)
		}
		t.Logf("%s: picture from %d to %d ms, file ends %d, container starts %d", film.name, facts.StartMs, facts.LastMs, facts.EndMs, facts.ContainerStartMs)
		for _, r := range usualRates {
			film, r := film, r
			t.Run(fmt.Sprintf("%s-%d-%d", film.name, r[0], r[1]), func(t *testing.T) {
				t.Parallel()
				g, c := Lattice(facts.StartMs, facts.LastMs, facts.EndMs, r[0], r[1])
				c.ContainerStartMs, c.LeadMs = facts.ContainerStartMs, facts.LeadMs()
				c.W, c.H, c.Preset, c.MaxRateK = 64, 36, UltraFast, 2000
				if err := c.fits(g); err != nil {
					t.Fatal(err)
				}
				spec := Spec{Path: src, Grid: g, AudioTrack: -1, Convert: &c}
				n := len(g.Cuts)
				// Chunks spread over the film, the ones whose second is a whole
				// number of frames at the rates with 1001 in them, and the last.
				jumps := []int{1, 2, 3, n - 3, n - 2, n - 1}
				for k := 97; k < n; k += max(1, n/6) {
					jumps = append(jumps, k)
				}
				for k := 1; k < n; k++ {
					if c.Targets[k]%1001 == 0 {
						jumps = append(jumps, k-1, k, k+1)
					}
				}
				worst := checkAgainst(t, ffmpeg, spec, fmt.Sprintf("%s at %d/%d", film.name, r[0], r[1]), jumps, false, 3)
				t.Logf("%d chunks %d s apart, %d jumped to; the worst difference between list and fragment: %.3f ms", n, c.step(), len(jumps), worst)
			})
		}
	}
}

// The same played through from the top, all of it, at the two rates that
// make the most frames and the least even ones. With the converter that is
// shipped this takes hours: it reads a film whose frames are seconds apart
// at about the speed it plays (see the checker's report). TestCheckDenseLongFilm
// is the one to run there.
func TestCheckLongFilmPlayedThrough(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	if os.Getenv("LM_CHECK_LONG") == "" {
		t.Skip("LM_CHECK_LONG is not set")
	}
	for _, film := range []string{"3h20", "4h10"} {
		src := filepath.Join(dir, "sparse-"+film+".mkv")
		facts, err := ProbePicture(context.Background(), ffprobe, src)
		if err != nil {
			t.Fatalf("%s: %v", film, err)
		}
		for _, r := range [][2]int64{{24000, 1001}, {60000, 1001}, {25, 1}} {
			film, r := film, r
			t.Run(fmt.Sprintf("%s-%d-%d", film, r[0], r[1]), func(t *testing.T) {
				t.Parallel()
				g, c := Lattice(facts.StartMs, facts.LastMs, facts.EndMs, r[0], r[1])
				c.ContainerStartMs, c.LeadMs = facts.ContainerStartMs, facts.LeadMs()
				c.W, c.H, c.Preset, c.MaxRateK = 64, 36, UltraFast, 2000
				if err := c.fits(g); err != nil {
					t.Fatal(err)
				}
				spec := Spec{Path: src, Grid: g, AudioTrack: -1, Convert: &c}
				worst := checkAgainst(t, ffmpeg, spec, fmt.Sprintf("%s at %d/%d", film, r[0], r[1]), nil, true, 0)
				t.Logf("%d chunks %d s apart, played through; the worst difference between list and fragment: %.3f ms", len(g.Cuts), c.step(), worst)
			})
		}
	}
}
