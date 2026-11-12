package hls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// PictureFacts is what has to be known about a film's picture before it can
// be converted: where its clock stands, how fast its frames come and what
// shape they are. ffprobe finds it with two short looks at the file (see
// ProbePicture). It does not change while the file does not, so it is
// worth keeping.
type PictureFacts struct {
	// StartMs is when the first frame is shown and LastMs when the last one
	// is, on the file's own clock, in milliseconds. LastMs is 0 if the last
	// frame could not be found.
	StartMs int64 `json:"start"`
	LastMs  int64 `json:"last,omitempty"`
	// ContainerStartMs is where the file's clock starts, which is the start
	// of whichever of its tracks starts first: ffmpeg counts from there when
	// it is told where to jump to. EndMs is where the file ends: that start
	// plus the length the file states, or 0 if it states none.
	ContainerStartMs int64 `json:"containerStart"`
	EndMs            int64 `json:"end,omitempty"`
	// RateNum/RateDen is the frame rate, as one of the usual rates: the one
	// the film has, or the nearest to it.
	RateNum int64 `json:"rateNum"`
	RateDen int64 `json:"rateDen"`
	// Width and Height are the frame as stored, in pixels. SarNum:SarDen is
	// the shape of one pixel (1:1 is square; DVDs and some broadcasts have
	// pixels that are wider or narrower). Rotation is how far the picture is
	// to be turned to stand upright, as phones record it: 0, 90, 180 or 270.
	Width    int `json:"width"`
	Height   int `json:"height"`
	SarNum   int `json:"sarNum"`
	SarDen   int `json:"sarDen"`
	Rotation int `json:"rotation,omitempty"`
	// FieldOrder says whether each frame is one picture ("progressive") or
	// two half-pictures taken at different moments, as on TV ("tt", "bb",
	// "tb", "bt"). Empty when the file does not say.
	FieldOrder string `json:"fieldOrder,omitempty"`
	// Matrix is the colour matrix, as ffprobe names it ("bt709",
	// "bt2020nc", ...). Empty when the file does not say.
	Matrix string `json:"matrix,omitempty"`
	// Format is the kind of file, as ffprobe names it.
	Format string `json:"format"`
	// KeyGapMs is the longest time between two key frames in the first
	// minute of the picture. It is only looked for in files where a jump
	// does not land on a key frame (see Indexed), and is 0 otherwise.
	KeyGapMs int64 `json:"keyGap,omitempty"`
}

// Indexed reports whether the file is of a kind that lists its own key
// frames (Matroska and MP4), so that ffmpeg, told to start somewhere, starts
// at the key frame before it. In other kinds (MPEG-TS above all) it starts
// wherever the time falls and shows nothing until a key frame comes by.
func (f PictureFacts) Indexed() bool {
	return strings.Contains(f.Format, "matroska") || strings.Contains(f.Format, "mp4")
}

// The lead-in: how long before a chunk the converter is started, so that
// by the chunk's first frame it is decoding the film properly.
const (
	leadMs    = 2000  // where a jump lands on a key frame
	maxLeadMs = 15000 // however far apart the key frames are
)

// LeadMs is how long before a chunk the converter is to be started for
// this film. Two seconds do where a jump lands on a key frame. Elsewhere it
// must have passed a key frame by the time the chunk begins, or the chunk
// would begin with whatever picture came first after it: so there the
// lead-in is the longest wait for a key frame that was seen, and a little
// more. A film whose key frames are further apart than the most allowed
// here shows a late picture for a moment after a jump, at the right time.
func (f PictureFacts) LeadMs() int64 {
	if f.Indexed() {
		return leadMs
	}
	if f.KeyGapMs <= 0 {
		return maxLeadMs // not known: as careful as can be
	}
	return min(max(leadMs, f.KeyGapMs+500), maxLeadMs)
}

// Lattice works out where this film is cut when its picture is converted
// (see the function Lattice), and fills in everything about the film's
// clock that the converter is told. The caller adds how large and how good
// the picture is to be (see Size).
func (f PictureFacts) Lattice() (Grid, Convert) {
	g, c := Lattice(f.StartMs, f.LastMs, f.EndMs, f.RateNum, f.RateDen)
	c.ContainerStartMs, c.LeadMs = f.ContainerStartMs, f.LeadMs()
	return g, c
}

// Size returns how many pixels wide and high the converted picture is, for
// a picture meant to be at most height pixels high (1080, 720, 480).
func (f PictureFacts) Size(height int) (w, h int) {
	sw, sh, sn, sd := f.Width, f.Height, f.SarNum, f.SarDen
	if f.Rotation == 90 || f.Rotation == 270 {
		// ffmpeg turns the picture upright before anything else is done to
		// it, so what is scaled is the turned picture.
		sw, sh, sn, sd = sh, sw, sd, sn
	}
	return fit(sw, sh, sn, sd, height)
}

// fit is the arithmetic of Size. The picture comes out
//
//   - with square pixels: a picture stored with wider or narrower ones keeps
//     its height and is given the width it is shown at;
//   - never enlarged: no higher than it is stored, and no wider than it is
//     shown (give or take the fraction of a pixel that rounding costs);
//   - inside the frame the height stands for, which is 16:9 (1920x1080,
//     1280x720, 854x480), so that a very wide film is not more work than the
//     height suggests;
//   - with an even width and height, which the encoder insists on.
func fit(w, h, sarNum, sarDen, height int) (int, int) {
	if w <= 0 || h <= 0 || height <= 0 {
		return 0, 0
	}
	if sarNum <= 0 || sarDen <= 0 {
		sarNum, sarDen = 1, 1
	}
	shown := float64(w) * float64(sarNum) / float64(sarDen) // the width with square pixels
	scale := min(1, float64(height)/float64(h), float64(height)*16/9/shown)
	even := func(v float64) int {
		n := 2 * int(math.Round(v*scale/2))
		if float64(n) >= v+1 {
			n -= 2 // an odd size is rounded down, not up
		}
		return max(n, 2)
	}
	return even(shown), even(float64(h))
}

// snapRate chooses the frame rate a picture is converted at from the two
// rates ffprobe gives for it: avg is the number of frames divided by the
// time they take, and base the rate the frames' times are counted in. It
// returns one of usualRates.
//
// A film with an even rate has the same value in both and is given exactly
// that. A film recorded unevenly (a phone that drops frames in poor light)
// averages below its real rate, so there the base rate is believed if it is
// a usual one. A rate that is neither is tried doubled, tripled and so on,
// and divided the same way, which keeps a 15 or 120 frames per second film
// evenly paced; failing all that it is the nearest usual rate.
func snapRate(avg, base float64) (num, den int64) {
	usual := func(v float64) (int64, int64, bool) {
		if !(v > 0) || math.IsInf(v, 0) {
			return 0, 0, false
		}
		best, off := usualRates[0], math.Inf(1)
		for _, r := range usualRates {
			if d := math.Abs(math.Log(v * float64(r[1]) / float64(r[0]))); d < off {
				best, off = r, d
			}
		}
		return best[0], best[1], off < 0.005
	}
	for _, v := range []float64{avg, base} {
		if n, d, ok := usual(v); ok {
			return n, d
		}
	}
	for _, v := range []float64{avg, base} {
		for _, k := range []float64{2, 3, 4, 0.5, 1.0 / 3, 0.25} {
			if n, d, ok := usual(v * k); ok {
				return n, d
			}
		}
	}
	for _, v := range []float64{avg, base} {
		if n, d, _ := usual(v); n > 0 {
			return n, d
		}
	}
	return 25, 1 // the file says nothing at all: any rate shows its frames
}

// What ffprobe prints, as far as it is read here. Every value is text.
type probedPicture struct {
	Packets []probedPacket `json:"packets"`
	Streams []struct {
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		Sar        string `json:"sample_aspect_ratio"`
		FieldOrder string `json:"field_order"`
		Matrix     string `json:"color_space"`
		BaseRate   string `json:"r_frame_rate"`
		AvgRate    string `json:"avg_frame_rate"`
		StartTime  string `json:"start_time"`
		Duration   string `json:"duration"`
		Tags       struct {
			Duration string `json:"DURATION"`
		} `json:"tags"`
		SideData []struct {
			Rotation *float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		Name      string `json:"format_name"`
		StartTime string `json:"start_time"`
		Duration  string `json:"duration"`
	} `json:"format"`
}

type probedPacket struct {
	Time  string `json:"pts_time"`
	Flags string `json:"flags"`
}

// millis reads a time in seconds, as ffprobe prints it, into milliseconds.
func millis(s string) (int64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.Abs(v) > 1e12 {
		return 0, false
	}
	return int64(math.Round(v * 1000)), true
}

// clockMillis reads a length written as hours:minutes:seconds, which is how
// a Matroska file notes the length of each of its tracks.
func clockMillis(s string) (int64, bool) {
	p := strings.Split(strings.TrimSpace(s), ":")
	if len(p) != 3 {
		return 0, false
	}
	h, err1 := strconv.ParseInt(p[0], 10, 64)
	m, err2 := strconv.ParseInt(p[1], 10, 64)
	sec, ok := millis(p[2])
	if err1 != nil || err2 != nil || !ok || h < 0 || h > 1e6 || m < 0 || m > 59 || sec < 0 {
		return 0, false
	}
	return (h*60+m)*60000 + sec, true
}

// ratio reads "a/b" or "a:b" as two numbers.
func ratio(s string) (a, b float64, ok bool) {
	x, y, found := strings.Cut(strings.TrimSpace(s), "/")
	if !found {
		x, y, found = strings.Cut(strings.TrimSpace(s), ":")
	}
	a, err1 := strconv.ParseFloat(x, 64)
	b, err2 := strconv.ParseFloat(y, 64)
	return a, b, found && err1 == nil && err2 == nil
}

// rate reads a frame rate as ffprobe prints it ("24000/1001"); 0 if it
// says none ("0/0").
func rate(s string) float64 {
	if a, b, ok := ratio(s); ok && a > 0 && b > 0 {
		return a / b
	}
	return 0
}

// parsePicture reads ffprobe's first answer about a file: everything but
// the last frame and the key frames. pictureEnd is where the picture track
// itself says it ends, or 0 if it does not say: a hint for where to look
// for the last frame.
func parsePicture(out []byte) (f PictureFacts, pictureEnd int64, err error) {
	var p probedPicture
	if err := json.Unmarshal(out, &p); err != nil {
		return f, 0, fmt.Errorf("ffprobe's answer could not be read: %w", err)
	}
	if len(p.Streams) == 0 {
		return f, 0, errors.New("the file has no picture")
	}
	s := p.Streams[0]
	if s.Width <= 0 || s.Height <= 0 || s.Width > 1<<15 || s.Height > 1<<15 {
		return f, 0, fmt.Errorf("the picture's size is given as %d by %d", s.Width, s.Height)
	}
	f.Width, f.Height, f.SarNum, f.SarDen, f.Format = s.Width, s.Height, 1, 1, p.Format.Name
	if a, b, ok := ratio(s.Sar); ok && a >= 1 && b >= 1 && a/b < 100 && b/a < 100 && a < 1<<20 && b < 1<<20 {
		f.SarNum, f.SarDen = int(a), int(b)
	}
	switch s.FieldOrder {
	case "progressive", "tt", "bb", "tb", "bt":
		f.FieldOrder = s.FieldOrder
	}
	if s.Matrix != "unknown" && len(s.Matrix) < 32 {
		f.Matrix = s.Matrix
	}
	for _, d := range s.SideData {
		if d.Rotation != nil && !math.IsNaN(*d.Rotation) && math.Abs(*d.Rotation) <= 360 {
			// ffprobe gives the turn as anything from -180 to 180.
			f.Rotation = ((int(math.Round(*d.Rotation/90))*90)%360 + 360) % 360
		}
	}
	f.RateNum, f.RateDen = snapRate(rate(s.AvgRate), rate(s.BaseRate))

	// The picture's start: as the file states it, or failing that the first
	// frame stored in it.
	start, ok := millis(s.StartTime)
	if !ok && len(p.Packets) > 0 {
		start, ok = millis(p.Packets[0].Time)
	}
	if !ok {
		return f, 0, errors.New("the file does not say when its picture starts")
	}
	f.StartMs = start
	f.ContainerStartMs, ok = millis(p.Format.StartTime)
	if !ok {
		f.ContainerStartMs = start
	}
	if d, ok := millis(p.Format.Duration); ok && d > 0 {
		f.EndMs = f.ContainerStartMs + d
	}
	if d, ok := millis(s.Duration); ok && d > 0 {
		pictureEnd = start + d
	} else if d, ok := clockMillis(s.Tags.Duration); ok && d > 0 {
		pictureEnd = d // counted from zero on the file's clock, not from the picture's start
	}
	return f, pictureEnd, nil
}

// parsePackets reads ffprobe's list of the picture's packets: when each is
// shown, in milliseconds, and whether it is a key frame.
func parsePackets(out []byte) (times []int64, keys []bool, err error) {
	var p probedPicture
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, nil, fmt.Errorf("ffprobe's answer could not be read: %w", err)
	}
	for _, k := range p.Packets {
		if t, ok := millis(k.Time); ok {
			times, keys = append(times, t), append(keys, strings.HasPrefix(k.Flags, "K"))
		}
	}
	return times, keys, nil
}

// keyGapWindowMs is how much of the start of a film is looked at for how
// far apart its key frames are.
const keyGapWindowMs = 60000

// keyGap returns the longest time between two key frames among the packets
// of the first minute after startMs, counting the wait for the first one
// and the time after the last one for which no key frame was seen.
func keyGap(startMs int64, times []int64, keys []bool) int64 {
	var gap int64
	prev, end := startMs, startMs
	for i, t := range times {
		if t < startMs || t > startMs+keyGapWindowMs {
			continue
		}
		end = max(end, t)
		if keys[i] && t > prev {
			gap, prev = max(gap, t-prev), t
		}
	}
	return max(gap, end-prev)
}

// tailMs is how much of the end of the picture is read to find its last
// frame. Frames are not stored in the order they are shown, so the last
// one stored is not always the last one shown; it is among the last few.
const tailMs = 8000

// ProbePicture asks ffprobe for the facts about a film's picture. It looks
// at the file twice: once for what the file says about itself, and once at
// its last few seconds for the last frame (and, where it matters, at its
// first minute for how far apart the key frames are). Neither look reads
// the film through.
func ProbePicture(ctx context.Context, ffprobe, path string) (PictureFacts, error) {
	if ffprobe == "" {
		return PictureFacts{}, errors.New("ffprobe is not available")
	}
	run := func(a ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, ffprobe, append(append([]string{"-v", "error", "-select_streams", "v:0"}, a...), "-of", "json", path)...)
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("ffprobe could not read the file: %w", err)
		}
		return out, nil
	}
	out, err := run("-read_intervals", "%+#1", "-show_entries",
		"packet=pts_time:stream=width,height,sample_aspect_ratio,field_order,color_space,r_frame_rate,avg_frame_rate,start_time,duration"+
			":stream_tags=DURATION:stream_side_data=rotation:format=format_name,start_time,duration")
	if err != nil {
		return PictureFacts{}, err
	}
	f, pictureEnd, err := parsePicture(out)
	if err != nil {
		return f, err
	}
	// Where the picture ends: where its own track says, if that is inside
	// the file, and otherwise where the file does.
	end := f.EndMs
	if pictureEnd > f.StartMs && (end == 0 || pictureEnd < end) {
		end = pictureEnd
	}
	// Look at the last few seconds before that, and on to the end of the
	// file in case the picture runs on. In a file that lists its key frames
	// that always finds the last frame: ffprobe starts at the last key frame
	// before the place it is sent to. In other files it starts at the place
	// itself, and finds nothing if the picture ended earlier (the track's
	// own account of its length was wrong, or it gave none and the sound
	// runs on after the picture). Then it is sent further back, twice at
	// most; each look reads more of the file, and is rarely needed.
	head := ""
	if !f.Indexed() {
		head = "%+" + strconv.Itoa(keyGapWindowMs/1000) + ","
	}
	to := int64(0) // where the look ends; 0 for the end of the file
	for _, back := range []int64{tailMs, 60000, 600000} {
		from := end - back
		// A short film, or one that does not say how long it is, is read
		// through.
		whole := end == 0 || from <= f.ContainerStartMs
		a := []string{"-show_entries", "packet=pts_time,flags"}
		if !whole {
			interval := head + clock(from)
			if to > 0 {
				interval += "%" + clock(to)
			}
			a = append(a, "-read_intervals", interval)
		}
		out, err := run(a...)
		if err != nil {
			// What the file says about itself is known; without its last
			// frame the picture is taken to run to the end of the file.
			return f, ctx.Err()
		}
		times, keys, err := parsePackets(out)
		if err != nil {
			return f, nil
		}
		if head != "" {
			f.KeyGapMs, head = keyGap(f.StartMs, times, keys), ""
		}
		for _, t := range times {
			// The first minute may be in the answer as well: its frames do
			// not say where the picture ends.
			if (whole || f.Indexed() || t >= from) && t > f.LastMs {
				f.LastMs = t
			}
		}
		if f.LastMs > 0 || whole {
			break
		}
		to = from
	}
	return f, nil
}

// clock writes a time in milliseconds as seconds, the way ffmpeg and
// ffprobe read one.
func clock(ms int64) string {
	sign := ""
	if ms < 0 {
		sign, ms = "-", -ms
	}
	return fmt.Sprintf("%s%d.%03d", sign, ms/1000, ms%1000)
}
