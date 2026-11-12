package hls

import (
	"os"
	"testing"
)

// The films the untouched-picture tests are made with, through the
// converter: among them the clip cut out of a longer film without
// re-encoding, whose first frames lie before its start.
func TestCheckOtherFilmsConverted(t *testing.T) {
	if os.Getenv("LM_CHECK") == "" {
		t.Skip()
	}
	ffmpeg, ffprobe := tools(t)
	for _, name := range []string{"trimmed", "sparse"} {
		src := source(t, ffmpeg, name)
		t.Run(name, func(t *testing.T) {
			spec, facts := convertSpec(t, ffprobe, src)
			t.Logf("%s: %+v", name, facts)
			t.Logf("%s: cuts %v, anchor %d, lead-in %d", name, spec.Grid.Cuts, spec.Convert.AnchorMs, spec.Convert.LeadMs)
			for _, sound := range []string{"ac3", ""} {
				spec.AudioCopy, spec.AudioCodec = sound != "", sound
				everyConvertedChunk(t, ffmpeg, ffprobe, spec, facts)
			}
		})
	}
}
