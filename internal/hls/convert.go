package hls

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Convert says how a film's picture is converted. A Spec that has one is
// for a film whose picture the device cannot show as it is: it is decoded
// and encoded again as H.264, the kind every device plays, and cut on a
// lattice (see Lattice) instead of at the film's own key frames.
//
// Like the rest of a Spec it is made of whole numbers and of words from
// fixed lists, and all of it is the server's own; see Lattice and
// PictureFacts for where the numbers come from.
type Convert struct {
	// Num/Den is the frame rate of the converted picture, one of the usual
	// rates (see PictureFacts.RateNum).
	Num, Den int64
	// AnchorMs is when the picture's first frame is shown, as the converter
	// is told in every run, wherever in the film the run begins. Being told
	// the same every time is what makes every run put the same frame at the
	// same moment.
	AnchorMs int64
	// ContainerStartMs is where the file's own clock starts: ffmpeg counts
	// from there when it is told where to jump to.
	ContainerStartMs int64
	// LeadMs is how long before a chunk a run for that chunk starts reading
	// (see PictureFacts.LeadMs).
	LeadMs int64
	// Targets[k] is the whole second at which the key frame that opens
	// chunk k is asked for. Nothing is asked for at Targets[0]: the first
	// frame is a key frame anyway.
	Targets []int64
	// W and H are the size of the converted picture in pixels, both even
	// (see PictureFacts.Size).
	W, H int
	// Preset is how much work the encoder puts into each frame, and
	// MaxRateK the most the picture may take, in thousands of bits a second.
	Preset   Preset
	MaxRateK int
}

// Preset is how much work the encoder puts into each frame: more work
// gives a better picture for the same number of bits, and takes longer.
type Preset int

const (
	VeryFast Preset = iota
	SuperFast
	UltraFast
)

// word is the preset as the encoder names it.
func (p Preset) word() string {
	switch p {
	case SuperFast:
		return "superfast"
	case UltraFast:
		return "ultrafast"
	}
	return "veryfast"
}

// step is how many seconds apart the cuts are.
func (c *Convert) step() int64 {
	if len(c.Targets) > 1 {
		return c.Targets[1] - c.Targets[0]
	}
	return LatticeSeconds
}

// fits says what is wrong with c as the way to convert a film cut by g, or
// nil. Both come from the same arithmetic (see Lattice), so this fails only
// on a mistake in the program: a list of cuts from one place and a
// conversion from another.
func (c *Convert) fits(g Grid) error {
	switch {
	case !g.Valid():
		return errors.New("the list of cuts for the converted picture cannot be used")
	case c.Num <= 0 || c.Den <= 0 || c.Num > 240*c.Den:
		return fmt.Errorf("the converted picture's frame rate is given as %d/%d", c.Num, c.Den)
	case c.W < 2 || c.H < 2 || c.W%2 != 0 || c.H%2 != 0 || c.W > 1<<14 || c.H > 1<<14:
		return fmt.Errorf("the converted picture's size is given as %d by %d", c.W, c.H)
	case c.MaxRateK <= 0 || c.MaxRateK > 1<<20:
		return fmt.Errorf("the converted picture's bit rate is given as %d", c.MaxRateK)
	case c.LeadMs < 0 || c.LeadMs > maxChunkMs:
		return fmt.Errorf("the converter's lead-in is given as %d ms", c.LeadMs)
	case len(c.Targets) != len(g.Cuts) || c.step() <= 0:
		return fmt.Errorf("the converted picture has %d cuts in a list of %d", len(c.Targets), len(g.Cuts))
	}
	// The first chunk begins with the first frame, on the tick nearest to
	// the anchor, and each later one on the first tick at or after the
	// second its key frame is asked for at.
	if first := tickMs(divRound(c.AnchorMs*c.Num, c.Den*1000), c.Num, c.Den); g.Cuts[0] != first {
		return fmt.Errorf("the list of cuts begins at %d ms, and the converted picture at %d", g.Cuts[0], first)
	}
	for k := 1; k < len(g.Cuts); k++ {
		if at := tickMs(divCeil(c.Targets[k]*c.Num, c.Den), c.Num, c.Den); c.Targets[k] <= c.Targets[k-1] || g.Cuts[k] != at {
			return fmt.Errorf("cut %d is at %d ms, and the converted picture's key frame for it, asked for at %d s, comes at %d", k, g.Cuts[k], c.Targets[k], at)
		}
	}
	return nil
}

// ErrLeftLattice means a converted picture came out with a key frame
// somewhere other than where the list of cuts has one. The chunks could
// then not hold what the list says they do, so the run is ended.
var ErrLeftLattice = errors.New("the converted picture left the lattice")

// latticeChunk says which chunk of a converted film a fragment belongs to.
// ms is when the fragment's first frame is shown, first the chunk the run
// was started for, and cur the chunk the run's last fragment went into (-1
// before there has been one).
//
// The converter is told to put a key frame at every cut and nowhere else,
// and it starts a fragment at every key frame: so each fragment is one
// chunk, the next one in the list, and begins exactly where the list says.
// Two things are allowed for. A run started in the middle of the film
// begins a little before its chunk (see convertArgs), and what it makes
// there is not kept: lead says so. And after the last cut everything
// belongs to the last chunk, whatever key frames it has.
func latticeChunk(g Grid, first, cur int, ms float64) (seg int, lead bool, err error) {
	if cur == len(g.Cuts)-1 {
		return cur, false, nil
	}
	next := cur + 1
	if cur < 0 {
		next = first
	}
	want := float64(g.Cuts[next])
	switch {
	case cur < 0 && ms < want-latticeSlackMs:
		return 0, true, nil
	case math.Abs(ms-want) > latticeSlackMs:
		return 0, false, fmt.Errorf("%w: chunk %d begins at %.3f s, where the list says %.3f s", ErrLeftLattice, next, ms/1000, want/1000)
	}
	return next, false, nil
}

// convertArgs builds the ffmpeg command line for one run that converts the
// picture. The film is decoded, its frames are put on the lattice's ticks,
// and they are encoded with a key frame at every cut still to come.
func convertArgs(spec Spec, startSeg, readRate, burst int) []string {
	c, g := spec.Convert, spec.Grid
	a := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	// A run for a chunk in the middle of the film starts reading a little
	// before the chunk, at the key frame ffmpeg finds there, so that by the
	// chunk's first frame the picture is being decoded properly and the
	// sound around the cut is whole. Not "exactly there": told to be exact,
	// ffmpeg shows the wrong picture when the cut falls in a stretch where
	// the film has no new frame.
	jump := startSeg > 0 && startSeg < len(g.Cuts)
	// Everything before this moment is dropped again once it is decoded: a
	// few frames before the cut. What is made between it and the cut is not
	// sent to anyone (see latticeChunk); it is there so that the sound up
	// to the cut has a place to go that is not the chunk.
	var from string
	if jump {
		if at := g.Cuts[startSeg] - c.LeadMs - c.ContainerStartMs; at > 0 {
			a = append(a, "-ss", clock(at), "-noaccurate_seek")
		}
		from = clock(g.Cuts[startSeg] - max(100, 3000*c.Den/c.Num))
	}
	a = append(a, "-readrate", strconv.Itoa(readRate), "-readrate_initial_burst", strconv.Itoa(burst), "-i", spec.Path, "-map", "0:v:0")

	// The picture, step by step. First every frame is put on a tick, counted
	// from the same moment in every run: a frame the film lacks is filled
	// with the one before it, and a run that starts in the middle is filled
	// from the film's start with its first frame, which the next step drops.
	chain := []string{fmt.Sprintf("fps=%d/%d:start_time=%s", c.Num, c.Den, clock(c.AnchorMs))}
	if jump {
		chain = append(chain, "trim=start="+from)
	}
	// Then it is brought to its size, with square pixels, in the one colour
	// format every device plays.
	chain = append(chain, fmt.Sprintf("scale=%d:%d:flags=bicubic", c.W, c.H), "setsar=1", "format=yuv420p")
	a = append(a, "-vf", strings.Join(chain, ","))

	// The encoder: a quality to aim for and a ceiling on the bit rate. No
	// frames that are stored out of order ("-bf 0"), so that every run
	// writes the same header whichever frame it begins with. And no key
	// frames of its own: not at changes of scene, and not by counting, for
	// which the count is set longer than any chunk.
	perChunk := divCeil(c.step()*c.Num, c.Den)
	a = append(a, "-c:v", "libx264", "-preset", c.Preset.word(), "-crf", "22",
		"-maxrate", strconv.Itoa(c.MaxRateK)+"k", "-bufsize", strconv.Itoa(2*c.MaxRateK)+"k",
		"-profile:v", "high", "-bf", "0", "-sc_threshold", "0", "-g", strconv.FormatInt(2*perChunk+2, 10), "-forced-idr", "1",
		// Frames keep the times the first step gave them.
		"-fps_mode", "vfr")
	// A key frame at every cut from this chunk on. ffmpeg puts each on the
	// first frame at or after the second it is given, which is where the
	// list has the cut (see Lattice).
	var keys []string
	for k := max(startSeg, 1); k < len(c.Targets); k++ {
		keys = append(keys, strconv.FormatInt(c.Targets[k], 10))
	}
	if len(keys) > 0 {
		a = append(a, "-force_key_frames", strings.Join(keys, ","))
	}

	if spec.AudioTrack >= 0 {
		a = append(a, "-map", "0:a:"+strconv.Itoa(spec.AudioTrack))
		if spec.AudioCopy {
			a = append(a, "-c:a", "copy")
			if spec.AudioCodec == "aac" {
				a = append(a, "-bsf:a", "aac_adtstoasc") // as when the picture is untouched: see args
			}
		} else {
			if jump {
				a = append(a, "-af", "atrim=start="+from)
			}
			a = append(a, "-c:a", "aac", "-ac", "2", "-b:a", "192k")
		}
	}
	// Picture and sound are written in the order of their times. When one
	// of them has nothing for a while ffmpeg waits ten seconds for it and
	// then goes on without, which files sound into the wrong chunk when it
	// carries on long after the picture has ended. It is told to wait as
	// long as it takes.
	a = append(a, "-max_interleave_delta", "0")
	return append(a, outputArgs(g)...)
}

// pacer measures how fast a converted film is being made: how many seconds
// of film each second of the clock gives. A PC that makes less than one
// cannot keep up with the viewer.
//
// It is measured from one chunk being finished to the next, so the time a
// run takes to get going is left out, and so is any time the converter was
// made to wait because it was far enough ahead (see Manager.hold).
type pacer struct {
	last   time.Time     // when the run's previous chunk was finished; zero if it has finished none
	held   time.Duration // how long since then the converter was made to wait
	recent []paceSample  // the last few chunks measured, oldest first
}

type paceSample struct{ film, wall time.Duration }

// paceChunks is how many chunks back the pace is taken over.
const paceChunks = 3

// restart is called when a new run begins: its first chunk has nothing to
// be measured from.
func (p *pacer) restart() {
	p.last, p.held = time.Time{}, 0
}

// waited notes that the converter was held back for d.
func (p *pacer) waited(d time.Duration) {
	p.held += d
}

// made notes that a chunk holding film's worth of picture was finished at
// now. A chunk given as holding none is not measured, but the next one is
// measured from it.
func (p *pacer) made(film time.Duration, now time.Time) {
	if !p.last.IsZero() && film > 0 {
		wall := max(now.Sub(p.last)-p.held, time.Millisecond)
		p.recent = append(p.recent, paceSample{film, wall})
		if len(p.recent) > paceChunks {
			p.recent = p.recent[len(p.recent)-paceChunks:]
		}
	}
	p.last, p.held = now, 0
}

// pace returns seconds of film per second of the clock over the chunks
// measured, and how many those are.
func (p *pacer) pace() (float64, int) {
	var film, wall time.Duration
	for _, s := range p.recent {
		film, wall = film+s.film, wall+s.wall
	}
	if wall <= 0 {
		return 0, 0
	}
	return film.Seconds() / wall.Seconds(), len(p.recent)
}
