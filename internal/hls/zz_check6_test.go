package hls

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The longest command line there can be: the builder's test measures a run
// from the top with the sound passed through. A run for chunk 1 carries the
// same list of times and more besides.
func TestCheckLongestCommandLine(t *testing.T) {
	if os.Getenv("LM_CHECK") == "" {
		t.Skip()
	}
	path := `C:\` + strings.Repeat("a", 259-len(`C:\.mkv`)) + ".mkv"
	longest, how := 0, ""
	for _, start := range []int64{36000 * 1000, 10000 * 1000, 99999*1000 - 12001*1000, 87000 * 1000} {
		g, conv := Lattice(start, 0, start+12000*1000, 60000, 1001)
		conv.ContainerStartMs, conv.LeadMs, conv.W, conv.H, conv.Preset, conv.MaxRateK = start-21, 15000, 1920, 1080, SuperFast, 12000
		for _, runsOn := range []bool{false, true} {
			conv.SoundRunsOn = runsOn
			for _, seg := range []int{0, 1} {
				for _, copied := range []bool{true, false} {
					c := conv
					a := args(Spec{Path: path, Grid: g, AudioTrack: 11, AudioCopy: copied, AudioCodec: "aac", Convert: &c}, seg, 8, 60)
					n := len(strings.Join(a, `" "`)) + 2 + len(`C:\Program Files\Local Media\ffmpeg.exe" `)
					if n > longest {
						longest = n
						how = fmt.Sprintf("chunk %d of %d, sound passed through: %v, sound runs on: %v", seg, len(g.Cuts), copied, runsOn)
					}
					t.Logf("clock from %d s, %d chunks, chunk %d, sound copied %v, sound runs on %v: %d characters", start/1000, len(g.Cuts), seg, copied, runsOn, n)
				}
			}
		}
	}
	t.Logf("the longest: %d characters (%s)", longest, how)
}
