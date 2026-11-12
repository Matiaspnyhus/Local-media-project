package hls

import "strconv"

// A picture that is converted can be cut wherever we like, because the
// converter can be told to put a key frame there. So a converted film is not
// cut by its own key frames (see FindCuts) but on a lattice: an even row of
// cuts a few seconds apart, worked out here by arithmetic alone. Nothing in
// the film has to be searched for before it plays, and the times in the list
// are exact however the converter got to a chunk.
//
// The arithmetic copies what ffmpeg's "fps" filter does, which is the filter
// that puts the frames in place:
//
//   - Time is counted in ticks, one per frame at the film's frame rate, from
//     zero on the film's own clock. Every frame that comes out of the filter
//     sits on a whole tick.
//   - The filter is told when the picture starts (the anchor), and shows the
//     first frame on the tick nearest to that. It is told the same in every
//     run, so every run puts the same frame on the same tick.
//   - Chunk k, for k of 1 and up, is asked to begin at a whole second: the
//     second the picture starts in, rounded up, plus k steps. It begins at
//     the first tick at or after that second, which is where ffmpeg puts a
//     key frame it is asked for by time.

// LatticeSeconds is how long a chunk of a converted film is meant to be.
// Shorter than a repackaged chunk (SegmentSeconds), because each one has to
// be made before it can be sent: a viewer who jumps waits for one of them.
const LatticeSeconds = 3

// maxLatticeChunks is the most chunks a converted film is cut into. The
// converter is given the time of every cut still to come on its command
// line, and a command line can only be so long (32,767 characters on
// Windows). A film too long for this many chunks of the usual length gets
// longer ones. maxTargetChars bounds the same thing from the other side:
// how many characters the times may take together, however many digits
// each has.
const (
	maxLatticeChunks = 4000
	maxTargetChars   = 24000
)

// latticeTailMs is how close to the picture's last frame, or to the end of
// the file, a cut may be. A chunk shorter than this is not worth a request.
const latticeTailMs = 500

// soundRunsOnMs is how long after the picture's last frame a file may go
// on before its sound is said to run on (see Convert.SoundRunsOn). It is
// well under the ten seconds ffmpeg waits for a track that has nothing,
// with room for the second or two of frames an encoder holds back.
const soundRunsOnMs = 5000

// latticeSlackMs is how far from its place in the list a converted chunk
// may begin. The list is in whole milliseconds and a tick seldom is, which
// accounts for half of one.
const latticeSlackMs = 2

// usualRates are the frame rates a converted picture may have, as fractions.
// Nearly every film has one of them to begin with.
var usualRates = [...][2]int64{{24000, 1001}, {24, 1}, {25, 1}, {30000, 1001}, {30, 1}, {50, 1}, {60000, 1001}, {60, 1}}

// divRound divides a by b (which is above zero) and rounds to the nearest
// whole number, a half going away from zero: ffmpeg's own rule.
func divRound(a, b int64) int64 {
	if a < 0 {
		return -((-a + b/2) / b)
	}
	return (a + b/2) / b
}

// divCeil divides a by b (which is above zero) and rounds up.
func divCeil(a, b int64) int64 {
	q := a / b
	if a%b > 0 {
		q++
	}
	return q
}

// tickMs is the time of a tick, to the nearest millisecond.
func tickMs(tick, num, den int64) int64 {
	return divRound(tick*den*1000, num)
}

// anchor returns the time the fps filter is told the picture starts at, and
// the tick it then shows the first frame on.
//
// That is the picture's start itself, with one exception. A start that lies
// half-way between two ticks could be rounded either way: ffmpeg reads the
// time as a decimal fraction of a second and works in millionths, we work in
// whole milliseconds, and the two need not agree to the last digit. A start
// within a millisecond of half-way is therefore moved a millisecond further
// from it, where there is no doubt. Nobody can see a millisecond.
func anchor(startMs, num, den int64) (anchorMs, tick0 int64) {
	// One tick is den*1000 of these units, and one millisecond is num.
	tick := den * 1000
	at := ((startMs*num)%tick + tick) % tick
	if off := at - tick/2; off >= 0 && off <= num {
		startMs++
	} else if off < 0 && -off <= num {
		startMs--
	}
	return startMs, divRound(startMs*num, tick)
}

// Lattice works out where a converted film is cut.
//
// startMs is when the picture's first frame is shown, lastMs when its last
// one is (0 if that is not known), and endMs where the file ends, all on
// the file's own clock; num/den is the frame rate the picture is converted
// to, one of the usual ones.
//
// It returns the list of cuts, and in Convert what the converter has to be
// told to cut exactly there: the rate, the anchor and the whole seconds at
// which key frames are asked for. The caller adds how large and how good
// the picture is to be. If the numbers make no sense the grid returned is
// not valid (see Grid.Valid), and there is nothing to convert by.
//
// The list ends with the picture. Sound that carries on after the last
// frame, as it does in some films for many seconds, sits in the last chunk:
// there are no key frames to cut it by once the picture has stopped.
func Lattice(startMs, lastMs, endMs, num, den int64) (Grid, Convert) {
	if num <= 0 || den <= 0 {
		return Grid{}, Convert{}
	}
	frame := divCeil(den*1000, num)
	if lastMs <= startMs {
		lastMs = endMs - frame // not known: taken to run to the end of the file
	}
	if lastMs < startMs {
		return Grid{}, Convert{}
	}
	if endMs < lastMs+frame {
		endMs = lastMs + frame // the file ends no earlier than its last frame does
	}
	anchorMs, tick0 := anchor(startMs, num, den)
	g := Grid{Cuts: []int64{tickMs(tick0, num, den)}, From: "lattice"}
	g.StartMs = g.Cuts[0]
	first := divCeil(startMs, 1000) // the whole second the count of steps starts from
	c := Convert{Num: num, Den: den, AnchorMs: anchorMs, Targets: []int64{first}, SoundRunsOn: endMs-lastMs > soundRunsOnMs}

	// No cut later than this.
	limit := min(lastMs, endMs) - latticeTailMs
	// The step: the usual one, or as much longer as keeps the number of
	// chunks, and the length of the list of their times, within bounds.
	most := int64(maxLatticeChunks)
	if digits := int64(len(strconv.FormatInt(max(limit/1000, 0), 10))); most > maxTargetChars/(digits+1) {
		most = maxTargetChars / (digits + 1)
	}
	step := int64(LatticeSeconds)
	if span := limit - first*1000; span > 0 {
		step = max(step, span/(most*1000)+1) // the least that gives under "most" cuts after the first
	}
	for k := int64(1); ; k++ {
		second := first + k*step
		ms := tickMs(divCeil(second*num, den), num, den) // the first tick at or after it
		if ms > limit {
			break
		}
		g.Cuts = append(g.Cuts, ms)
		c.Targets = append(c.Targets, second)
	}
	// A chunk can only be so long (see maxChunkMs). Sound that outlasts the
	// picture by more than that is still all sent, in the last chunk; the
	// list then says less than the chunk holds, which players put up with.
	g.EndMs = min(endMs, g.Cuts[len(g.Cuts)-1]+maxChunkMs)
	if !g.Valid() {
		return Grid{}, Convert{}
	}
	return g, c
}
