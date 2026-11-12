package hls

// The independent checker: do pictures of odd shapes come out of the real
// converter at the size Size works out, with square pixels and upright?

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCheckOddShapesComeOutAsSized(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	lav := func(size string) []string {
		return []string{"-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=25:duration=4"}
	}
	for _, film := range []struct {
		name   string
		recipe []string
	}{
		{"dvd-wide.mkv", append(lav("720x576"), "-vf", "setsar=64/45", "-c:v", "mpeg2video", "-b:v", "2M", "-pix_fmt", "yuv420p")},
		{"dvd-ntsc.mkv", append(lav("720x480"), "-vf", "setsar=32/27", "-c:v", "mpeg2video", "-b:v", "2M", "-pix_fmt", "yuv420p")},
		{"odd.mkv", append(lav("853x479"), "-c:v", "ffv1", "-pix_fmt", "yuv444p")},
		{"odd-tiny.mkv", append(lav("31x17"), "-c:v", "ffv1", "-pix_fmt", "yuv444p")},
		{"upright.mp4", append(lav("608x1080"), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")},
		{"wide4k.mkv", append(lav("3840x1600"), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")},
		{"hdv.mkv", append(lav("1440x1080"), "-vf", "setsar=4/3", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")},
		{"coded1088.mkv", append(lav("1920x1088"), "-c:v", "mpeg4", "-q:v", "5", "-pix_fmt", "yuv420p")},
	} {
		src := filepath.Join(dir, "shape-"+film.name)
		checkMake(t, ffmpeg, src, film.recipe...)
		checkShape(t, ffmpeg, ffprobe, dir, src)
	}
	// A phone's upright recording: stored lying down, with a note to turn it.
	lying := filepath.Join(dir, "shape-lying.mp4")
	checkMake(t, ffmpeg, lying, append(lav("1280x720"), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")...)
	for _, turn := range []string{"90", "-90", "180"} {
		src := filepath.Join(dir, "shape-turned"+turn+".mp4")
		checkMake(t, ffmpeg, src, "-display_rotation:v:0", turn, "-i", lying, "-c", "copy")
		checkShape(t, ffmpeg, ffprobe, dir, src)
	}
}

func checkShape(t *testing.T, ffmpeg, ffprobe, dir, src string) {
	t.Helper()
	facts, err := ProbePicture(context.Background(), ffprobe, src)
	if err != nil {
		t.Errorf("%s: %v", filepath.Base(src), err)
		return
	}
	for _, height := range []int{1080, 480} {
		g, c := facts.Lattice()
		c.W, c.H = facts.Size(height)
		c.Preset, c.MaxRateK = UltraFast, 5000
		if err := c.fits(g); err != nil {
			t.Errorf("%s for %dp: %v", filepath.Base(src), height, err)
			continue
		}
		a := args(Spec{Path: src, Grid: g, AudioTrack: -1, Convert: &c}, 0, 0, 60)
		out := filepath.Join(dir, "shaped.mp4")
		a[len(a)-1] = out
		if said, err := exec.Command(ffmpeg, append([]string{"-y"}, a...)...).CombinedOutput(); err != nil {
			t.Errorf("%s for %dp (%dx%d): the converter failed: %v %s", filepath.Base(src), height, c.W, c.H, err, said)
			continue
		}
		got, _ := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height,sample_aspect_ratio,display_aspect_ratio:stream_side_data=rotation", "-of", "compact=p=0:nk=0", out).Output()
		line := strings.Join(strings.Fields(string(got)), " ")
		t.Logf("%s (%dx%d, pixels %d:%d, turned %d) for %dp: asked %dx%d, came out %s", filepath.Base(src), facts.Width, facts.Height, facts.SarNum, facts.SarDen, facts.Rotation, height, c.W, c.H, line)
		want := "width=" + strconv.Itoa(c.W) + "|height=" + strconv.Itoa(c.H) + "|sample_aspect_ratio=1:1"
		if !strings.HasPrefix(line, want) || strings.Contains(line, "rotation") {
			t.Errorf("%s for %dp: asked %dx%d with square pixels and no turn, came out %s", filepath.Base(src), height, c.W, c.H, line)
		}
	}
}
