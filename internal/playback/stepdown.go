package playback

// This file says what to do when a film that is playing runs into trouble:
// what to change so that it plays, and what to remember so that the next
// film does not run into the same trouble. Like the rest of the package it
// only decides. Keeping what was learned, and for how long, is the
// server's work.

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
	LessonDevice  Lesson = "device"  // this device cannot show a kind of picture
	LessonPC      Lesson = "pc"      // this PC converts slower than was thought
	LessonNetwork Lesson = "network" // films reach this device too slowly
	LessonDraw    Lesson = "draw"    // this device's page cannot draw subtitles
)

// Step is what to change after trouble. Only the part that Lesson names is
// filled in.
type Step struct {
	// Lesson is what was learned. "" means nothing can be changed: no step
	// is taken and the film is left as it is.
	Lesson Lesson `json:"lesson"`
	// Notice tells the viewer in one sentence what changes.
	Notice string `json:"notice"`

	Device  DeviceLesson  `json:"device"`
	PC      SpeedLesson   `json:"pc"`
	Network NetworkLesson `json:"network"`
	// Draw names the drawing that failed and is switched off.
	Draw Drawing `json:"draw"`

	// Lower is the rung to ask for when this film is started again
	// (Want.Lower); 0 for no wish. It belongs to this play only and is not
	// part of what is remembered.
	Lower int `json:"lower"`
}

// DeviceLesson is a kind of picture a device had trouble showing, and what
// follows from it.
type DeviceLesson struct {
	Picture PictureKind `json:"picture"`
	// NoVideo and NoTall name the format, as Caps.Video and Caps.Tall do,
	// that the device turns out not to play at all, or not when the
	// picture is larger than 1080p.
	NoVideo string `json:"noVideo"`
	NoTall  string `json:"noTall"`
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
	// What a connection that starved a player is trusted with, and for
	// how long.
	networkRate  = 4_000_000
	networkHours = 24
)

// StepDown says what to change when a film that is playing runs into
// trouble. p is the plan being played; pace is how fast this PC has been
// converting it, in seconds of film per second over the last three chunks
// (0 when not known, or when nothing is being converted); steps is how
// many steps this play has taken already.
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
// Steps only go down, and there are at most two in one play.
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
			return Step{Lesson: LessonDevice, Device: d,
				Notice: "This device had trouble showing the picture at this size, so it is now sent a smaller one."}
		}
		if p.Picture.Codec == "" {
			return Step{} // the file was never inspected: there is no kind to name
		}
		d := DeviceLesson{Picture: p.Picture}
		key := videoKey(p.Picture.Codec, p.Picture.BitDepth)
		if p.Picture.Size == Size4K {
			d.NoTall = key
			return Step{Lesson: LessonDevice, Device: d,
				Notice: "This device had trouble showing a picture this large, so the PC now makes it smaller."}
		}
		d.NoVideo = key
		return Step{Lesson: LessonDevice, Device: d,
			Notice: "This device had trouble showing this film's picture, so the PC now converts it to a kind every device plays."}

	case Starved:
		slow := pace > 0 && pace < slowPace
		if p.Method != Convert {
			if slow {
				// Nothing is converted, and still the film was not ready
				// in time: the disk it is read from is the slow part.
				// Converting would only add to the wait.
				return Step{}
			}
			return network()
		}
		if slow {
			return Step{Lesson: LessonPC, Lower: p.Video.Rung + 1,
				PC:     SpeedLesson{Class: p.Video.Class, Rung: p.Video.Rung, Encoder: p.Video.Encoder, Speed: pace},
				Notice: "This PC could not convert the film fast enough, so the picture is now made lighter."}
		}
		if p.Video.Rung >= 1 && p.Video.Rung <= NumRungs && ladder[p.Video.Rung-1].MaxRate <= networkRate {
			return Step{} // already as light as the cap would make it
		}
		return network()

	case RendererFailed:
		if p.Subtitles != SubsDraw {
			return Step{}
		}
		off := Drawing{Styled: p.SubtitleKind == StyledSubs, Picture: p.SubtitleKind == PictureSubs}
		if !off.Styled && !off.Picture {
			off = Drawing{Styled: true, Picture: true}
		}
		return Step{Lesson: LessonDraw, Draw: off,
			Notice: "This device could not draw the subtitles over the film, so they are now shown another way."}
	}
	return Step{}
}

func network() Step {
	return Step{Lesson: LessonNetwork, Network: NetworkLesson{MaxRate: networkRate, Hours: networkHours},
		Notice: "The film was reaching this device too slowly, so a lighter picture is now sent."}
}

// Apply gives what to plan with once a step has been taken: what the
// device plays, what the viewer wants and the limits, each with the
// lesson worked in. Nothing that is passed in is changed.
//
// It serves two uses: planning the film again right after the step, and
// planning a later film for a device with a remembered lesson. For the
// second, clear Lower first: it belongs to the play that took the step.
//
// Everything Apply does takes something away, never adds. What that
// promises, and what it does not:
//
//   - With speeds like real ones (a lighter rung is never slower than a
//     heavier one), the plan made afterwards is never a better picture
//     than the one before. With a table where a lighter rung is slower,
//     a cap can leave only rungs that are too slow, and a lesson that is
//     only advice then gives way to the film as it was (see advised).
//   - The plan made afterwards may be the very same one: where this PC
//     cannot convert, a lesson changes nothing. The caller can see that
//     and spare the viewer a restart.
func (s Step) Apply(c *Caps, w Want, l Limits) (*Caps, Want, Limits) {
	switch s.Lesson {
	case LessonDevice:
		if c != nil && (s.Device.NoVideo != "" || s.Device.NoTall != "") {
			trimmed := *c
			if s.Device.NoVideo != "" {
				trimmed.Video = without(c.Video, s.Device.NoVideo)
				trimmed.Tall = without(c.Tall, s.Device.NoVideo)
			}
			if s.Device.NoTall != "" {
				trimmed.Tall = without(trimmed.Tall, s.Device.NoTall)
			}
			c = &trimmed
		}
		l.MaxHeight = capped(l.MaxHeight, s.Device.MaxHeight)
	case LessonPC:
		if encoderName(l.Encoder) == encoderName(s.PC.Encoder) {
			l.Speed = l.Speed.lowered(s.PC.Class, s.PC.Rung, s.PC.Speed)
		}
		if s.Lower > w.Lower {
			w.Lower = s.Lower
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

// without is a copy of what a device plays with one format switched off.
func without(plays map[string]bool, key string) map[string]bool {
	out := make(map[string]bool, len(plays)+1)
	for k, v := range plays {
		out[k] = v
	}
	out[key] = false
	return out
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
