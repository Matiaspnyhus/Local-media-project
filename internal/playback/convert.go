package playback

import (
	"math"
	"strconv"
)

// This file holds what converting the picture adds to the decision: what
// the viewer can ask for, what this PC can do, the ladder of settings a
// picture is converted at, and how subtitles reach the viewer.
//
// A converted picture is always H.264 in normal colour. How large it is and
// how much effort goes into it is picked from five rungs, best first, and
// the rule is simple: the best rung this PC has been measured to keep up
// with. A film that arrives slower than it plays cannot be watched, so a
// rung measured below its bar is never picked, however good it would look.

// Want is what the viewer asks for in one play.
type Want struct {
	// Audio is the sound track, counted from 0; -1 for the file's own main
	// track (see BrowserAudio).
	Audio int `json:"audio"`
	// Subtitle counts into Media.Subtitles; -1 for none.
	Subtitle int `json:"subtitle"`
	// Styled says what to do with styled subtitles where the page cannot
	// draw them: Designed (also "") keeps their design by putting them into
	// the picture, which means converting it; Plain shows their words as
	// plain text and leaves the picture alone.
	Styled Styling `json:"styled"`
	// Lower asks for a lighter picture: the number of the best rung that
	// may be used (see Ladder). 0 means no wish. It comes from the viewer
	// ("lighter picture", see Lighter) and from nobody else: a step after
	// trouble asks through Limits.Lower.
	//
	// It is a wish, not a demand. A film that is converted anyway is made
	// at that rung or a lighter one. A film that would play as it is gets
	// converted for it where this PC can; where it cannot, the film plays
	// as it would have without the wish. Asking for lighter than the
	// lightest rung gives the lightest.
	Lower int `json:"lower"`
}

// Styling is a viewer's choice for styled subtitles (Want.Styled).
type Styling string

const (
	Designed Styling = "designed"
	Plain    Styling = "plain"
)

// Limits is what holds for one play besides the file and the device: what
// this PC can do, how fast it has been measured to do it, and what earlier
// trouble has taught. The zero value is a PC that cannot convert pictures.
type Limits struct {
	// Convert says this PC can convert pictures at all.
	Convert bool `json:"convert"`
	// ToneMap says its converter can turn HDR into normal colour, and
	// DoViTone that it can do so for Dolby Vision profile 5, which needs a
	// filter of its own (tonemapx).
	ToneMap  bool `json:"toneMap"`
	DoViTone bool `json:"doviTone"`
	// Encoder is what makes the H.264: "" or "x264" for the PC's
	// processor, or the name of a graphics-card encoder that passed the
	// owner's test. Speed must be the figures measured for this encoder.
	Encoder string `json:"encoder"`
	// Speed is how many seconds of film this PC converts in one second,
	// for each class of film and each rung; 0 means not measured.
	Speed Speeds `json:"speed"`
	// Busy is how many pictures are being converted already, each of which
	// takes its share of the PC.
	Busy int `json:"busy"`
	// Lower is the best rung a converted picture may use in this play; 0
	// for any. A step after trouble sets it (this PC did not keep up with
	// the rung before it, see StepDown). Unlike the viewer's own wish
	// (Want.Lower) it never has a film converted that would play as it
	// is, and it belongs to the play that took the step, not to later
	// ones.
	Lower int `json:"lower"`
	// MaxHeight caps how large a converted picture may be, in lines; 0 for
	// no cap. It comes from a lesson: this device had trouble showing the
	// converted picture at a larger size.
	MaxHeight int `json:"maxHeight"`
	// MaxRate caps how many bits a second may be sent; 0 for no cap. It
	// comes from a lesson: films arrived at this device too slowly. A film
	// that would play as it is, but spends more than this, is converted
	// to a lighter one where this PC can.
	MaxRate int `json:"maxRate"`
	// NoHDR comes from a lesson too: the viewer reported that HDR colours
	// look wrong on this device, so HDR pictures are converted to normal
	// colour for it where this PC can.
	NoHDR bool `json:"noHdr"`
	// Trouble lists the kinds of picture this device had trouble showing
	// when it was sent a film's own picture (lessons, see StepDown). A film
	// of the same format and bit depth, at that size or larger, is
	// converted for the device where this PC can. What the device itself
	// reports (Caps) is left as it reported it, so that the reason given
	// can say which of the two it was.
	Trouble []PictureKind `json:"trouble"`
}

// Class sorts films by how much work their picture is to convert. The
// speed table has one row for each.
type Class string

const (
	ClassHD    Class = "hd"     // up to 1080p, normal colour
	ClassHDR   Class = "hd-hdr" // up to 1080p, HDR
	Class4K    Class = "4k"     // larger than 1080p, normal colour
	Class4KHDR Class = "4k-hdr" // larger than 1080p, HDR
)

// Speeds is the speed table: for each class of film, the measured speed at
// each rung (rung 1 first), in seconds of film per second. 0 means not
// measured.
type Speeds map[Class][NumRungs]float64

// NumRungs is how many rungs the ladder has.
const NumRungs = 5

// Rung is one setting a picture can be converted at.
type Rung struct {
	// Height is the frame the picture is made to fit, in lines; the frame
	// is 16:9 wide. A picture that already fits keeps its size.
	Height int
	// Preset is the encoder's name for how much effort it spends.
	Preset string
	// MaxRate is the most bits a second the picture may spend. The
	// encoder's buffer is twice this.
	MaxRate int
	// HalveFast says a film with more than 30 pictures a second is shown
	// with every second one left out.
	HalveFast bool
}

// ladder is the rungs in order of picture quality. Rung 2 looks worse than
// rung 1 at the same size because the encoder is given less time; that is
// also why it is allowed more bits.
var ladder = [NumRungs]Rung{
	{Height: 1080, Preset: "veryfast", MaxRate: 10_000_000},
	{Height: 1080, Preset: "superfast", MaxRate: 12_000_000},
	{Height: 720, Preset: "veryfast", MaxRate: 5_000_000},
	{Height: 720, Preset: "superfast", MaxRate: 5_000_000, HalveFast: true},
	{Height: 480, Preset: "ultrafast", MaxRate: 2_500_000},
}

// Ladder returns the rungs, best picture first. Rung n of a plan is
// Ladder()[n-1].
func Ladder() [NumRungs]Rung { return ladder }

// A conversion has to run this much faster than the film plays before its
// rung is picked: 1.5 times, or 1.2 for 4K HDR, where no four-core PC has
// more to give. The margin pays for jumps, busy scenes and whatever else
// the PC is doing.
const (
	bar      = 1.5
	bar4KHDR = 1.2
)

// Video is what a picture is converted to.
type Video struct {
	// Rung is the setting, 1 to NumRungs (see Ladder).
	Rung int `json:"rung"`
	// Height is how many lines the converted picture has, as near as can
	// be told here: the converter works out the exact size, which also
	// depends on the shape of the film's pixels. A picture held upright
	// has more lines than its rung's frame, which it fits lying down.
	Height int `json:"height"`
	// Encoder is what makes the H.264: "x264" or a graphics-card encoder.
	Encoder string `json:"encoder"`
	// HDRToSDR says the film is HDR and is turned into normal colour.
	HDRToSDR bool `json:"hdrToSdr"`
	// Class is the row of the speed table this film belongs to.
	Class Class `json:"class"`
}

// SubtitleMode is how subtitles reach the viewer.
type SubtitleMode string

const (
	SubsNone  SubtitleMode = "none"  // no subtitles are shown
	SubsTrack SubtitleMode = "track" // as text, shown by the player
	SubsDraw  SubtitleMode = "draw"  // drawn over the film by the page
	SubsBurn  SubtitleMode = "burn"  // put into the picture by the PC
)

// SubtitleKind is what a subtitle is made of.
type SubtitleKind string

const (
	TextSubs    SubtitleKind = "text"    // words and times
	StyledSubs  SubtitleKind = "styled"  // words with their own fonts, colours and positions
	PictureSubs SubtitleKind = "picture" // pictures of words, as on discs
)

// Size sorts pictures by the smallest of the ladder's frames they fit.
type Size string

const (
	Size480  Size = "480p"
	Size720  Size = "720p"
	Size1080 Size = "1080p"
	Size4K   Size = "4k" // anything larger than 1080p
)

// PictureKind is a kind of picture as far as a device's trouble with it
// goes: a lesson is about a kind, never about a title.
type PictureKind struct {
	Codec    string `json:"codec"`
	BitDepth int    `json:"bitDepth"`
	Size     Size   `json:"size"` // "" where the size was not read
}

// What a viewer loses, in the words the page shows.
const (
	lostSurround = "Surround sound is played as stereo."
	lostDesign   = "The subtitles are shown as plain text, without their design."
	lostMotion   = "Every second picture is left out, so fast movement is less smooth."
)

// wanted is the subtitle the viewer asked for, nil for none.
func wanted(m Media, w Want) *Subtitle {
	if w.Subtitle < 0 || w.Subtitle >= len(m.Subtitles) {
		return nil
	}
	return &m.Subtitles[w.Subtitle]
}

// kindOf is a subtitle's kind; one that does not say is text, as every
// subtitle was before there were other kinds.
func kindOf(s *Subtitle) SubtitleKind {
	if s.Kind == "" {
		return TextSubs
	}
	return s.Kind
}

// showing says how a subtitle reaches the viewer on a device.
//
// Plain text is handed to the player as a track. Styled subtitles and ones
// stored as pictures have to be drawn: by the page where it can and where
// what it draws is still there in fullscreen, otherwise by the PC, into
// the picture itself. For styled ones the viewer may prefer plain words
// over a converted picture.
//
// A viewer who would rather have the page draw on a device where it
// cannot go fullscreen, and do without subtitles there, is served by
// passing that device in with PageFullscreen set.
func showing(sub *Subtitle, c *Caps, w Want) SubtitleMode {
	if sub == nil {
		return SubsNone
	}
	switch kindOf(sub) {
	case StyledSubs:
		if c.Draw.Styled && c.PageFullscreen {
			return SubsDraw
		}
		if w.Styled != Plain {
			return SubsBurn
		}
	case PictureSubs:
		if c.Draw.Picture && c.PageFullscreen {
			return SubsDraw
		}
		return SubsBurn
	}
	return SubsTrack
}

// burnReason says why subtitles are put into the picture.
func burnReason(sub *Subtitle, c *Caps) string {
	what, can := "have their own design", c.Draw.Styled
	if kindOf(sub) == PictureSubs {
		what, can = "are stored as pictures", c.Draw.Picture
	}
	if can {
		// The iPhone's case: the page could draw them, but not where the
		// film is shown once it goes fullscreen.
		return "The subtitles you chose " + what + ". This device can draw them only while the film plays inside the page, not in fullscreen, so they have to be put into the picture."
	}
	return "The subtitles you chose " + what + ", which this device cannot draw over the film, so they have to be put into the picture."
}

// blind finishes a plan made without knowing the file or the device. Text
// can always be tried as a track. Whether anything else could be drawn or
// would have to be put into the picture cannot be told yet.
func blind(p Plan, sub *Subtitle, w Want) Plan {
	p.Reasons, p.Lost, p.Subtitles = []string{}, []string{}, SubsNone
	if sub == nil {
		return p
	}
	switch kindOf(sub) {
	case PictureSubs:
		p.Lost = append(p.Lost, "The subtitles you chose are not shown: they are stored as pictures, and the app cannot tell yet whether this device can draw them.")
		return p
	case StyledSubs:
		if w.Styled != Plain {
			p.Lost = append(p.Lost, lostDesign)
		}
	}
	p.Subtitles, p.SubtitleKind = SubsTrack, kindOf(sub)
	return p
}

// advised converts a film that would play as it is (p) when there is
// advice to: a lesson (the device had trouble showing this kind of picture,
// the connection is too slow for the original, the device shows HDR
// wrongly) or the viewer's wish for a lighter picture. Advice is not law.
// Where this PC cannot follow it, the film plays as it would have without
// it, which beats not playing.
func advised(p Plan, other []string, m Media, a *Track, c *Caps, w Want, l Limits) Plan {
	v := m.Video[0]
	var why []string
	if troubled(l.Trouble, v) {
		why = append(why, "Its picture is "+named(v)+", which this device has had trouble showing.")
	}
	if l.MaxRate > 0 && rateOf(m) > l.MaxRate {
		why = append(why, "Films have been reaching this device too slowly, so a lighter picture is made for it.")
	}
	if l.NoHDR && isHDR(v) {
		why = append(why, "HDR colours were reported to look wrong on this device, so the picture is converted to normal colour.")
	}
	if w.Lower > 0 && len(why) == 0 {
		// Said only where nothing else is: beside another reason the wish
		// only picks the rung, and what the viewer sees of that is the
		// smaller picture named among what is lost.
		why = append(why, "You asked for a lighter picture.")
	}
	if len(why) == 0 || !l.Convert {
		return p
	}
	if q := converted(p, append(why, other...), m, a, c, w, l); q.Playable != No {
		return q
	}
	return p
}

// troubled says a device has had trouble showing a picture like v: a
// lesson names the same format and bit depth at this size or a smaller
// one. A device that could not show a format at 720p is not sent it at
// 1080p either, while one that failed at 4K is still sent 1080p. A lesson
// from a film whose size was not read speaks for every size.
func troubled(kinds []PictureKind, v Track) bool {
	own := pictureOf(v)
	for _, k := range kinds {
		if k.Codec == own.Codec && (k.BitDepth > 8) == (own.BitDepth > 8) && (k.Size == "" || sizeRank(own.Size) >= sizeRank(k.Size)) {
			return true
		}
	}
	return false
}

// sizeRank puts the sizes in order. A picture whose size was not read
// counts as 1080p here, as it does everywhere else: never as larger.
func sizeRank(s Size) int {
	switch s {
	case Size480:
		return 1
	case Size720:
		return 2
	case Size4K:
		return 4
	}
	return 3
}

// named is a picture's size and format in the words people know them by,
// such as "4K 10-bit HEVC (H.265)".
func named(v Track) string {
	if size := sizeWord(v.Width, v.Height); size != "" {
		return size + " " + VideoName(v)
	}
	return VideoName(v)
}

// rateOf is how many bits a second a file spends, as far as is known.
// Where the file does not say, the size of its picture stands in: it is
// taken to spend what the ladder allows a picture of that size, and a
// picture larger than 1080p, or one whose size is not known either, to
// spend more than any cap. That keeps a slow connection from having a
// small film converted that was light all along.
func rateOf(m Media) int {
	if m.BitRate > 0 {
		return m.BitRate
	}
	v := m.Video[0]
	switch sizeOf(v.Width, v.Height) {
	case Size480:
		return ladder[NumRungs-1].MaxRate
	case Size720:
		return ladder[2].MaxRate
	case Size1080:
		return ladder[0].MaxRate
	}
	return math.MaxInt
}

// Lighter is the rung to ask for (Want.Lower) when the viewer wants a
// lighter picture than plan p of film m gives them: the next rung down
// from a converted picture; for a film sent with its own picture, the best
// rung that is really lighter than the film, which is one that makes the
// picture smaller or may spend fewer bits a second than the file does. 0
// means nothing lighter is made.
//
// Whether this PC keeps up with that rung is for DecideFor to say: the
// plan that comes back may be the same as p, and the viewer is then told
// that this is the lightest picture there is.
func Lighter(m Media, p Plan) int {
	if p.Method == Convert {
		if p.Video.Rung >= 1 && p.Video.Rung < NumRungs {
			return p.Video.Rung + 1
		}
		return 0
	}
	if p.Playable == No || len(m.Video) == 0 {
		return 0
	}
	v := m.Video[0]
	for n, r := range ladder {
		if !fits(v.Width, v.Height, r.Height) || rateOf(m) > r.MaxRate {
			return n + 1
		}
	}
	return 0
}

// converted is the plan that converts the picture, for the reasons given,
// or the answer that says why this PC cannot. p carries the sound track
// and the subtitles already settled.
//
// What comes out is 8-bit H.264 in chunks. Whether the device plays H.264
// is not asked: everything that takes chunks does, and a lesson about
// H.264 (Limits.Trouble) is about a film's own H.264, which is exactly
// what converting replaces.
func converted(p Plan, why []string, m Media, a *Track, c *Caps, w Want, l Limits) Plan {
	v := m.Video[0]
	if !c.HLS {
		return refuse(p, append(why, "This browser cannot play converted video."))
	}
	hdr := isHDR(v)
	switch {
	case v.DoVi == 5 && !l.DoViTone:
		return cannot(p, append(why, "This PC's converter cannot turn Dolby Vision of this kind into normal colour, so it cannot convert this film."))
	case v.DoVi != 5 && hdr && !l.ToneMap:
		return cannot(p, append(why, "This PC's converter cannot turn HDR into normal colour, so it cannot convert this film."))
	}
	// Sound that the device plays is passed on beside the new picture, so
	// surround survives. AAC is passed on only out of the files that store
	// it the way the chunks do (MKV, MP4 and MP4's sibling MOV); out of
	// anything else it would have to be rewritten on the way, and
	// converting it is the safe way.
	keep := a == nil || (c.Audio[a.Codec] && (a.Codec == "ac3" || a.Codec == "eac3" ||
		a.Codec == "aac" && (m.Container == "matroska" || m.Container == "mp4" || m.Container == "mov")))
	class := classOf(v)
	rung, best := pickRung(v, w, l)
	if rung == 0 && (keep || c.Audio["aac"]) {
		no := cannot(p, append(why, tooSlow(best, barFor(class), l.Busy)))
		no.TooSlow = true
		return no
	}
	if !keep && !c.Audio["aac"] {
		return refuse(p, append(why, "This browser reports no sound format the sound could be converted to."))
	}
	r := ladder[rung-1]
	width, height := fit(v.Width, v.Height, r.Height)
	p.Playable, p.Method, p.Reasons, p.AudioCopy = Yes, Convert, why, keep
	p.Video = Video{Rung: rung, Height: height, Encoder: encoderName(l.Encoder), HDRToSDR: hdr, Class: class}
	p.Picture = PictureKind{Codec: "h264", BitDepth: 8, Size: sizeOf(width, height)}
	p.Lost = []string{}
	if v.Height > 0 && height < v.Height {
		p.Lost = append(p.Lost, sizeWord(v.Width, v.Height)+" picture shown as "+strconv.Itoa(r.Height)+"p.")
	}
	switch {
	case hdr && v.DoVi > 0:
		p.Lost = append(p.Lost, "Dolby Vision shown as normal colour.")
	case hdr:
		p.Lost = append(p.Lost, "HDR shown as normal colour.")
	}
	if r.HalveFast && v.FrameRate > 30 {
		p.Lost = append(p.Lost, lostMotion)
	}
	if !keep && a.Channels > 2 {
		p.Lost = append(p.Lost, lostSurround)
	}
	return p
}

// pickRung picks the setting a film's picture is converted at.
//
// A rung keeps up when this PC converts at it fast enough, sharing itself
// with what it is converting already. A lighter rung is never slower than
// a heavier one, so a rung counts as no faster than any lighter rung was
// measured: a film that showed a light rung to be slow speaks for the
// heavier ones too, whether they were measured or not. A rung measured
// below its bar is never picked.
//
// A rung that was never measured, with nothing lighter speaking against
// it, is trusted from a safe starting point down: rung 1 for a film up to
// 1080p in normal colour, rung 3 for 4K and for HDR, and one rung further
// down for each picture this PC is converting already.
//
// Of the rungs that keep up, the best is picked that is no better than
// Want.Lower and Limits.Lower ask, no larger than Limits.MaxHeight and
// spends no more bits than Limits.MaxRate. None of these rules out the
// last rung: there is nothing lighter to fall back to, and a film that is
// larger or heavier than was wished for beats no film.
//
// It returns 0 only when no rung keeps up at all, which is when the
// lightest one was measured too slow, with that rung's speed for the
// answer to quote.
func pickRung(v Track, w Want, l Limits) (rung int, best float64) {
	class := classOf(v)
	need := barFor(class)
	speeds := l.Speed[class]
	share := 1.0
	start := 1
	if class != ClassHD {
		start = 3
	}
	if l.Busy > 0 {
		share = float64(l.Busy + 1)
		start = min(start+min(l.Busy, NumRungs), NumRungs)
	}
	var keeps [NumRungs + 1]bool
	slowest := math.Inf(1) // the slowest speed measured at this rung or a lighter one
	for n := NumRungs; n >= 1; n-- {
		speed := speeds[n-1]
		measured := speed > 0
		if measured {
			slowest = math.Min(slowest, speed)
		}
		keeps[n] = (measured || n >= start) && slowest/share >= need
	}
	if !keeps[NumRungs] {
		return 0, speeds[NumRungs-1] / share
	}
	for n := 1; n < NumRungs; n++ {
		r := ladder[n-1]
		if !keeps[n] || n < w.Lower || n < l.Lower {
			continue
		}
		if l.MaxRate > 0 && r.MaxRate > l.MaxRate {
			continue
		}
		if width, height := fit(v.Width, v.Height, r.Height); l.MaxHeight > 0 && !fits(width, height, l.MaxHeight) {
			continue
		}
		return n, 0
	}
	return NumRungs, 0
}

// tooSlow says that no setting keeps up, quoting what was measured. It
// also says what the figure is held against: a reader who sees "1.4
// seconds of film a second" would otherwise take that for fast enough.
func tooSlow(best, need float64, busy int) string {
	s := "This PC cannot convert this film fast enough to watch"
	if best > 0 {
		s += ": at best it manages " + figure(best) + " seconds of film a second"
	}
	switch {
	case busy == 1:
		s += " while it is converting for another screen as well"
	case busy > 1:
		s += " while it is converting for " + strconv.Itoa(busy) + " other screens as well"
	}
	if best > 0 {
		s += ", and the app asks for " + figure(need) + " so that the film does not stop when you jump or the PC has other work"
	}
	return s + "."
}

// figure writes a speed with one decimal, cut off rather than rounded, so
// that a speed just under its bar never reads as having reached it.
func figure(x float64) string {
	tenths := int(x * 10)
	// Multiplying by ten can land a hair to either side of a whole number.
	for float64(tenths+1)/10 <= x {
		tenths++
	}
	for tenths > 0 && float64(tenths)/10 > x {
		tenths--
	}
	return strconv.Itoa(tenths/10) + "." + strconv.Itoa(tenths%10)
}

func barFor(c Class) float64 {
	if c == Class4KHDR {
		return bar4KHDR
	}
	return bar
}

func encoderName(e string) string {
	if e == "" {
		return "x264"
	}
	return e
}

// isHDR says a picture has to be turned into normal colour when it is
// converted. Dolby Vision profile 5 always has to, whatever else the file
// says about itself.
func isHDR(v Track) bool {
	return v.Transfer == PQ || v.Transfer == HLG || v.DoVi == 5
}

func classOf(v Track) Class {
	large, hdr := sizeOf(v.Width, v.Height) == Size4K, isHDR(v)
	switch {
	case large && hdr:
		return Class4KHDR
	case large:
		return Class4K
	case hdr:
		return ClassHDR
	}
	return ClassHD
}

func pictureOf(v Track) PictureKind {
	depth := v.BitDepth
	if depth <= 0 {
		depth = 8
	}
	return PictureKind{Codec: v.Codec, BitDepth: depth, Size: sizeOf(v.Width, v.Height)}
}

// wide is the width of the 16:9 frame that is lines tall, rounded up to an
// even number (854 for 480).
func wide(lines int) int { return (lines*16/9 + 1) &^ 1 }

// lying turns a picture that is taller than it is wide (a phone held
// upright) on its side, and says whether it did. Sizes here are about how
// much picture there is, and 1080x1920 is as much as 1920x1080: it must
// not count as larger than 1080p, nor be shrunk to fit a frame it would
// fill lying down. A width of 0 is not known and is left alone.
func lying(width, height int) (w, h int, upright bool) {
	if width > 0 && height > width {
		return height, width, true
	}
	return width, height, false
}

// fits says a picture is no larger than the frame that is lines tall.
// Films wider than 16:9 are measured by their width: 1920x800 is a 1080p
// picture although it has 800 lines.
func fits(width, height, lines int) bool {
	width, height, _ = lying(width, height)
	return height <= lines && width <= wide(lines)
}

// fit is the size a picture has once it is made to fit the frame that is
// lines tall: shrunk if it has to be, never enlarged. A picture of unknown
// size is taken to fill the frame.
func fit(width, height, lines int) (int, int) {
	if height <= 0 {
		return 0, lines
	}
	w, h, upright := lying(width, height)
	switch {
	case fits(w, h, lines):
	case w*9 <= h*16:
		// No wider than the frame's shape: the height decides.
		w, h = (w*lines/h)&^1, lines
	default:
		w, h = wide(lines), (h*wide(lines)/w)&^1
	}
	if upright {
		return h, w
	}
	return w, h
}

// sizeOf sorts a picture by the smallest frame it fits; "" for unknown.
// A few lines over 1080 (1088, as some files store 1080p) still count as
// 1080p.
func sizeOf(width, height int) Size {
	if height <= 0 {
		return ""
	}
	width, height, _ = lying(width, height)
	switch {
	case height > 1088 || width > 1920:
		return Size4K
	case !fits(width, height, 720):
		return Size1080
	case !fits(width, height, 480):
		return Size720
	}
	return Size480
}

// sizeWord names a picture's size the way people know it.
func sizeWord(width, height int) string {
	s := sizeOf(width, height)
	width, height, _ = lying(width, height)
	switch {
	case s == Size4K && (height > 1440 || width > 2560):
		return "4K"
	case s == Size4K:
		return "1440p"
	}
	return string(s)
}
