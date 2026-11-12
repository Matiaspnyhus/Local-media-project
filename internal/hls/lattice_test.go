package hls

import (
	"fmt"
	"math"
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The lattice for the commonest kind of film: 23.976 frames a second from
// zero. A frame takes 41.7 ms, so three seconds is not a whole number of
// frames and each cut is the first frame after its second.
func TestLatticeOfAnOrdinaryFilm(t *testing.T) {
	// 25.025 seconds: 600 frames, the last at 24.983 s.
	g, c := Lattice(0, 24983, 25025, 24000, 1001)
	want := []int64{0, 3003, 6006, 9009, 12012, 15015, 18018, 21021, 24024}
	if !g.Valid() || fmt.Sprint(g.Cuts) != fmt.Sprint(want) || g.StartMs != 0 || g.EndMs != 25025 || g.From != "lattice" {
		t.Fatalf("the lattice is %+v, want cuts %v", g, want)
	}
	if fmt.Sprint(c.Targets) != "[0 3 6 9 12 15 18 21 24]" || c.AnchorMs != 0 || c.Num != 24000 || c.Den != 1001 || c.step() != 3 {
		t.Errorf("the converter would be told %+v", c)
	}
	// The cut asked for at 21 s is the one to watch. 21 s is 503.4965 frames
	// in: the first frame at or after it is frame 504, at 21.021 s, and that
	// is where ffmpeg puts the key frame. Rounding to the nearest frame, as
	// older versions of ffmpeg did and as comes naturally, gives frame 503,
	// at 20.979 s, a frame early. Every other cut in this film rounds up
	// either way, so only this one tells the two apart.
	if nearest := tickMs(divRound(21*24000, 1001), 24000, 1001); nearest != 20979 || g.Cuts[7] != 21021 {
		t.Errorf("the cut asked for at 21 s is at %d ms (rounding to the nearest frame gives %d), want 21021 and 20979", g.Cuts[7], nearest)
	}
}

func TestLatticeEndsWithThePicture(t *testing.T) {
	// A TV recording: the picture starts at 1.44 s, 25 frames a second. The
	// count of steps starts from the next whole second.
	g, c := Lattice(1440, 26400, 26440, 25, 1)
	if fmt.Sprint(g.Cuts) != "[1440 5000 8000 11000 14000 17000 20000 23000]" || fmt.Sprint(c.Targets) != "[2 5 8 11 14 17 20 23]" || c.AnchorMs != 1440 {
		t.Errorf("a recording that starts at 1.44 s: %v, asked for at %v, anchor %d", g.Cuts, c.Targets, c.AnchorMs)
	}
	// No cut within half a second of the last frame: a chunk that short is
	// not worth having. With the last frame at 9.4 s a cut at 9 s is in...
	if g, _ := Lattice(0, 9500, 9540, 25, 1); fmt.Sprint(g.Cuts) != "[0 3000 6000 9000]" {
		t.Errorf("last frame at 9.5 s: %v", g.Cuts)
	}
	// ...and with it at 9.48 s it is out.
	if g, _ := Lattice(0, 9480, 9520, 25, 1); fmt.Sprint(g.Cuts) != "[0 3000 6000]" {
		t.Errorf("last frame at 9.48 s: %v", g.Cuts)
	}
	// Sound that runs on after the picture does not get chunks of its own:
	// the list ends with the picture and its end stays the file's.
	g, c = Lattice(0, 10958, 21021, 24, 1)
	if fmt.Sprint(g.Cuts) != "[0 3000 6000 9000]" || g.EndMs != 21021 || len(c.Targets) != 4 || !g.Valid() {
		t.Errorf("sound ten seconds longer than the picture: %+v", g)
	}
	// Even when it runs on for longer than a chunk may be. The list then
	// says less than the last chunk holds, and stays one that can be used.
	if g, _ := Lattice(0, 10958, 400000, 24, 1); !g.Valid() || len(g.Cuts) != 4 || g.EndMs != 9000+maxChunkMs {
		t.Errorf("sound six minutes longer than the picture: %+v", g)
	}
	// A film whose last frame was not found is taken to run to the end of
	// the file, and one that does not say where it ends to end with its
	// last frame.
	if g, _ := Lattice(0, 0, 20000, 25, 1); fmt.Sprint(g.Cuts) != "[0 3000 6000 9000 12000 15000 18000]" || g.EndMs != 20000 {
		t.Errorf("last frame unknown: %+v", g)
	}
	if g, _ := Lattice(0, 19960, 0, 25, 1); fmt.Sprint(g.Cuts) != "[0 3000 6000 9000 12000 15000 18000]" || g.EndMs != 20000 {
		t.Errorf("end of file unknown: %+v", g)
	}
	// A film shorter than a chunk is one chunk.
	if g, c := Lattice(0, 2960, 3000, 25, 1); !g.Valid() || len(g.Cuts) != 1 || len(c.Targets) != 1 || c.step() != LatticeSeconds {
		t.Errorf("a three-second film: %+v %+v", g, c)
	}
	// Nothing to go by: no lattice, and nothing that looks like one.
	for _, bad := range [][5]int64{{0, 0, 0, 25, 1}, {0, 5000, 6000, 0, 1}, {0, 5000, 6000, 25, 0}, {9000, 0, 5000, 25, 1}, {0, 5000, 6000, -25, 1}} {
		if g, c := Lattice(bad[0], bad[1], bad[2], bad[3], bad[4]); g.Valid() || len(c.Targets) != 0 {
			t.Errorf("Lattice%v gave %+v", bad, g)
		}
	}
}

// The command line carries the time of every cut, so there may only be so
// many: a long film gets longer chunks.
func TestLatticeOfAVeryLongFilmHasLongerChunks(t *testing.T) {
	for _, c := range []struct {
		name            string
		startMs, lenMs  int64
		wantStep, chars int64
	}{
		{"an ordinary film", 0, 2 * 3600 * 1000, 3, 6},
		{"3 hours 20 minutes", 0, 12000 * 1000, 3, 6},
		{"a little longer than that", 0, 12100 * 1000, 4, 6},
		{"a whole day's recording", 0, 24 * 3600 * 1000, 22, 6},
		{"a recording whose clock starts ten hours in", 36000 * 1000, 12000 * 1000, 3, 6},
		// Times of six digits take a character more each, so fewer fit.
		{"a clock that starts 28 hours in", 100000 * 1000, 12000 * 1000, 4, 7},
		{"a clock that starts four months in", 10000000 * 1000, 12000 * 1000, 5, 9},
	} {
		g, conv := Lattice(c.startMs, 0, c.startMs+c.lenMs, 30000, 1001)
		conv.W, conv.H, conv.MaxRateK = 1920, 1080, 10000
		if err := conv.fits(g); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if conv.step() != c.wantStep || len(g.Cuts) > maxLatticeChunks {
			t.Errorf("%s: %d chunks %d seconds apart, want %d apart and no more than %d", c.name, len(g.Cuts), conv.step(), c.wantStep, maxLatticeChunks)
		}
		var list []string
		for _, s := range conv.Targets[1:] {
			list = append(list, strconv.FormatInt(s, 10))
		}
		if n := len(strings.Join(list, ",")); n > maxTargetChars {
			t.Errorf("%s: the times of the cuts take %d characters, more than %d", c.name, n, maxTargetChars)
		}
		// And no longer than they need be: a second less would be too many.
		most := min(maxLatticeChunks, maxTargetChars/c.chars)
		if conv.step() > LatticeSeconds && (c.lenMs-600)/((conv.step()-1)*1000)+1 <= most {
			t.Errorf("%s: a step of %d seconds, where %d would have done", c.name, conv.step(), conv.step()-1)
		}
		for k := 2; k < len(conv.Targets); k++ {
			if conv.Targets[k]-conv.Targets[k-1] != conv.step() {
				t.Fatalf("%s: the cuts are not evenly spaced at %d: %v", c.name, k, conv.Targets[k-2:k+1])
			}
		}
	}
	// A film too long to be cut even into chunks of a minute is not cut.
	if g, _ := Lattice(0, 0, 400*3600*1000, 25, 1); g.Valid() {
		t.Errorf("a film of 400 hours was cut into %d chunks", len(g.Cuts))
	}
}

// Whatever the film, the lattice is one the manager can use and the
// converter can be told: in order, every cut the first frame at or after
// its second, and the first frame within half a frame of where the picture
// really starts.
func TestLatticeHoldsForAnyFilm(t *testing.T) {
	rnd := rand.New(rand.NewSource(4))
	for i := 0; i < 20000; i++ {
		r := usualRates[rnd.Intn(len(usualRates))]
		start := rnd.Int63n(5000)
		switch rnd.Intn(4) {
		case 0:
			start = 0
		case 1:
			start += 36000000 // a recording
		case 2:
			start -= 2500 // some of them below zero
		}
		length := 100 + rnd.Int63n(4*3600*1000)
		if rnd.Intn(3) == 0 {
			length = 100 + rnd.Int63n(30000)
		}
		last := start + length
		end := last + rnd.Int63n(20000)
		if rnd.Intn(8) == 0 {
			end, last = last, 0
		}
		g, c := Lattice(start, last, end, r[0], r[1])
		c.W, c.H, c.MaxRateK = 320, 180, 1000
		if err := c.fits(g); err != nil {
			t.Fatalf("Lattice(%d, %d, %d, %d/%d): %v", start, last, end, r[0], r[1], err)
		}
		halfFrame := float64(r[1]) * 500 / float64(r[0])
		if off := math.Abs(float64(g.Cuts[0] - start)); off > halfFrame+1.5 {
			t.Fatalf("Lattice(%d, ..., %d/%d): the first frame is put at %d ms, %.1f ms from the picture's start", start, r[0], r[1], g.Cuts[0], off)
		}
		if d := c.AnchorMs - start; d < -1 || d > 1 {
			t.Fatalf("Lattice(%d, ..., %d/%d): the anchor is %d", start, r[0], r[1], c.AnchorMs)
		}
		for k := 1; k < len(g.Cuts); k++ {
			// The first tick at or after the second, worked out the long way.
			tick := math.Ceil(float64(c.Targets[k]) * float64(r[0]) / float64(r[1]))
			if want := math.Round(tick * float64(r[1]) * 1000 / float64(r[0])); float64(g.Cuts[k]) != want {
				t.Fatalf("Lattice(%d, ..., %d/%d): cut %d is at %d ms, want %.0f", start, r[0], r[1], k, g.Cuts[k], want)
			}
		}
		if lastCut := g.Cuts[len(g.Cuts)-1]; len(g.Cuts) > 1 && last > 0 && lastCut > last-latticeTailMs {
			t.Fatalf("Lattice(%d, %d, %d, %d/%d): the last cut, at %d, is within half a second of the last frame", start, last, end, r[0], r[1], lastCut)
		}
	}
}

// A picture that starts half-way between two ticks is the one case where
// two ways of rounding could disagree, so the anchor steps aside.
func TestTheAnchorKeepsClearOfHalfWay(t *testing.T) {
	for _, c := range []struct{ start, num, den, anchor, tick int64 }{
		{0, 25, 1, 0, 0},
		{20, 25, 1, 21, 1}, // exactly half-way: up, as a half is rounded
		{19, 25, 1, 18, 0}, // within a millisecond below it: down
		{21, 25, 1, 22, 1}, // and above it: up
		{18, 25, 1, 18, 0}, // further away: left alone
		{22, 25, 1, 22, 1},
		{21, 24, 1, 22, 1}, // half-way is 20.83 ms
		{20, 24, 1, 19, 0},
		{1440, 25, 1, 1440, 36},
		{1460, 25, 1, 1461, 37},
		{-20, 25, 1, -19, 0}, // below zero too
		{-21, 25, 1, -22, -1},
		{21, 24000, 1001, 22, 1},
	} {
		if a, tick := anchor(c.start, c.num, c.den); a != c.anchor || tick != c.tick {
			t.Errorf("a picture that starts at %d ms, at %d/%d: anchor %d on tick %d, want %d on %d", c.start, c.num, c.den, a, tick, c.anchor, c.tick)
		}
	}
	// For every start and rate: never more than a millisecond moved, and
	// afterwards at least a millisecond from half-way.
	for _, r := range usualRates {
		for start := int64(-3000); start < 6000; start++ {
			a, tick := anchor(start, r[0], r[1])
			at := float64(a) * float64(r[0]) / float64(r[1]) / 1000 // in ticks
			msInTicks := float64(r[0]) / float64(r[1]) / 1000
			if a < start-1 || a > start+1 || math.Abs(at-float64(tick)) > 0.5-msInTicks*0.999 {
				t.Fatalf("a picture that starts at %d ms, at %d/%d: anchor %d (%.4f ticks) on tick %d", start, r[0], r[1], a, at, tick)
			}
		}
	}
}

// The arithmetic above is a copy of what ffmpeg's fps filter does. This
// asks the filter itself: for a picture that starts anywhere in the first
// tenth of a second, and at each of the usual rates, the first frame it
// puts out must be on the tick the lattice says, which is the time the
// list gives for the first chunk.
func TestTheFirstFrameIsWhereTheFpsFilterPutsIt(t *testing.T) {
	_, ffprobe := tools(t)
	starts := []int64{1440, 1480, 36000021, 95443717} // and a few as recordings have them
	for s := int64(0); s <= 100; s++ {
		starts = append(starts, s)
	}
	for _, r := range usualRates {
		r := r
		t.Run(fmt.Sprintf("%d-%d", r[0], r[1]), func(t *testing.T) {
			t.Parallel()
			// One made-up picture per start: a few frames whose times are
			// counted in milliseconds, as in most files, the first at the
			// start; each through the filter as the converter sets it. All
			// of them in one run of ffprobe, which lists the frames that
			// come out. (One run per rate, to keep the command a length
			// every system takes.)
			var graph []string
			for i, start := range starts {
				a, _ := anchor(start, r[0], r[1])
				graph = append(graph, fmt.Sprintf("color=size=16x16:rate=%d/%d:duration=0.2,settb=1/1000,setpts=PTS+%d,fps=%d/%d:start_time=%s[out%d]",
					r[0], r[1], start, r[0], r[1], clock(a), i))
			}
			out, err := exec.Command(ffprobe, "-v", "error", "-f", "lavfi", "-i", strings.Join(graph, ";"),
				"-show_entries", "packet=stream_index,pts_time", "-of", "csv=p=0").Output()
			if err != nil {
				t.Fatalf("ffprobe: %v", err)
			}
			first := map[int]float64{}
			count := map[int]int{}
			for _, line := range strings.Split(string(out), "\n") {
				p := strings.Split(strings.TrimSpace(line), ",")
				if len(p) < 2 {
					continue
				}
				i, err := strconv.Atoi(p[0])
				if err != nil {
					continue
				}
				if count[i] == 0 {
					first[i] = f(p[1])
				}
				count[i]++
			}
			for i, start := range starts {
				g, c := Lattice(start, start+10000, start+10040, r[0], r[1])
				_, tick := anchor(start, r[0], r[1])
				want := float64(tick) * float64(r[1]) / float64(r[0])
				if count[i] == 0 {
					t.Fatalf("a picture that starts at %d ms: the filter put out nothing", start)
				}
				if math.Abs(first[i]-want) > 2e-6 || math.Abs(first[i]*1000-float64(g.Cuts[0])) > 0.501 {
					t.Errorf("a picture that starts at %d ms (anchor %d): the filter puts the first frame at %.6f s; the lattice says tick %d, %.6f s, and the list %d ms",
						start, c.AnchorMs, first[i], tick, want, g.Cuts[0])
				}
				// 0.2 seconds of frames went in, and as many came out: none
				// was doubled or dropped on the way to its tick.
				if in := int(math.Round(0.2 * float64(r[0]) / float64(r[1]))); count[i] < in-1 || count[i] > in+1 {
					t.Errorf("a picture that starts at %d ms: %d frames came out, about %d went in", start, count[i], in)
				}
			}
		})
	}
}
