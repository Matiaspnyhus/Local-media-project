package playback

// This file says what to do when a film that is playing runs into trouble:
// what to change so that it plays, what to remember so that the next film
// does not run into the same trouble, and what to tell the viewer. Like
// the rest of the package it only decides. Keeping what was learned, and
// for how long, is the server's work.
//
// Replan is the whole of it in one call. StepDown and Step.Apply are its
// two halves: what trouble teaches, and how a lesson changes what is
// planned with. The server needs Apply by itself when it plans a later
// film for a device with remembered lessons.

// Evidence is what went wrong, as the page or the converter saw it.
type Evidence string

const (
	// DecodeTrouble: the device could not show the picture it was sent.
	// Its player gave up on it, or left out too many pictures to watch.
	DecodeTrouble Evidence = "decode"
	// Starved: the player ran out of film and had to wait for more.
	Starved Evidence = "starved"
	// RendererFailed: the page could not draw the subtitles.
	RendererFailed Evidence = "renderer"
)

// Lesson says what kind of thing trouble has taught.
type Lesson string

const (
	LessonDevice  Lesson = "device"  // this device has trouble showing a kind of picture
	LessonPC      Lesson = "pc"      // this PC converts slower than was thought
	LessonNetwork Lesson = "network" // films reach this device too slowly
	LessonDraw    Lesson = "draw"    // this device's page cannot draw subtitles
)

// Step is what trouble has taught. Only the part that Lesson names is
// filled in.
//
// A step carries no sentence for the viewer. What to tell them depends on
// what the lesson leads to for the film at hand, which may be a lighter
// picture, the same film as before, or nothing that can be done; Replan
// knows, and says it in Outcome.Notice.
type Step struct {
	// Lesson is what was learned. "" means nothing was: no step is taken
	// and the film is left as it is.
	Lesson Lesson `json:"lesson"`

	Device  DeviceLesson  `json:"device"`
	PC      SpeedLesson   `json:"pc"`
	Network NetworkLesson `json:"network"`
	// Draw names the drawing that failed and is switched off.
	Draw Drawing `json:"draw"`

	// Lower is the best rung to use when this film is started again
	// (Limits.Lower); 0 for any. It belongs to this play only and is not
	// part of what is remembered.
	Lower int `json:"lower"`
}

// DeviceLesson is a kind of picture a device had trouble showing, and what
// follows from it.
type DeviceLesson struct {
	// Picture is the kind that gave trouble: its format, bit depth and
	// size. When it was a film's own picture (MaxHeight is 0), films of
	// that format and bit depth, at that size or larger, are converted for
	// the device from then on (Limits.Trouble).
	Picture PictureKind `json:"picture"`
	// MaxHeight is set when the trouble was with a picture this PC
	// converted, which is as easy to show as a picture gets: the device
	// is sent nothing larger than this from then on (Limits.MaxHeight).
	MaxHeight int `json:"maxHeight"`
}

// SpeedLesson is a speed this PC was seen to convert at while a film
// played: seconds of film per second, for one row and rung of the speed
// table and one encoder. The table should be lowered to it, never raised,
// and only when this was the only picture being converted: with others
// running, each gets a share and the figure says little about the PC.
type SpeedLesson struct {
	Class   Class   `json:"class"`
	Rung    int     `json:"rung"`
	Encoder string  `json:"encoder"`
	Speed   float64 `json:"speed"`
}

// NetworkLesson caps what is sent to a device whose connection could not
// keep up, for a while: connections get better again.
type NetworkLesson struct {
	MaxRate int `json:"maxRate"` // bits a second (Limits.MaxRate)
	Hours   int `json:"hours"`   // how long the lesson holds
}

const (
	// maxSteps is how many steps one play may take. After two the trouble
	// is not one a lighter picture cures, and the viewer is better served
	// by being told than by a third restart.
	maxSteps = 2
	// slowPace: a conversion that ran below this for three chunks was
	// making less film than was being watched. It sits a little under 1
	// because a figure taken over three chunks wobbles.
	slowPace = 0.95
	// How long a connection that starved a player is held to its cap.
	networkHours = 24
)

// What a connection that starved a player is trusted with: first what the
// 720p rungs may spend, so that one stall costs a 1080p picture one size
// and not two; and when the film starves at that as well, what the 480p
// rung may spend. They are read off the ladder so that a cap always has a
// rung that meets it.
var (
	firstRate  = ladder[2].MaxRate
	secondRate = ladder[NumRungs-1].MaxRate
)

// StepDown says what trouble in a film that is playing teaches. p is the
// plan being played; pace is how fast this PC has been converting it, in
// seconds of film per second over the last three chunks (0 when not known,
// or when nothing is being converted); steps is how many steps this play
// has taken already.
//
//   - The device cannot show the picture: it is not sent that kind of
//     picture again. For a film's own picture that means converting it;
//     for a converted picture, a smaller one.
//   - The player ran out of film while the conversion was not keeping up:
//     this PC is slower than was thought. The speed table is lowered and
//     the next lighter rung is asked for.
//   - The player ran out of film although the PC kept up: the connection
//     is the slow part, and what is sent to this device is capped.
//   - The page could not draw the subtitles: drawing is switched off for
//     this device, so they are put into the picture or shown as text.
//
// There are at most two steps in one play. What a step leads to for this
// film is not known here; see Replan.
func StepDown(p Plan, e Evidence, pace float64, steps int) Step {
	if steps >= maxSteps || p.Playable == No {
		return Step{}
	}
	switch e {
	case DecodeTrouble:
		if p.Method == Convert {
			// The device struggles with the easiest kind of picture there
			// is, at this size. Only a smaller one can help.
			d := DeviceLesson{Picture: p.Picture}
			switch p.Picture.Size {
			case Size1080:
				d.MaxHeight = 720
			case Size720:
				d.MaxHeight = 480
			default:
				return Step{} // nothing smaller is made
			}
			return Step{Lesson: LessonDevice, Device: d}
		}
		if p.Picture.Codec == "" {
			return Step{} // the file was never inspected: there is no kind to name
		}
		return Step{Lesson: LessonDevice, Device: DeviceLesson{Picture: p.Picture}}

	case Starved:
		slow := pace > 0 && pace < slowPace
		if p.Method != Convert {
			if slow {
				// Nothing is converted, and still the film was not ready
				// in time: the disk it is read from is the slow part.
				// Converting would only add to the wait.
				return Step{}
			}
			return network(firstRate)
		}
		if slow {
			return Step{Lesson: LessonPC, Lower: min(p.Video.Rung+1, NumRungs),
				PC: SpeedLesson{Class: p.Video.Class, Rung: p.Video.Rung, Encoder: p.Video.Encoder, Speed: pace}}
		}
		sent := firstRate + 1 // a plan that names no rung: take it for a heavy one
		if p.Video.Rung >= 1 && p.Video.Rung <= NumRungs {
			sent = ladder[p.Video.Rung-1].MaxRate
		}
		switch {
		case sent <= secondRate:
			return Step{} // already as light as a picture is made
		case sent <= firstRate:
			return network(secondRate)
		}
		return network(firstRate)

	case RendererFailed:
		if p.Subtitles != SubsDraw {
			return Step{}
		}
		off := Drawing{Styled: p.SubtitleKind == StyledSubs, Picture: p.SubtitleKind == PictureSubs}
		if !off.Styled && !off.Picture {
			off = Drawing{Styled: true, Picture: true}
		}
		return Step{Lesson: LessonDraw, Draw: off}
	}
	return Step{}
}

func network(rate int) Step {
	return Step{Lesson: LessonNetwork, Network: NetworkLesson{MaxRate: rate, Hours: networkHours}}
}

// Apply gives what to plan with once a lesson has been learned: what the
// device plays, what the viewer wants and the limits, each with the lesson
// worked in. Nothing that is passed in is changed.
//
// It serves two uses: planning the film again right after the step (Replan
// does that), and planning a later film for a device with a remembered
// lesson. For the second, clear the step's Lower first: it belongs to the
// play that took the step.
//
// What the viewer wants comes back as it went in. It is passed through so
// that the three travel together.
//
// Everything Apply does takes something away, never adds. A lesson about
// a device or a connection still cannot refuse a film that would play
// without it: such a lesson is advice (see advised), and a cap on a
// converted picture never rules out the last rung. Only a speed this PC
// was seen at can: a film that must be converted, on a PC now known to be
// too slow for it even at the lightest rung, is answered "too slow".
//
// What Apply does not promise is a change. Where this PC cannot convert,
// or the film is light already, the plan made afterwards is the very same
// one.
func (s Step) Apply(c *Caps, w Want, l Limits) (*Caps, Want, Limits) {
	switch s.Lesson {
	case LessonDevice:
		switch {
		case s.Device.MaxHeight > 0:
			l.MaxHeight = capped(l.MaxHeight, s.Device.MaxHeight)
		case s.Device.Picture.Codec != "":
			l.Trouble = with(l.Trouble, s.Device.Picture)
		}
	case LessonPC:
		if encoderName(l.Encoder) == encoderName(s.PC.Encoder) {
			l.Speed = l.Speed.lowered(s.PC.Class, s.PC.Rung, s.PC.Speed)
		}
		if s.Lower > l.Lower {
			l.Lower = s.Lower
		}
	case LessonNetwork:
		l.MaxRate = capped(l.MaxRate, s.Network.MaxRate)
	case LessonDraw:
		if c != nil {
			trimmed := *c
			trimmed.Draw.Styled = c.Draw.Styled && !s.Draw.Styled
			trimmed.Draw.Picture = c.Draw.Picture && !s.Draw.Picture
			c = &trimmed
		}
	}
	return c, w, l
}

// with is a copy of the list with one more kind on it, unless it is there
// already. A copy, because the list belongs to whoever passed it in.
func with(kinds []PictureKind, k PictureKind) []PictureKind {
	for _, have := range kinds {
		if have == k {
			return kinds
		}
	}
	out := make([]PictureKind, 0, len(kinds)+1)
	return append(append(out, kinds...), k)
}

// capped is the lower of two caps, where 0 means none.
func capped(old, lesson int) int {
	if lesson > 0 && (old <= 0 || lesson < old) {
		return lesson
	}
	return old
}

// lowered is a copy of the table with one figure lowered to speed. A
// figure is never raised: what a real film showed can only be bad news.
func (t Speeds) lowered(class Class, rung int, speed float64) Speeds {
	if rung < 1 || rung > NumRungs || !(speed > 0) {
		return t
	}
	row := t[class]
	if row[rung-1] > 0 && row[rung-1] <= speed {
		return t
	}
	out := make(Speeds, len(t)+1)
	for k, v := range t {
		out[k] = v
	}
	row[rung-1] = speed
	out[class] = row
	return out
}

// Outcome is what trouble in a film that is playing leads to.
type Outcome struct {
	// Step is what was learned, for the server to remember (see
	// Step.Apply). Its Lesson is "" when nothing was.
	Step Step `json:"step"`
	// Changed says the film is to be started again where it was, as Plan
	// says, and that this counts as one of the play's steps. When it is
	// false nothing better can be done: the film is left playing as it
	// is, and nothing is counted.
	Changed bool `json:"changed"`
	// Plan is how the film plays from now on: the new plan when Changed,
	// otherwise the one it has been playing with.
	Plan Plan `json:"plan"`
	// Caps, Want and Limits are what to plan with for the rest of this
	// play, when the viewer changes the sound track, say, or trouble
	// comes again: what was passed in, with the lesson worked in wherever
	// the film still plays with it.
	Caps   *Caps  `json:"caps"`
	Want   Want   `json:"want"`
	Limits Limits `json:"limits"`
	// Notice tells the viewer in one sentence what went wrong and what
	// was done about it, or that nothing could be done. It is written
	// from what really changed between the two plans. "" when no lesson
	// was learned.
	Notice string `json:"notice"`
}

// Replan says what happens to a film that ran into trouble while playing.
// m, c, w and l are what the film was planned with (DecideFor); e, pace and
// steps are as for StepDown.
//
// It learns the lesson (StepDown), works it in (Step.Apply), plans the
// film again and compares the two plans:
//
//   - The plan changes and the film still plays: Changed, with the new
//     plan and a sentence that says what is different now.
//   - The plan stays the same (this PC cannot convert, the film is as
//     light as it gets, the device has not said what it plays): the film
//     is left alone and the sentence says that nothing was changed. The
//     lesson is still handed out: it may matter for the next film.
//   - The film would not play at all any more. That happens in one case
//     only: this PC turned out too slow at the lightest rung it had left,
//     for a film that must be converted. The film is left playing, however
//     badly, the viewer is told why, and the speed that was seen is handed
//     out so that the next play is answered honestly from the start.
//
// Two things it does beyond one step. When the first cap on a slow
// connection changes nothing because the film is lighter than that
// already, it tries the second. And when subtitles stored as pictures can
// neither be drawn nor be put into the picture, the film goes on without
// them (Want.Subtitle is -1 in the outcome).
//
// The picture after a step is never better than the one before, with one
// exception: a picture that did not have to be converted (it was, on a
// lesson's advice, for the viewer's wish for a lighter one, or to carry
// the design of styled subtitles) goes back to the film's own when this PC
// turns out too slow to convert it. That holds whatever is in the speed
// table, measured in full, in part or not at all: pickRung never takes a
// rung for faster than a lighter one was measured.
func Replan(m Media, c *Caps, w Want, l Limits, e Evidence, pace float64, steps int) Outcome {
	p := DecideFor(m, c, w, l)
	stay := Outcome{Plan: p, Caps: c, Want: w, Limits: l}
	s := StepDown(p, e, pace, steps)
	if s.Lesson == "" {
		return stay
	}
	c2, w2, l2 := s.Apply(c, w, l)
	q := DecideFor(m, c2, w2, l2)
	if s.Lesson == LessonNetwork && s.Network.MaxRate > secondRate && unchanged(p, q) {
		// The film is lighter than the first cap already. The second one
		// is kept only if it gets this film a lighter picture; a film
		// that is lighter than both teaches nothing about that cap.
		t := network(secondRate)
		c3, w3, l3 := t.Apply(c, w, l)
		if r := DecideFor(m, c3, w3, l3); r.Playable != No && !unchanged(p, r) {
			s, c2, w2, l2, q = t, c3, w3, l3, r
		}
	}
	if q.Playable == No && s.Lesson == LessonDraw {
		// The subtitles can neither be drawn nor be put into the picture.
		// The film itself was playing: it goes on without them.
		w2.Subtitle = -1
		q = DecideFor(m, c2, w2, l2)
	}
	stay.Step = s
	switch {
	case q.Playable == No:
		stay.Notice = stuck(s, p)
		return stay
	case unchanged(p, q):
		return Outcome{Step: s, Plan: q, Caps: c2, Want: w2, Limits: l2, Notice: stuck(s, p)}
	}
	return Outcome{Step: s, Changed: true, Plan: q, Caps: c2, Want: w2, Limits: l2, Notice: notice(s, p, q)}
}

// unchanged says two plans send the viewer the same thing in the same way,
// so that going from one to the other would be a restart for nothing.
func unchanged(p, q Plan) bool {
	return p.Playable == q.Playable && p.Method == q.Method && p.Video == q.Video &&
		p.AudioTrack == q.AudioTrack && p.AudioCopy == q.AudioCopy && p.Subtitles == q.Subtitles
}

// stuck is the sentence for trouble that nothing could be done about.
func stuck(s Step, p Plan) string {
	switch s.Lesson {
	case LessonDevice:
		if p.DeviceUnknown {
			return "This device had trouble showing the picture, but it has not told the app what it can play, so nothing was changed; reloading the page makes it say so again."
		}
		return "This device had trouble showing the picture, but nothing easier to show can be made of this film here, so nothing was changed."
	case LessonPC:
		return "This PC could not convert the film fast enough, but it cannot make a lighter picture than this one, so nothing was changed."
	case LessonNetwork:
		return "The film was reaching this device too slowly, but nothing lighter can be made of it here, so nothing was changed."
	}
	return ""
}

// notice is the sentence for a step that changed the plan from p to q: what
// went wrong, then what is different now.
func notice(s Step, p, q Plan) string {
	var cause string
	switch s.Lesson {
	case LessonDevice:
		cause = "This device had trouble showing the picture"
	case LessonPC:
		cause = "This PC could not convert the film fast enough"
	case LessonNetwork:
		cause = "The film was reaching this device too slowly"
	case LessonDraw:
		cause = "This device could not draw the subtitles over the film"
		switch q.Subtitles {
		case SubsBurn:
			return cause + ", so the PC now puts them into the picture."
		case SubsTrack:
			for _, lost := range q.Lost {
				if lost == lostDesign {
					return cause + ", so they are now shown as plain text, without their design."
				}
			}
			return cause + ", so they are now shown as plain text."
		}
		return cause + ", and they cannot be put into the picture here, so the film now plays without them."
	}
	smaller := p.Picture.Size != "" && q.Picture.Size != "" && sizeRank(q.Picture.Size) < sizeRank(p.Picture.Size)
	var effect string
	switch {
	case q.Method == Convert && p.Method != Convert && smaller:
		effect = "so the PC now converts the picture to a smaller one (" + string(q.Picture.Size) + ")"
	case q.Method == Convert && p.Method != Convert && s.Lesson == LessonNetwork:
		effect = "so the PC now converts the picture to a lighter one"
	case q.Method == Convert && p.Method != Convert:
		effect = "so the PC now converts the picture to a kind every device plays"
	case q.Method == Convert && smaller:
		effect = "so the picture is now made smaller (" + string(q.Picture.Size) + ")"
	case q.Method == Convert:
		effect = "so the PC now makes the picture at a lighter setting"
	case p.Method == Convert:
		effect = "so the film is now sent with its own picture"
	default:
		effect = "so the film is now played another way"
	}
	if p.Subtitles == SubsBurn && q.Subtitles == SubsTrack {
		effect += ", and the subtitles are shown as plain text, without their design"
	}
	return cause + ", " + effect + "."
}
