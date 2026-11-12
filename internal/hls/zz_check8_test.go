package hls

import (
	"context"
	"path/filepath"
	"testing"
)

// An ordinary film of the longest kind that still has chunks of three
// seconds: 3 hours 20 minutes at 23.976 frames a second, every frame in the
// file. Played through from the top, every one of its 4,000 cuts must be
// where the list says: among them the seconds that are a whole number of
// frames (3003, 6006, 9009).
func TestCheckDenseLongFilm(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	src := filepath.Join(dir, "dense-3h20.mkv")
	checkMake(t, ffmpeg, src, "-f", "lavfi", "-i", "testsrc2=size=64x36:rate=24000/1001:duration=12000", "-c:v", "libx264", "-preset", "ultrafast", "-g", "240", "-pix_fmt", "yuv420p")
	facts, err := ProbePicture(context.Background(), ffprobe, src)
	if err != nil {
		t.Fatal(err)
	}
	g, c := facts.Lattice()
	c.W, c.H, c.Preset, c.MaxRateK = 64, 36, UltraFast, 2000
	if err := c.fits(g); err != nil {
		t.Fatal(err)
	}
	t.Logf("picture from %d to %d ms, %d/%d; %d chunks %d s apart", facts.StartMs, facts.LastMs, c.Num, c.Den, len(g.Cuts), c.step())
	spec := Spec{Path: src, Grid: g, AudioTrack: -1, Convert: &c}
	n := len(g.Cuts)
	worst := checkAgainst(t, ffmpeg, spec, "dense 3h20", []int{1, 1000, 1001, 1002, 2002, 3003, n - 2, n - 1}, true, 3)
	t.Logf("played through and jumped to 8 chunks; the worst difference between list and fragment: %.3f ms", worst)
}
