package hls

// The independent checker's films that the builder's tests do not have: a
// picture that stands still for longer than ffmpeg waits for it, and a film
// with changes of scene.

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A film in which nothing new is shown for 30 seconds (the file holds no
// frame between 4 and 34 s), with sound all the way through. Where does the
// sound of each chunk end up, played through and jumped to?
func TestCheckALongStill(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	src := filepath.Join(dir, "still30.mkv")
	checkMake(t, ffmpeg, src, "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30:duration=45", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=45",
		"-vf", `select=not(between(t\,4.01\,34))`, "-fps_mode", "vfr", "-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-pix_fmt", "yuv420p", "-g", "120", "-c:a", "aac", "-b:a", "96k")
	spec, facts := convertSpec(t, ffprobe, src)
	t.Logf("picture from %d to %d ms, file ends %d; cuts %v; sound runs on: %v", facts.StartMs, facts.LastMs, facts.EndMs, spec.Grid.Cuts, spec.Convert.SoundRunsOn)
	ctx := context.Background()
	for _, forced := range []bool{false, true} {
		for _, sound := range []string{"aac", ""} {
			c := *spec.Convert
			c.SoundRunsOn = forced // which is what gives "-max_interleave_delta 0"
			s := spec
			s.Convert = &c
			s.AudioCopy, s.AudioCodec = sound != "", sound
			how := fmt.Sprintf("told to wait for the picture: %v, sound %q", forced, sound)
			m := manager(t, ffmpeg)
			m.Log = func(string, ...any) {}
			straight := m.Open(s)
			header, err := m.Init(ctx, straight)
			if err != nil {
				t.Errorf("%s: header: %v", how, err)
				m.Close()
				continue
			}
			var line string
			silent := 0
			for k := range s.Grid.Cuts {
				b, err := m.Segment(ctx, straight, k)
				if err != nil {
					t.Errorf("%s: played through: chunk %d: %v", how, k, err)
					break
				}
				got := inspect(t, ffprobe, joined(t, header, b))
				if got["audio"].Packets == "" || got["audio"].Packets == "0" {
					silent++
					line += fmt.Sprintf(" %d:none", k)
				} else {
					line += fmt.Sprintf(" %d:%s@%.2f", k, got["audio"].Packets, f(got["audio"].StartTime)-shift)
				}
			}
			t.Logf("%s: played through, pieces of sound in each chunk and when they begin:%s", how, line)
			if silent > 0 {
				t.Errorf("%s: played through: %d chunks have no sound in them", how, silent)
			}
			// A viewer who jumps to 12 s, in the middle of the still.
			for _, to := range []int{4, 9} {
				jumped := m.Open(s)
				b, err := m.Segment(ctx, jumped, to)
				if err != nil {
					t.Errorf("%s: jumped to chunk %d: %v", how, to, err)
					m.Stop(jumped)
					continue
				}
				h2, _ := m.Init(ctx, jumped)
				got := inspect(t, ffprobe, joined(t, h2, b))
				m.mu.Lock()
				starts := m.sessions[jumped].starts
				m.mu.Unlock()
				t.Logf("%s: jumped to chunk %d (%d ms): %s frames from %.3f, %s pieces of sound from %s; ffmpeg started %d times", how, to, s.Grid.Cuts[to],
					got["video"].Packets, f(got["video"].StartTime)-shift, got["audio"].Packets, got["audio"].StartTime, starts)
				if got["audio"].Packets == "" || got["audio"].Packets == "0" || f(got["audio"].StartTime)-shift > float64(s.Grid.Cuts[to])/1000+0.1 {
					t.Errorf("%s: jumped to chunk %d: the chunk's sound is missing or late", how, to)
				}
				m.Stop(jumped)
			}
			m.Close()
		}
	}
}

// A film whose picture changes completely every 0.7 seconds. The encoder
// must not answer a change of scene with a key frame of its own.
func TestCheckChangesOfScene(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	src := filepath.Join(dir, "scenes.mkv")
	checkMake(t, ffmpeg, src, "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=20", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=20",
		"-vf", `negate=enable='lt(mod(t\,1.4)\,0.7)',hue=h=90:enable='lt(mod(t\,2.8)\,1.4)'`, "-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-pix_fmt", "yuv420p", "-g", "50", "-c:a", "ac3", "-b:a", "96k")
	spec, _ := convertSpec(t, ffprobe, src)
	for _, preset := range []Preset{VeryFast, SuperFast, UltraFast} {
		c := *spec.Convert
		c.Preset = preset
		s := spec
		s.Convert = &c
		frags, _, said := checkRun(t, ffmpeg, args(s, 0, 1000, 60), s.Grid, 0)
		var at []string
		for _, fr := range frags {
			at = append(at, fmt.Sprintf("%.0f", fr.ms))
		}
		t.Logf("preset %s: fragments at %v; cuts %v %s", preset.word(), at, s.Grid.Cuts, said)
		if len(frags) != len(s.Grid.Cuts) {
			t.Errorf("preset %s: %d fragments for %d cuts", preset.word(), len(frags), len(s.Grid.Cuts))
		}
	}
}

// No test of the builder's compares what comes out with what went in: they
// compare one run with another. This does: each converted frame against the
// frame the source film shows at that moment.
func TestCheckConvertedShowsTheSourceAtTheSameTime(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	for _, name := range []string{"film24", "tv25", "phone", "frozen", "longsound", "longgop", "midstream"} {
		src := convertFilm(t, ffmpeg, name)
		spec, facts := convertSpec(t, ffprobe, src)
		a := args(spec, 0, 1000, 60)
		whole := filepath.Join(dir, "whole-"+name+".mp4")
		a[len(a)-1] = whole
		a = append([]string{"-y"}, a...)
		if out, err := exec.Command(ffmpeg, a...).CombinedOutput(); err != nil {
			t.Errorf("%s: %v %s", name, err, out)
			continue
		}
		// Both read with the times they hold. The converted one is a second
		// later (the shift) and is moved back to a hundredth of a second
		// after its frame's own time, so that the source frame "at that
		// moment" is the one shown then, whatever the last digit says.
		cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-copyts", "-i", whole, "-i", src,
			"-lavfi", "[0:v]setpts=PTS-0.990/TB[a];[1:v]format=yuv420p[b];[a][b]psnr=stats_file=-", "-f", "null", "-")
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("%s: comparing: %v", name, err)
			continue
		}
		n, low, minDb, sum := 0, 0, 1000.0, 0.0
		var lows []int
		for _, line := range strings.Split(string(out), "\n") {
			for _, field := range strings.Fields(line) {
				if v, ok := strings.CutPrefix(field, "psnr_avg:"); ok {
					db := 100.0
					if v != "inf" {
						db = f(v)
					}
					if db < 25 {
						low++
						if len(lows) < 12 {
							lows = append(lows, n)
						}
					}
					minDb = min(minDb, db)
					sum += db
					n++
				}
			}
		}
		t.Logf("%s (picture from %d ms): %d frames compared with the source at the same moment: lowest %.1f dB, average %.1f, %d under 25 dB %v", name, facts.StartMs, n, minDb, sum/float64(max(n, 1)), low, lows)
		if low > 0 && name != "midstream" {
			t.Errorf("%s: %d converted frames do not show what the source shows at that moment", name, low)
		}
	}
}
