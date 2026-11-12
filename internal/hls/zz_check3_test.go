package hls

// The independent checker's arithmetic checks that need no ffmpeg.

import (
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Over a wide range of films: the list of key-frame times never takes more
// than the bound, the encoder's own key-frame count is longer than any
// chunk, and what odd inputs give.
func TestCheckLatticeBounds(t *testing.T) {
	if os.Getenv("LM_CHECK") == "" {
		t.Skip()
	}
	rnd := rand.New(rand.NewSource(11))
	longestList, longestLine, mostChunks := 0, 0, 0
	var worstG float64
	path := `C:\` + strings.Repeat("a", 259-len(`C:\.mkv`)) + ".mkv"
	for i := 0; i < 400000; i++ {
		r := usualRates[rnd.Intn(len(usualRates))]
		var start int64
		switch rnd.Intn(8) {
		case 0:
			start = 0
		case 1:
			start = rnd.Int63n(100)
		case 2:
			start = rnd.Int63n(100000) * 1000 / 7
		case 3:
			start = 9990000 + rnd.Int63n(20000) // around the step from four digits to five
		case 4:
			start = 99990000 + rnd.Int63n(20000)
		case 5:
			start = rnd.Int63n(95443718) // anywhere a transport stream can count to
		case 6:
			start = -rnd.Int63n(8000)
		case 7:
			start = rnd.Int63n(int64(1) << 40)
		}
		var length int64
		switch rnd.Intn(5) {
		case 0:
			length = 40 + rnd.Int63n(10000)
		case 1:
			length = 11990000 + rnd.Int63n(20000) // around the most chunks of three seconds
		case 2:
			length = rnd.Int63n(240 * 3600 * 1000)
		default:
			length = rnd.Int63n(5 * 3600 * 1000)
		}
		last := start + length
		end := last + rnd.Int63n(30000)
		if rnd.Intn(6) == 0 && start >= 0 {
			last = 0
		}
		g, c := Lattice(start, last, end, r[0], r[1])
		if !g.Valid() {
			if length < 239*3600*1000 && length > 100 && end > start {
				// Not cut although it could be?
				if length < 200*3600*1000 {
					t.Errorf("Lattice(%d, %d, %d, %d/%d) gave nothing", start, last, end, r[0], r[1])
				}
			}
			continue
		}
		c.ContainerStartMs, c.LeadMs, c.W, c.H, c.Preset, c.MaxRateK = start-21, 15000, 1920, 1080, SuperFast, 12000
		if err := c.fits(g); err != nil {
			t.Fatalf("Lattice(%d, %d, %d, %d/%d): %v", start, last, end, r[0], r[1], err)
		}
		var list []string
		for _, s := range c.Targets[1:] {
			list = append(list, strconv.FormatInt(s, 10))
		}
		n := len(strings.Join(list, ","))
		if n > longestList {
			longestList = n
			t.Logf("list of %d characters: Lattice(%d, %d, %d, %d/%d): %d chunks %d s apart", n, start, last, end, r[0], r[1], len(g.Cuts), c.step())
		}
		if n > maxTargetChars {
			t.Fatalf("Lattice(%d, %d, %d, %d/%d): the list takes %d characters", start, last, end, r[0], r[1], n)
		}
		mostChunks = max(mostChunks, len(g.Cuts))
		if i%50 == 0 || n > 23000 {
			a := args(Spec{Path: path, Grid: g, AudioTrack: 11, AudioCopy: true, AudioCodec: "aac", Convert: &c}, 0, 8, 60)
			line := len(strings.Join(a, `" "`)) + 2 + len(`C:\Program Files\Local Media\ffmpeg.exe" `)
			longestLine = max(longestLine, line)
		}
		// The encoder is told to count this many frames before it adds a key
		// frame of its own. No stretch between two cuts, nor from the last cut
		// to the picture's last frame, may be that long.
		perChunk := divCeil(c.step()*c.Num, c.Den)
		gop := float64(2*perChunk + 2)
		frame := float64(r[1]) * 1000 / float64(r[0])
		for k := 0; k < len(g.Cuts); k++ {
			to := end
			if last > start {
				to = last
			}
			if k+1 < len(g.Cuts) {
				to = g.Cuts[k+1]
			}
			frames := float64(to-g.Cuts[k])/frame + 1
			if frames/gop > worstG {
				worstG = frames / gop
			}
			if frames > gop {
				t.Fatalf("Lattice(%d, %d, %d, %d/%d): chunk %d of %d runs from %d to %d ms, %.0f frames, and the encoder adds a key frame after %.0f", start, last, end, r[0], r[1], k, len(g.Cuts), g.Cuts[k], to, frames, gop)
			}
		}
	}
	t.Logf("the longest list: %d characters; the longest command line tried: %d; the most chunks: %d; the longest chunk is %.2f of the encoder's count", longestList, longestLine, mostChunks, worstG)
}

// What some odd films give.
func TestCheckLatticeOddities(t *testing.T) {
	if os.Getenv("LM_CHECK") == "" {
		t.Skip()
	}
	for _, c := range [][5]int64{
		{-5000, 60000, 60040, 25, 1},
		{-40, 60000, 60040, 25, 1},
		{-60, 60000, 60040, 25, 1},
		{999, 20000, 20040, 25, 1},
		{2981, 20000, 20040, 25, 1},
		{0, 3400, 3440, 25, 1},
		{0, 3600, 3640, 25, 1},
		{0, 10000, 130000, 25, 1},
		{95443000, 95443000 + 7200000, 95443000 + 7200040, 25, 1},
	} {
		g, conv := Lattice(c[0], c[1], c[2], c[3], c[4])
		cuts, targets := g.Cuts, conv.Targets
		if len(cuts) > 6 {
			cuts, targets = cuts[:6], targets[:6]
		}
		t.Logf("Lattice%v: valid %v, %d cuts %v..., asked at %v, anchor %d, end %d, sound runs on %v", c, g.Valid(), len(g.Cuts), cuts, targets, conv.AnchorMs, g.EndMs, conv.SoundRunsOn)
		if g.Valid() {
			conv.W, conv.H, conv.MaxRateK, conv.LeadMs = 320, 180, 1000, 2000
			if err := conv.fits(g); err != nil {
				t.Errorf("Lattice%v: %v", c, err)
			}
			for _, seg := range []int{0, 1} {
				if seg < len(g.Cuts) {
					t.Logf("   chunk %d: %s", seg, strings.Join(args(Spec{Path: "f.mkv", Grid: g, AudioTrack: 0, Convert: &conv}, seg, 8, 60), " "))
				}
			}
		}
	}
}

// The size of the converted picture, for every size and many pixel shapes.
func TestCheckSizes(t *testing.T) {
	if os.Getenv("LM_CHECK") == "" {
		t.Skip()
	}
	type bad struct {
		w, h, sn, sd, height, gw, gh int
		ratio                        float64
	}
	var worst bad
	sars := [][2]int{{1, 1}, {64, 45}, {16, 15}, {32, 27}, {8, 9}, {10, 11}, {40, 33}, {4, 3}, {3, 4}, {2, 1}, {1, 2}, {160, 99}, {12, 11}}
	enlargedH, enlargedW, outside := 0, 0, 0
	for w := 2; w <= 4200; w += 1 {
		step := 1
		if w > 800 {
			step = 7
		}
		for h := 2; h <= 2400; h += step {
			for _, sar := range sars {
				for _, height := range []int{1080, 720, 480} {
					gw, gh := fit(w, h, sar[0], sar[1], height)
					shown := float64(w) * float64(sar[0]) / float64(sar[1])
					if gw < 2 || gh < 2 || gw%2 != 0 || gh%2 != 0 {
						t.Fatalf("%dx%d sar %d:%d for %d: %dx%d", w, h, sar[0], sar[1], height, gw, gh)
					}
					if gh > h && h >= 2 && h%2 == 0 || gh > h+1 {
						enlargedH++
						if enlargedH < 5 {
							t.Errorf("higher than stored: %dx%d sar %d:%d for %d: %dx%d", w, h, sar[0], sar[1], height, gw, gh)
						}
					}
					if float64(gw) > shown+1 && shown >= 2 {
						enlargedW++
						if enlargedW < 5 {
							t.Errorf("wider than shown: %dx%d sar %d:%d (shown %.2f) for %d: %dx%d", w, h, sar[0], sar[1], shown, height, gw, gh)
						}
					}
					boxW := int(math.Ceil(float64(height) * 16 / 9))
					if gh > height || gw > boxW+1 {
						outside++
						if outside < 5 {
							t.Errorf("outside the frame: %dx%d sar %d:%d for %d: %dx%d", w, h, sar[0], sar[1], height, gw, gh)
						}
					}
					if gw >= 240 && gh >= 240 {
						got, want := float64(gw)/float64(gh), shown/float64(h)
						ratio := math.Max(got/want, want/got)
						if ratio > worst.ratio {
							worst = bad{w, h, sar[0], sar[1], height, gw, gh, ratio}
						}
					}
				}
			}
		}
	}
	t.Logf("higher than stored %d times, wider than shown %d, outside the frame %d", enlargedH, enlargedW, outside)
	t.Logf("the worst change of shape among pictures of 240 pixels and more each way: %+v", worst)
	for _, c := range [][5]int{{853, 480, 1, 1, 480}, {1920, 1080, 1, 1, 480}, {720, 480, 32, 27, 480}, {4096, 16, 1, 1, 480}, {16, 4096, 1, 1, 480}, {1, 1, 1, 1, 1080}, {2, 3, 1, 1, 1080},
		{1920, 1088, 1, 1, 1080}, {1918, 1078, 1, 1, 1080}, {7680, 4320, 1, 1, 1080}, {5120, 1440, 1, 1, 1080}, {1080, 1920, 1, 1, 480}, {720, 576, 64, 45, 480}, {352, 576, 24, 11, 1080}, {32768, 2, 1, 1, 1080}} {
		gw, gh := fit(c[0], c[1], c[2], c[3], c[4])
		t.Logf("%dx%d sar %d:%d for %dp: %dx%d", c[0], c[1], c[2], c[3], c[4], gw, gh)
	}
}
