package hls

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A viewer jumps to ten seconds before a still of a minute, in a film that
// is ordinary otherwise, and watches on. How long does each chunk take to
// arrive? The manager reads the film as it always does (eight times as fast
// as it plays, after a head start of a minute).
func TestCheckWatchingIntoALongStill(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	src := filepath.Join(dir, "gap60.mkv")
	checkMake(t, ffmpeg, src, "-f", "lavfi", "-i", "testsrc2=size=64x36:rate=30:duration=200", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=200",
		"-vf", `select=not(between(t\,70.01\,130))`, "-fps_mode", "vfr", "-c:v", "libx264", "-preset", "ultrafast", "-g", "60", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-b:a", "64k")
	spec, _ := convertSpec(t, ffprobe, src)
	spec.Convert.SoundRunsOn = true // so that the sound of the still is filed where it belongs
	spec.AudioCopy, spec.AudioCodec = true, "ac3"
	m := manager(t, ffmpeg)
	m.Log = func(string, ...any) {}
	token := m.Open(spec)
	began := time.Now()
	for k := 20; k <= 26; k++ { // from 60 s; the still begins in chunk 23, at 70 s
		asked := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		_, err := m.Segment(ctx, token, k)
		for tries := 0; err != nil && tries < 3 && ctx.Err() == nil; tries++ {
			t.Logf("chunk %d (film from %d s): %v after %.1f s; asking again, as a player does", k, spec.Grid.Cuts[k]/1000, err, time.Since(asked).Seconds())
			_, err = m.Segment(ctx, token, k)
		}
		cancel()
		t.Logf("chunk %d (film from %d s): arrived %.1f s after it was asked for, %.1f s after the jump (%v)", k, spec.Grid.Cuts[k]/1000, time.Since(asked).Seconds(), time.Since(began).Seconds(), err)
		if time.Since(asked) > 10*time.Second {
			t.Errorf("chunk %d took %.1f s to arrive", k, time.Since(asked).Seconds())
		}
	}
}
