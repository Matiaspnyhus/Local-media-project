package hls

// An independent checker's measurements: what ffmpeg really produces against
// what the lattice says. Run only when LM_CHECK names a folder for the films.

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type checkFrag struct {
	ms     float64 // when its picture begins, on the film's clock
	frames int     // how many pictures it holds
	sound  int     // how many pieces of sound
	sndMs  float64 // when its sound begins (NaN if it has none)
}

// checkRun runs ffmpeg with the arguments given and lists the fragments it
// writes; it stops ffmpeg once it has seen limit fragments (0: all).
func checkRun(t *testing.T, ffmpeg string, a []string, g Grid, limit int) ([]checkFrag, []byte, string) {
	t.Helper()
	cmd := exec.Command(ffmpeg, a...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	said := &tail{}
	cmd.Stderr = said
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReaderSize(out, 256<<10)
	var video videoTrack
	var header []byte
	var frags []checkFrag
	soundScale := map[uint32]uint32{}
	for {
		typ, raw, payload, err := boxHeader(br)
		if err != nil {
			break
		}
		body := make([]byte, 0)
		if typ == "ftyp" || typ == "moov" || typ == "moof" {
			body = make([]byte, payload)
			if _, err := io.ReadFull(br, body); err != nil {
				break
			}
		} else {
			if _, err := io.CopyN(io.Discard, br, payload); err != nil {
				break
			}
		}
		switch typ {
		case "ftyp":
			header = append(append(header, raw...), body...)
		case "moov":
			header = append(append(header, raw...), body...)
			video = findVideo(body)
			children(body, func(typ string, trak []byte) {
				if typ != "trak" {
					return
				}
				var id, scale uint32
				children(trak, func(typ string, b []byte) {
					switch typ {
					case "tkhd":
						if len(b) >= 24 && b[0] == 1 {
							id = binary.BigEndian.Uint32(b[20:24])
						} else if len(b) >= 16 {
							id = binary.BigEndian.Uint32(b[12:16])
						}
					case "mdia":
						children(b, func(typ string, b []byte) {
							if typ == "mdhd" {
								if len(b) >= 24 && b[0] == 1 {
									scale = binary.BigEndian.Uint32(b[20:24])
								} else if len(b) >= 16 {
									scale = binary.BigEndian.Uint32(b[12:16])
								}
							}
						})
					}
				})
				soundScale[id] = scale
			})
		case "moof":
			ms, _, ok := fragmentStart(body, video)
			fr := checkFrag{ms: math.NaN(), sndMs: math.NaN()}
			if ok {
				fr.ms = ms - float64(g.shift())
			}
			children(body, func(typ string, traf []byte) {
				if typ != "traf" {
					return
				}
				var id uint32
				var n int
				var base int64 = -1
				children(traf, func(typ string, b []byte) {
					switch typ {
					case "tfhd":
						if len(b) >= 8 {
							id = binary.BigEndian.Uint32(b[4:8])
						}
					case "tfdt":
						if len(b) >= 12 && b[0] == 1 {
							base = int64(binary.BigEndian.Uint64(b[4:12]))
						} else if len(b) >= 8 {
							base = int64(binary.BigEndian.Uint32(b[4:8]))
						}
					case "trun":
						if len(b) >= 8 {
							n += int(binary.BigEndian.Uint32(b[4:8]))
						}
					}
				})
				if id == video.id {
					fr.frames += n
				} else {
					fr.sound += n
					if base >= 0 && soundScale[id] > 0 {
						fr.sndMs = float64(base)*1000/float64(soundScale[id]) - float64(g.shift())
					}
				}
			})
			frags = append(frags, fr)
		}
		if limit > 0 && len(frags) >= limit {
			cmd.Process.Kill()
			break
		}
	}
	io.Copy(io.Discard, br)
	cmd.Wait()
	return frags, header, said.String()
}

func checkDir(t *testing.T) string {
	dir := os.Getenv("LM_CHECK")
	if dir == "" {
		t.Skip("LM_CHECK is not set")
	}
	return dir
}

var checkMaking sync.Mutex

func checkMake(t *testing.T, ffmpeg, path string, recipe ...string) {
	t.Helper()
	if strings.Contains(filepath.Base(path), "base") {
		checkMaking.Lock()
		defer checkMaking.Unlock()
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	a := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, recipe...)
	tmp := path + ".part" + filepath.Ext(path)
	if out, err := exec.Command(ffmpeg, append(a, tmp)...).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v %s", filepath.Base(path), err, out)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// checkAgainst runs the film from the top and from every chunk in "jumps",
// and reports where ffmpeg's fragments are not where the list says.
func checkAgainst(t *testing.T, ffmpeg string, spec Spec, what string, jumps []int, whole bool, limit int) (worst float64) {
	t.Helper()
	g := spec.Grid
	var through []checkFrag
	if whole {
		var said string
		through, _, said = checkRun(t, ffmpeg, args(spec, 0, 1000, 60), g, 0)
		if len(through) != len(g.Cuts) {
			t.Errorf("%s: played through: %d fragments for %d cuts (%s)", what, len(through), len(g.Cuts), said)
		}
		for k := 0; k < len(through) && k < len(g.Cuts); k++ {
			d := through[k].ms - float64(g.Cuts[k])
			worst = max(worst, math.Abs(d))
			if math.Abs(d) > 2 {
				t.Errorf("%s: played through: fragment %d begins at %.3f ms, the list says %d (off by %.3f)", what, k, through[k].ms, g.Cuts[k], d)
				break
			}
		}
	}
	for _, n := range jumps {
		if n <= 0 || n >= len(g.Cuts) {
			continue
		}
		lim := limit
		if lim > 0 {
			lim += 2
		}
		frags, _, said := checkRun(t, ffmpeg, args(spec, n, 1000, 60), g, lim)
		cur := -1
		kept := 0
		for i, fr := range frags {
			seg, lead, err := latticeChunk(g, n, cur, fr.ms)
			if err != nil {
				t.Errorf("%s: jumped to chunk %d: fragment %d: %v", what, n, i, err)
				break
			}
			if lead {
				continue
			}
			if seg < len(g.Cuts)-1 || cur != seg {
				worst = max(worst, math.Abs(fr.ms-float64(g.Cuts[seg])))
			}
			if whole && seg < len(through) && seg < len(g.Cuts)-1 && fr.frames != through[seg].frames && !(limit > 0 && i == len(frags)-1) {
				t.Errorf("%s: jumped to chunk %d: chunk %d has %d frames, played through %d", what, n, seg, fr.frames, through[seg].frames)
			}
			cur = seg
			kept++
		}
		if kept == 0 {
			t.Errorf("%s: jumped to chunk %d: no fragment was kept of %d (%s)", what, n, len(frags), said)
		}
	}
	return worst
}

// Every usual rate, every start from 0 to 100 ms, in three kinds of file.
func TestCheckStartsAndRates(t *testing.T) {
	dir := checkDir(t)
	ffmpeg, ffprobe := tools(t)
	kinds := strings.Split(os.Getenv("LM_CHECK_KINDS"), ",")
	if os.Getenv("LM_CHECK_KINDS") == "" {
		kinds = []string{"mkv", "mp4", "ts"}
	}
	step := int64(1)
	if s := os.Getenv("LM_CHECK_STEP"); s != "" {
		step, _ = strconv.ParseInt(s, 10, 64)
	}
	var mu sync.Mutex
	worstAll := 0.0
	for _, kind := range kinds {
		for _, r := range usualRates {
			kind, r := kind, r
			t.Run(fmt.Sprintf("%s-%d-%d", kind, r[0], r[1]), func(t *testing.T) {
				t.Parallel()
				worst := 0.0
				n := 0
				for start := int64(0); start <= 100; start += step {
					src := filepath.Join(dir, fmt.Sprintf("s-%s-%d-%d-%03d.%s", kind, r[0], r[1], start, kind))
					// The picture and the sound are made once for each rate, and
					// put together anew for each start with the picture moved.
					basev := filepath.Join(dir, fmt.Sprintf("basev-%s-%d-%d.%s", kind, r[0], r[1], kind))
					basea := filepath.Join(dir, fmt.Sprintf("basea-%s.%s", kind, kind))
					pic := []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=64x36:rate=%d/%d:duration=8.3", r[0], r[1])}
					snd := []string{"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=8.3"}
					switch kind {
					case "ts":
						checkMake(t, ffmpeg, basev, append(pic, "-c:v", "mpeg2video", "-bf", "2", "-g", "12", "-b:v", "300k", "-pix_fmt", "yuv420p", "-f", "mpegts")...)
						checkMake(t, ffmpeg, basea, append(snd, "-c:a", "mp2", "-b:a", "64k", "-f", "mpegts")...)
					default:
						checkMake(t, ffmpeg, basev, append(pic, "-c:v", "libx264", "-preset", "ultrafast", "-bf", "2", "-g", "40", "-pix_fmt", "yuv420p")...)
						checkMake(t, ffmpeg, basea, append(snd, "-c:a", "ac3", "-b:a", "64k")...)
					}
					recipe := []string{"-itsoffset", clock(start), "-i", basev, "-i", basea, "-map", "0:v:0", "-map", "1:a:0", "-c", "copy"}
					if kind == "ts" {
						recipe = append(recipe, "-f", "mpegts")
					}
					checkMake(t, ffmpeg, src, recipe...)
					facts, err := ProbePicture(context.Background(), ffprobe, src)
					if err != nil {
						t.Errorf("start %d: facts: %v", start, err)
						continue
					}
					g, c := facts.Lattice()
					c.W, c.H = facts.Size(1080)
					c.Preset, c.MaxRateK = UltraFast, 2000
					if err := c.fits(g); err != nil {
						t.Errorf("start %d: %v", start, err)
						continue
					}
					if c.Num != r[0] || c.Den != r[1] {
						t.Errorf("start %d: the rate was taken as %d/%d", start, c.Num, c.Den)
					}
					if kind != "ts" && facts.StartMs != start {
						t.Logf("start %d: ffprobe says the picture starts at %d ms (container %d)", start, facts.StartMs, facts.ContainerStartMs)
					}
					spec := Spec{Path: src, Grid: g, AudioTrack: 0, Convert: &c}
					var jumps []int
					for k := 1; k < len(g.Cuts); k++ {
						jumps = append(jumps, k)
					}
					w := checkAgainst(t, ffmpeg, spec, fmt.Sprintf("start %d (facts %d, anchor %d, cuts %v)", start, facts.StartMs, c.AnchorMs, g.Cuts), jumps, true, 0)
					worst = max(worst, w)
					n++
					if os.Getenv("LM_CHECK_KEEP") == "" {
						os.Remove(src)
					}
				}
				t.Logf("%d films; the worst difference between list and fragment: %.3f ms", n, worst)
				mu.Lock()
				worstAll = max(worstAll, worst)
				mu.Unlock()
			})
		}
	}
}
