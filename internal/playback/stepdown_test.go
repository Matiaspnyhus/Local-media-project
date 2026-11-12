package playback

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The films and devices the step-down tests play: each plan in the table
// below is what DecideFor really answers for one of them.
var (
	stepPhone = surfaces[3].on(newDevices["safari"])      // plays HEVC in 4K, cannot open MKV, the page cannot go fullscreen
	stepPC    = surfaces[0].on(newDevices["chrome+hevc"]) // plays HEVC in 4K, no Dolby sound, the page draws subtitles
	plainWish = Want{Audio: -1, Subtitle: -1}

	stepUHD   = film("mp4", "hevc", 10, "aac", sizes[2], colours[1], "")         // 4K HDR: sent as it is to both
	stepHD    = film("mp4", "hevc", 10, "aac", sizes[1], colours[0], "")         // 1080p: sent as it is to both
	stepMKV   = film("matroska", "h264", 8, "aac", sizes[1], colours[0], "")     // repackaged for the phone
	stepDTS   = film("mp4", "h264", 8, "dts", sizes[1], colours[0], "")          // only the sound is converted
	stepOld   = film("avi", "mpeg4", 8, "mp3", sizes[1], colours[0], "")         // DivX at 1080p: converted for every device
	stepSmall = film("avi", "mpeg4", 8, "mp3", sizes[0], colours[0], "")         // DivX at 480p
	stepAnime = film("mp4", "h264", 8, "aac", sizes[1], colours[0], StyledSubs)  // styled subtitles
	stepDisc  = film("mp4", "h264", 8, "aac", sizes[1], colours[0], PictureSubs) // picture subtitles
	stepText  = film("mp4", "h264", 8, "aac", sizes[1], colours[0], TextSubs)
)

func lighter(n int) Want { return Want{Audio: -1, Subtitle: -1, Lower: n} }

// What each kind of trouble teaches (StepDown). What the lesson leads to
// is another matter: see TestReplan.
func TestStepDown(t *testing.T) {
	plan := func(m Media, dev *Caps, w Want, method Method) Plan {
		t.Helper()
		p := DecideFor(m, dev, w, middling)
		if p.Method != method {
			t.Fatalf("the film this case is about is answered %s %v, not %s", p.Method, p.Reasons, method)
		}
		return p
	}
	uhd := plan(stepUHD, stepPhone, plainWish, Direct)
	hd := plan(stepHD, stepPhone, plainWish, Direct)
	mkv := plan(stepMKV, stepPhone, plainWish, Remux)
	dts := plan(stepDTS, stepPC, plainWish, Audio)
	rung := func(n int) Plan { return plan(stepOld, stepPC, lighter(n), Convert) }
	small := plan(stepSmall, stepPC, plainWish, Convert)
	styled := plan(stepAnime, stepPC, Want{Audio: -1, Subtitle: 0}, Direct)
	pictures := plan(stepDisc, stepPC, Want{Audio: -1, Subtitle: 0}, Direct)
	text := plan(stepText, stepPC, Want{Audio: -1, Subtitle: 0}, Direct)
	refused := DecideFor(stepOld, stepPC, plainWish, Limits{})

	// One stall caps the connection at what the 720p rungs spend; a stall
	// at 720p caps it at what the 480p rung spends.
	network := Step{Lesson: LessonNetwork, Network: NetworkLesson{MaxRate: 5_000_000, Hours: 24}}
	networkAgain := Step{Lesson: LessonNetwork, Network: NetworkLesson{MaxRate: 2_500_000, Hours: 24}}
	slower := func(n, next int, speed float64) Step {
		return Step{Lesson: LessonPC, PC: SpeedLesson{Class: ClassHD, Rung: n, Encoder: "x264", Speed: speed}, Lower: next}
	}
	device := func(codec string, depth int, size Size, maxHeight int) Step {
		return Step{Lesson: LessonDevice, Device: DeviceLesson{Picture: PictureKind{Codec: codec, BitDepth: depth, Size: size}, MaxHeight: maxHeight}}
	}
	none := Step{}

	for _, c := range []struct {
		name     string
		plan     Plan
		evidence Evidence
		pace     float64
		steps    int
		want     Step
	}{
		// The device cannot show the picture: the lesson names the kind.
		{"4K HEVC sent as it is", uhd, DecodeTrouble, 0, 0, device("hevc", 10, Size4K, 0)},
		{"1080p HEVC sent as it is", hd, DecodeTrouble, 0, 0, device("hevc", 10, Size1080, 0)},
		{"a repackaged MKV", mkv, DecodeTrouble, 0, 0, device("h264", 8, Size1080, 0)},
		{"only the sound converted", dts, DecodeTrouble, 3.1, 0, device("h264", 8, Size1080, 0)},
		{"a picture converted to 1080p", rung(1), DecodeTrouble, 2.9, 0, device("h264", 8, Size1080, 720)},
		{"a picture converted to 1080p, second rung", rung(2), DecodeTrouble, 0, 0, device("h264", 8, Size1080, 720)},
		{"a picture converted to 720p", rung(3), DecodeTrouble, 0, 0, device("h264", 8, Size720, 480)},
		{"a picture converted to 480p: nothing smaller is made", rung(5), DecodeTrouble, 0, 0, none},
		{"a small film converted at the best rung: it is already small", small, DecodeTrouble, 0, 0, none},
		{"a file that was never inspected", Decide(Media{}, stepPC, -1), DecodeTrouble, 0, 0, none},
		{"a device that has not said what it plays: the kind is known all the same", Decide(media("mp4", "hevc", 10, "aac"), nil, -1), DecodeTrouble, 0, 0,
			device("hevc", 10, "", 0)},

		// The player ran out of film while the PC was not keeping up.
		{"converting at rung 1 at 0.8 of the film's speed", rung(1), Starved, 0.8, 0, slower(1, 2, 0.8)},
		{"converting at rung 2 at 0.94", rung(2), Starved, 0.94, 0, slower(2, 3, 0.94)},
		{"converting at the last rung at 0.5: there is no rung after it", rung(5), Starved, 0.5, 0, slower(5, 5, 0.5)},
		// ... or although it was: the connection is the slow part.
		{"converting at rung 2 at exactly 0.95", rung(2), Starved, 0.95, 0, network},
		{"converting at rung 2 at 1.4", rung(2), Starved, 1.4, 0, network},
		{"converting at rung 2, speed not known", rung(2), Starved, 0, 0, network},
		{"converting at rung 3: already within the first cap", rung(3), Starved, 2, 0, networkAgain},
		{"converting at rung 4", rung(4), Starved, 2, 0, networkAgain},
		{"converting at the last rung, fast enough: nothing lighter to send", rung(5), Starved, 1.2, 0, none},
		{"the original sent as it is", uhd, Starved, 0, 0, network},
		{"only the sound converted, fast", dts, Starved, 3, 0, network},
		{"a file that was never inspected, starved", Decide(Media{}, stepPC, -1), Starved, 0, 0, network},
		// Nothing is converted and the film still was not ready: the disk
		// is slow, and converting would not help.
		{"repackaging at 0.6 of the film's speed", mkv, Starved, 0.6, 0, none},
		{"only the sound converted, at 0.9", dts, Starved, 0.9, 0, none},

		// The page could not draw the subtitles.
		{"styled subtitles drawn by the page", styled, RendererFailed, 0, 0, Step{Lesson: LessonDraw, Draw: Drawing{Styled: true}}},
		{"picture subtitles drawn by the page", pictures, RendererFailed, 0, 0, Step{Lesson: LessonDraw, Draw: Drawing{Picture: true}}},
		{"text subtitles: nothing is drawn", text, RendererFailed, 0, 0, none},
		{"no subtitles", uhd, RendererFailed, 0, 0, none},

		// At most two steps in one play, whatever happens.
		{"the second step", uhd, DecodeTrouble, 0, 1, device("hevc", 10, Size4K, 0)},
		{"a third step", uhd, DecodeTrouble, 0, 2, none},
		{"a third step, starved", rung(1), Starved, 0.5, 2, none},
		{"a third step, subtitles", styled, RendererFailed, 0, 2, none},
		{"a fourth", uhd, Starved, 0, 3, none},

		// Nothing to step down from.
		{"a film that was refused", refused, DecodeTrouble, 0, 0, none},
		{"a film that was refused, starved", refused, Starved, 0, 0, none},
		{"trouble of a kind nobody knows", uhd, Evidence("smoke"), 0, 0, none},
	} {
		if got := StepDown(c.plan, c.evidence, c.pace, c.steps); got != c.want {
			t.Errorf("%s, %s: step %+v, want %+v", c.name, c.evidence, got, c.want)
		}
	}
}

// What trouble leads to, for the cases that matter most: what plays
// afterwards, and the sentence the viewer is shown, word for word. The
// sentence is written from what really changed, so it says "nothing was
// changed" where nothing was.
func TestReplan(t *testing.T) {
	sub := Want{Audio: -1, Subtitle: 0}
	uhdH264 := says(newDevices["chrome"], "h264", false) // plays H.264 in 4K
	slowLink := func(l Limits, rate int) Limits { l.MaxRate = rate; return l }
	lastRungOnly := measured(able, [NumRungs]float64{0.7, 0.9, 1.1, 1.3, 1.6}, every(0), every(0), every(0))
	h264at := func(size pictureSize, rate int) Media {
		m := film("mp4", "h264", 8, "aac", size, colours[0], "")
		m.BitRate = rate
		return m
	}
	animeMKV := film("matroska", "hevc", 10, "flac", sizes[1], colours[0], StyledSubs)
	smallHEVC := film("mp4", "hevc", 10, "aac", pictureSize{"360p", 640, 360}, colours[0], "")

	type after struct {
		changed   bool
		lesson    Lesson
		method    Method
		rung      int
		subtitles SubtitleMode
		notice    string
	}
	for _, c := range []struct {
		name  string
		m     Media
		dev   *Caps
		w     Want
		l     Limits
		e     Evidence
		pace  float64
		steps int
		want  after
	}{
		// The device cannot show the picture.
		{"1080p HEVC sent as it is", stepHD, stepPhone, plainWish, middling, DecodeTrouble, 0, 0,
			after{true, LessonDevice, Convert, 1, SubsNone, "This device had trouble showing the picture, so the PC now converts the picture to a kind every device plays."}},
		{"4K HEVC sent as it is, a PC that keeps up with 4K HDR at 720p", stepUHD, stepPhone, plainWish, middling, DecodeTrouble, 0, 0,
			after{true, LessonDevice, Convert, 3, SubsNone, "This device had trouble showing the picture, so the PC now converts the picture to a smaller one (720p)."}},
		{"a converted picture at 1080p", stepOld, stepPC, plainWish, middling, DecodeTrouble, 0, 0,
			after{true, LessonDevice, Convert, 3, SubsNone, "This device had trouble showing the picture, so the picture is now made smaller (720p)."}},
		// ... and this PC cannot make it an easier one: the film is left
		// playing, not refused, and nobody is promised anything.
		{"4K H.264 sent as it is, a PC that cannot convert", h264at(sizes[2], 0), uhdH264, plainWish, Limits{}, DecodeTrouble, 0, 0,
			after{false, LessonDevice, Direct, 0, SubsNone, "This device had trouble showing the picture, but nothing easier to show can be made of this film here, so nothing was changed."}},
		{"1080p HEVC sent as it is, a device that takes no chunks", stepHD, caps(false, "mp4", "hevc10", "aac"), plainWish, able, DecodeTrouble, 0, 0,
			after{false, LessonDevice, Direct, 0, SubsNone, "This device had trouble showing the picture, but nothing easier to show can be made of this film here, so nothing was changed."}},
		{"a device that has not said what it plays", stepHD, nil, plainWish, able, DecodeTrouble, 0, 0,
			after{false, LessonDevice, Direct, 0, SubsNone, "This device had trouble showing the picture, but it has not told the app what it can play, so nothing was changed; reloading the page makes it say so again."}},
		{"a converted picture that is as small as they get", stepSmall, stepPC, plainWish, able, DecodeTrouble, 0, 0,
			after{false, "", Convert, 1, SubsNone, ""}},

		// This PC is slower than was thought.
		{"too slow at rung 1", stepOld, stepPC, plainWish, middling, Starved, 0.8, 0,
			after{true, LessonPC, Convert, 2, SubsNone, "This PC could not convert the film fast enough, so the PC now makes the picture at a lighter setting."}},
		{"too slow at rung 2", stepOld, stepPC, lighter(2), middling, Starved, 0.8, 0,
			after{true, LessonPC, Convert, 3, SubsNone, "This PC could not convert the film fast enough, so the picture is now made smaller (720p)."}},
		// Too slow at the last rung, for a film that must be converted:
		// the film is left playing, and what the viewer is told is true.
		{"too slow at the last rung", stepOld, stepPC, plainWish, lastRungOnly, Starved, 0.8, 0,
			after{false, LessonPC, Convert, 5, SubsNone, "This PC could not convert the film fast enough, but it cannot make a lighter picture than this one, so nothing was changed."}},
		// Too slow at the last rung for a film that was converted only on
		// advice: the film plays as it is, as it would without the advice.
		{"too slow to follow a slow connection's advice", h264at(sizes[1], 8_000_000), newDevices["chrome"], plainWish, slowLink(lastRungOnly, 2_500_000), Starved, 0.8, 0,
			after{true, LessonPC, Direct, 0, SubsNone, "This PC could not convert the film fast enough, so the film is now sent with its own picture."}},
		{"too slow to make the lighter picture that was asked for", h264at(sizes[1], 8_000_000), newDevices["chrome"], lighter(3), lastRungOnly, Starved, 0.8, 0,
			after{true, LessonPC, Direct, 0, SubsNone, "This PC could not convert the film fast enough, so the film is now sent with its own picture."}},
		// Too slow to put styled subtitles into the picture: the film
		// plays with their words as plain text.
		{"too slow to put the subtitles' design into the picture", animeMKV, stepPhone, Want{Audio: -1, Subtitle: 0, Styled: Designed}, lastRungOnly, Starved, 0.8, 0,
			after{true, LessonPC, Audio, 0, SubsTrack, "This PC could not convert the film fast enough, so the film is now sent with its own picture, and the subtitles are shown as plain text, without their design."}},

		// The connection is the slow part. One stall costs one size.
		{"a converted 1080p picture", stepOld, stepPC, plainWish, fast, Starved, 2.9, 0,
			after{true, LessonNetwork, Convert, 3, SubsNone, "The film was reaching this device too slowly, so the picture is now made smaller (720p)."}},
		{"a converted 720p picture", stepOld, stepPC, plainWish, slowLink(fast, 5_000_000), Starved, 2.9, 1,
			after{true, LessonNetwork, Convert, 5, SubsNone, "The film was reaching this device too slowly, so the picture is now made smaller (480p)."}},
		{"1080p at 8 Mbit/s sent as it is", h264at(sizes[1], 8_000_000), newDevices["chrome"], plainWish, fast, Starved, 0, 0,
			after{true, LessonNetwork, Convert, 3, SubsNone, "The film was reaching this device too slowly, so the PC now converts the picture to a smaller one (720p)."}},
		{"4K HDR sent as it is", stepUHD, stepPhone, plainWish, middling, Starved, 0, 0,
			after{true, LessonNetwork, Convert, 3, SubsNone, "The film was reaching this device too slowly, so the PC now converts the picture to a smaller one (720p)."}},
		// A film whose size was not read may be small already: nobody is
		// told that its picture is made smaller.
		{"a film whose size was not read, sent as it is", h264at(pictureSize{"a size that was not read", 0, 0}, 8_000_000), newDevices["chrome"], plainWish, fast, Starved, 0, 0,
			after{true, LessonNetwork, Convert, 3, SubsNone, "The film was reaching this device too slowly, so the PC now converts the picture to a lighter one."}},
		// A film lighter than the first cap is not helped by it: the
		// second one is tried, and kept if it helps.
		{"1080p at 4 Mbit/s sent as it is", h264at(sizes[1], 4_000_000), newDevices["chrome"], plainWish, fast, Starved, 0, 0,
			after{true, LessonNetwork, Convert, 5, SubsNone, "The film was reaching this device too slowly, so the PC now converts the picture to a smaller one (480p)."}},
		{"480p at 3 Mbit/s sent as it is", h264at(sizes[0], 3_000_000), newDevices["chrome"], plainWish, fast, Starved, 0, 0,
			after{true, LessonNetwork, Convert, 5, SubsNone, "The film was reaching this device too slowly, so the PC now converts the picture to a lighter one."}},
		// Nothing lighter can be made: nothing is changed, and said so.
		{"480p at 2 Mbit/s sent as it is", h264at(sizes[0], 2_000_000), newDevices["chrome"], plainWish, fast, Starved, 0, 0,
			after{false, LessonNetwork, Direct, 0, SubsNone, "The film was reaching this device too slowly, but nothing lighter can be made of it here, so nothing was changed."}},
		{"1080p sent as it is, a PC that cannot convert", h264at(sizes[1], 8_000_000), newDevices["chrome"], plainWish, Limits{}, Starved, 0, 0,
			after{false, LessonNetwork, Direct, 0, SubsNone, "The film was reaching this device too slowly, but nothing lighter can be made of it here, so nothing was changed."}},
		{"a file that was never inspected", Media{}, newDevices["chrome"], plainWish, able, Starved, 0, 0,
			after{false, LessonNetwork, Direct, 0, SubsNone, "The film was reaching this device too slowly, but nothing lighter can be made of it here, so nothing was changed."}},
		{"a converted picture at the last rung, fast enough", stepOld, stepPC, lighter(5), fast, Starved, 2.9, 0,
			after{false, "", Convert, 5, SubsNone, ""}},

		// The page could not draw the subtitles.
		{"styled subtitles", stepAnime, stepPC, sub, middling, RendererFailed, 0, 0,
			after{true, LessonDraw, Convert, 1, SubsBurn, "This device could not draw the subtitles over the film, so the PC now puts them into the picture."}},
		{"styled subtitles, a viewer who prefers plain text", stepAnime, stepPC, Want{Audio: -1, Subtitle: 0, Styled: Plain}, middling, RendererFailed, 0, 0,
			after{true, LessonDraw, Direct, 0, SubsTrack, "This device could not draw the subtitles over the film, so they are now shown as plain text."}},
		{"styled subtitles, a PC that cannot convert", stepAnime, stepPC, sub, Limits{}, RendererFailed, 0, 0,
			after{true, LessonDraw, Direct, 0, SubsTrack, "This device could not draw the subtitles over the film, so they are now shown as plain text, without their design."}},
		{"picture subtitles", stepDisc, stepPC, sub, middling, RendererFailed, 0, 0,
			after{true, LessonDraw, Convert, 1, SubsBurn, "This device could not draw the subtitles over the film, so the PC now puts them into the picture."}},
		// Picture subtitles that can be neither drawn nor put into the
		// picture: the film itself was playing, and goes on without them.
		{"picture subtitles, a PC that cannot convert", stepDisc, stepPC, sub, Limits{}, RendererFailed, 0, 0,
			after{true, LessonDraw, Direct, 0, SubsNone, "This device could not draw the subtitles over the film, and they cannot be put into the picture here, so the film now plays without them."}},

		// No third step.
		{"a third step", stepHD, stepPhone, plainWish, middling, DecodeTrouble, 0, 2, after{false, "", Direct, 0, SubsNone, ""}},
		{"a film that does not play", stepOld, stepPC, plainWish, Limits{}, Starved, 0, 0, after{false, "", None, 0, SubsNone, ""}},
	} {
		before := DecideFor(c.m, c.dev, c.w, c.l)
		o := Replan(c.m, c.dev, c.w, c.l, c.e, c.pace, c.steps)
		got := after{o.Changed, o.Step.Lesson, o.Plan.Method, o.Plan.Video.Rung, o.Plan.Subtitles, o.Notice}
		if got != c.want {
			t.Errorf("%s, %s:\n got %+v\nwant %+v\n(before: %s rung %d %q)", c.name, c.e, got, c.want, before.Method, before.Video.Rung, before.Reasons)
			continue
		}
		// A film that played is never taken away by a step.
		if before.Playable != No && o.Plan.Playable == No {
			t.Errorf("%s: a step turned a film that played into a refusal: %q", c.name, o.Plan.Reasons)
		}
		// What is handed back plans the film as the outcome says, so the
		// rest of the play can go on from it.
		if again := DecideFor(c.m, o.Caps, o.Want, o.Limits); !unchanged(again, o.Plan) {
			t.Errorf("%s: planning with what came back gives %s rung %d, the outcome says %s rung %d", c.name, again.Method, again.Video.Rung, o.Plan.Method, o.Plan.Video.Rung)
		}
		if !o.Changed && !unchanged(before, o.Plan) {
			t.Errorf("%s: nothing is said to have changed, and the plan went from %s to %s", c.name, before.Method, o.Plan.Method)
		}
	}

	// What a step is remembered as, and what it does to later films.

	// Trouble with a 4K film leaves 1080p of the same format alone; trouble
	// with a 1080p film leaves a small film of that format alone, and
	// takes in the larger ones.
	o := Replan(stepUHD, stepPhone, plainWish, middling, DecodeTrouble, 0, 0)
	if p := DecideFor(stepHD, o.Caps, plainWish, o.Limits); p.Method != Direct {
		t.Errorf("1080p after trouble with 4K: %s %q", p.Method, p.Reasons)
	}
	o = Replan(stepHD, stepPhone, plainWish, middling, DecodeTrouble, 0, 0)
	if p := DecideFor(smallHEVC, o.Caps, plainWish, o.Limits); p.Method != Direct {
		t.Errorf("a 640x360 film after trouble with 1080p of the same format: %s %q", p.Method, p.Reasons)
	}
	if p := DecideFor(stepUHD, o.Caps, plainWish, o.Limits); p.Method != Convert {
		t.Errorf("4K after trouble with 1080p of the same format: %s %q", p.Method, p.Reasons)
	}
	if p := DecideFor(film("mp4", "hevc", 8, "aac", sizes[1], colours[0], ""), o.Caps, plainWish, o.Limits); p.Method != Direct {
		t.Errorf("8-bit HEVC after trouble with 10-bit: %s %q", p.Method, p.Reasons)
	}
	// Trouble with a film whose size was not read speaks for every size of
	// that format: there is nothing to tell a small one from it by.
	unread := film("mp4", "hevc", 10, "aac", pictureSize{"a size that was not read", 0, 0}, colours[0], "")
	if o := Replan(unread, stepPhone, plainWish, middling, DecodeTrouble, 0, 0); o.Step.Device.Picture.Size != "" || DecideFor(smallHEVC, o.Caps, plainWish, o.Limits).Method != Convert ||
		DecideFor(stepUHD, o.Caps, plainWish, o.Limits).Method != Convert {
		t.Errorf("after trouble with a film whose size was not read: %+v", o.Step)
	}
	// The reason given afterwards says that the device had trouble, not
	// that the browser cannot play the format, which it says it can.
	p := DecideFor(stepHD, o.Caps, plainWish, o.Limits)
	if !sameStrings(p.Reasons, []string{"Its picture is 1080p 10-bit HEVC (H.265), which this device has had trouble showing."}) {
		t.Errorf("the reason after a lesson: %q", p.Reasons)
	}
	if !reflect.DeepEqual(o.Caps, stepPhone) {
		t.Errorf("a lesson about a picture changed what the device reports: %+v", o.Caps)
	}
	// A device that cannot be helped (this PC cannot convert) is still sent
	// the next film of that kind rather than refused it.
	o = Replan(stepHD, stepPhone, plainWish, Limits{}, DecodeTrouble, 0, 0)
	if p := DecideFor(stepHD, o.Caps, plainWish, o.Limits); p.Method != Direct || len(p.Reasons) != 0 {
		t.Errorf("the next film of a kind the device had trouble with, on a PC that cannot convert: %s %q", p.Method, p.Reasons)
	}

	// A PC that turned out slower than measured keeps the figure, so the
	// next film does not start at the slow rung again.
	o = Replan(stepOld, stepPC, plainWish, middling, Starved, 0.8, 0)
	if o.Limits.Lower != 2 || o.Want != plainWish || o.Limits.Speed[ClassHD][0] != 0.8 {
		t.Errorf("after converting too slowly at rung 1: from rung %d, wishes %+v, speeds %v", o.Limits.Lower, o.Want, o.Limits.Speed[ClassHD])
	}
	// The lighter rung is the step's doing, not the viewer's wish: nothing
	// says "you asked", and when what forced the conversion goes away (the
	// viewer switches the subtitles off) the film plays as it is again.
	burned := Want{Audio: -1, Subtitle: 0}
	o = Replan(stepDisc, stepPhone, burned, middling, Starved, 0.8, 0)
	if !o.Changed || o.Plan.Video.Rung != 2 || o.Plan.Subtitles != SubsBurn || has(o.Plan.Reasons, "You asked") {
		t.Errorf("burned-in subtitles after converting too slowly: %+v", o.Plan)
	}
	if p := DecideFor(stepDisc, o.Caps, plainWish, o.Limits); p.Method != Direct {
		t.Errorf("the same film with the subtitles switched off: %s rung %d %q", p.Method, p.Video.Rung, p.Reasons)
	}
	if p := DecideFor(stepOld, stepPC, plainWish, o.Limits); p.Video.Rung != 2 {
		t.Errorf("the next film on that PC: %+v", p)
	}
	// Where nothing lighter was left, the film plays on under the plan it
	// had, and the figure is handed out for the server to keep: with it,
	// the next start of that film gets the honest answer, with the figure.
	lastRungOnly = measured(able, [NumRungs]float64{0.7, 0.9, 1.1, 1.3, 1.6}, every(0), every(0), every(0))
	o = Replan(stepOld, stepPC, plainWish, lastRungOnly, Starved, 0.8, 0)
	if o.Step.PC != (SpeedLesson{Class: ClassHD, Rung: 5, Encoder: "x264", Speed: 0.8}) || o.Limits.Speed[ClassHD][4] != 1.6 || o.Plan.Video.Rung != 5 {
		t.Errorf("too slow at the last rung: %+v", o)
	}
	_, _, learned := o.Step.Apply(stepPC, plainWish, lastRungOnly)
	if p := DecideFor(stepOld, stepPC, plainWish, learned); !p.TooSlow || !has(p.Reasons, "at best it manages 0.8 seconds of film a second") {
		t.Errorf("the next start of that film: %s %q", p.Method, p.Reasons)
	}

	// A slow connection is remembered as the cap that helped. A film that
	// was lighter than both caps teaches only the first.
	o = Replan(h264at(sizes[1], 4_000_000), newDevices["chrome"], plainWish, fast, Starved, 0, 0)
	if o.Step.Network != (NetworkLesson{MaxRate: 2_500_000, Hours: 24}) || o.Limits.MaxRate != 2_500_000 {
		t.Errorf("a 4 Mbit/s film that starved: %+v", o.Step)
	}
	o = Replan(h264at(sizes[0], 2_000_000), newDevices["chrome"], plainWish, fast, Starved, 0, 0)
	if o.Step.Network != (NetworkLesson{MaxRate: 5_000_000, Hours: 24}) || o.Limits.MaxRate != 5_000_000 {
		t.Errorf("a 2 Mbit/s film that starved: %+v", o.Step)
	}

	// Subtitles the page failed to draw: the other kind is still drawn,
	// and a film that goes on without subtitles no longer asks for them.
	o = Replan(stepAnime, stepPC, sub, middling, RendererFailed, 0, 0)
	if p := DecideFor(stepDisc, o.Caps, sub, o.Limits); p.Subtitles != SubsDraw {
		t.Errorf("picture subtitles after the styled renderer failed: %+v", p)
	}
	if o = Replan(stepDisc, stepPC, sub, Limits{}, RendererFailed, 0, 0); o.Want.Subtitle != -1 || o.Caps.Draw.Picture {
		t.Errorf("picture subtitles that can be shown no way: %+v, %+v", o.Want, o.Caps.Draw)
	}
}

// A play takes two steps at most, and each is told truthfully: a repackaged
// film the phone cannot show is converted; if the phone cannot show that
// either it is sent smaller; then nothing more.
func TestTwoStepsAndNoMore(t *testing.T) {
	o := Replan(stepMKV, stepPhone, plainWish, middling, DecodeTrouble, 0, 0)
	if !o.Changed || o.Plan.Method != Convert || o.Plan.Video.Rung != 1 || o.Plan.Video.Height != 1080 {
		t.Fatalf("an MKV after decode trouble: %+v", o)
	}
	o = Replan(stepMKV, o.Caps, o.Want, o.Limits, DecodeTrouble, 0, 1)
	if !o.Changed || o.Plan.Method != Convert || o.Plan.Video.Rung != 3 || o.Plan.Video.Height != 720 ||
		o.Notice != "This device had trouble showing the picture, so the picture is now made smaller (720p)." {
		t.Fatalf("an MKV after decode trouble twice: %+v", o)
	}
	again := Replan(stepMKV, o.Caps, o.Want, o.Limits, DecodeTrouble, 0, 2)
	if again.Changed || again.Step.Lesson != "" || again.Notice != "" || !unchanged(again.Plan, o.Plan) {
		t.Errorf("a third step: %+v", again)
	}
}

// Steps only go down. For a spread of films, devices, PCs and kinds of
// trouble, two in a row in every order, each step is held to this:
//
//   - A film that played still plays.
//   - The picture is not better than before. The one exception is a
//     picture that did not have to be converted (it was, on a lesson's
//     advice, for a wish, or to carry the design of styled subtitles) and
//     goes back to the film's own when this PC proves too slow for it.
//   - The thing that failed is not tried again.
//   - What the viewer is told agrees with what changed, and where nothing
//     changed they are told so, or nothing.
//   - A play takes at most two steps.
//
// The PCs include ones measured only in part, as a table is after real
// plays have lowered it and before a benchmark has run, and one that
// cannot convert at all. In every table a lighter rung that was measured
// is no slower than a heavier one that was.
func TestStepsOnlyGoDown(t *testing.T) {
	busy := middling
	busy.Busy = 1
	onlyLastSlow := measured(able, [NumRungs]float64{0, 0, 0, 0, 0.8}, [NumRungs]float64{0, 0, 0, 0, 0.8}, [NumRungs]float64{0, 0, 0, 0, 0.8}, [NumRungs]float64{0, 0, 0, 0, 0.8})
	firstSlow := measured(able, [NumRungs]float64{0.8, 0, 0, 0, 0}, [NumRungs]float64{0, 0, 0.8, 0, 0}, [NumRungs]float64{0, 0, 0.8, 0, 0}, [NumRungs]float64{0, 0, 0.8, 0, 0})
	lastOnly := measured(able, [NumRungs]float64{0.7, 0.9, 1.1, 1.3, 1.6}, [NumRungs]float64{0.5, 0.7, 0.9, 1.2, 1.5}, [NumRungs]float64{0, 0, 1.0, 1.2, 1.5}, [NumRungs]float64{0, 0, 0, 0.9, 1.2})
	pcs := []Limits{able, middling, busy, {}, onlyLastSlow, firstSlow, lastOnly}
	type trouble struct {
		evidence Evidence
		pace     float64
	}
	troubles := []trouble{{DecodeTrouble, 0}, {Starved, 0}, {Starved, 0.5}, {Starved, 1.2}, {RendererFailed, 0}}
	// What was seen along the way, counted for each device by itself (the
	// devices are walked side by side) and added up at the end.
	type tally struct {
		taken                    map[Lesson]int
		changed, stayed, gaveWay int
	}

	// check holds one outcome to the rules and returns it.
	check := func(t *testing.T, n *tally, id func() string, m Media, base Plan, dev *Caps, w Want, l Limits, tr trouble, steps int) Outcome {
		p := DecideFor(m, dev, w, l)
		o := Replan(m, dev, w, l, tr.evidence, tr.pace, steps)
		q := o.Plan
		taken := n.taken
		fail := func(what string) {
			t.Helper()
			t.Fatalf("%s, %s at pace %.1f, step %d: %s rung %d -> %s rung %d %q, told %q: %s",
				id(), tr.evidence, tr.pace, steps+1, p.Method, p.Video.Rung, q.Method, q.Video.Rung, q.Reasons, o.Notice, what)
		}
		if p.Playable != No && q.Playable == No {
			fail("a film that played was taken away")
		}
		if again := DecideFor(m, o.Caps, o.Want, o.Limits); !unchanged(again, q) {
			fail("planning with what came back does not give the plan that came back")
		}
		if o.Notice != "" && !strings.HasSuffix(o.Notice, ".") || strings.Contains(o.Notice, "another way") {
			fail("the viewer is told something that is not a plain sentence about what happened")
		}
		if !o.Changed {
			n.stayed++
			if !unchanged(p, q) {
				fail("nothing is said to have changed, and the plan did")
			}
			if (o.Step.Lesson != "") != strings.Contains(o.Notice, "nothing was changed") || o.Step.Lesson == "" && o.Notice != "" {
				fail("nothing changed, and the viewer is not told so (or is told something although nothing was learned)")
			}
			return o
		}
		n.changed++
		taken[o.Step.Lesson]++
		if steps >= 2 {
			fail("a third step")
		}
		if unchanged(p, q) || o.Step.Lesson == "" || o.Notice == "" || strings.Contains(o.Notice, "nothing was changed") {
			fail("a step that changed nothing, taught nothing or said nothing")
		}
		// Never a better picture, but for a conversion nothing demanded
		// that gives way.
		if rank(q) < rank(p) {
			if !(o.Step.Lesson == LessonPC && p.Method == Convert && q.Method != Convert && base.Method != Convert) {
				fail("a better picture after a step")
			}
			n.gaveWay++
		}
		// The thing that failed is not tried again.
		switch o.Step.Lesson {
		case LessonDevice:
			// The film's own picture is not sent again, and a converted
			// one not at the same size. (H.264 that a device could not
			// show is first converted at its own size: what trips a
			// player is more often something odd about the file than its
			// size.)
			if q.Method != Convert || p.Method == Convert && sizeRank(q.Picture.Size) >= sizeRank(p.Picture.Size) {
				fail("the device is sent what it could not show")
			}
		case LessonPC:
			if q.Method == Convert && q.Video.Rung <= p.Video.Rung {
				fail("the PC is asked for a rung it did not keep up with")
			}
		case LessonNetwork:
			if q.Method != Convert || q.Video.Rung < NumRungs && Ladder()[q.Video.Rung-1].MaxRate > o.Step.Network.MaxRate {
				fail("more is sent than the connection is trusted with")
			}
		case LessonDraw:
			if q.Subtitles == SubsDraw {
				fail("the page is asked to draw again")
			}
		}
		// What the viewer is told agrees with what changed.
		smaller := p.Picture.Size != "" && sizeRank(q.Picture.Size) < sizeRank(p.Picture.Size)
		for phrase, holds := range map[string]bool{
			"smaller":                     smaller,
			"now converts the picture":    q.Method == Convert && p.Method != Convert,
			"at a lighter setting":        q.Method == Convert && p.Method == Convert && q.Video.Rung > p.Video.Rung && !smaller,
			"with its own picture":        q.Method != Convert && p.Method == Convert,
			"puts them into the picture":  q.Subtitles == SubsBurn,
			"plain text":                  q.Subtitles == SubsTrack,
			"without their design":        has(q.Lost, "without their design"),
			"plays without them":          q.Subtitles == SubsNone && p.Subtitles == SubsDraw,
			"could not draw":              o.Step.Lesson == LessonDraw,
			"could not convert":           o.Step.Lesson == LessonPC,
			"reaching this device":        o.Step.Lesson == LessonNetwork,
			"trouble showing the picture": o.Step.Lesson == LessonDevice,
		} {
			if strings.Contains(o.Notice, phrase) && !holds {
				fail("the viewer is told \"" + phrase + "\", which is not what happened")
			}
		}
		return o
	}

	// walk plays every film on one device, with every PC and every kind of
	// trouble, and whatever trouble comes after a first step.
	walk := func(t *testing.T, dname string, base *Caps) tally {
		n := tally{taken: map[Lesson]int{}}
		for _, si := range []int{0, len(surfaces) - 1} {
			first := surfaces[si].on(base)
			for _, cont := range []string{"mp4", "matroska", "avi"} {
				for _, v := range oldVideos {
					for _, a := range []string{"aac", "dts"} {
						for _, sz := range sizes {
							for _, col := range []colour{colours[0], colours[1], colours[3]} {
								for _, sub := range subtitles {
									m := film(cont, v.codec, v.depth, a, sz, col, sub.kind)
									wish := Want{Audio: -1, Subtitle: -1, Styled: sub.styled}
									if sub.kind != "" {
										wish.Subtitle = 0
									}
									for pi, pc := range pcs {
										id := func() string {
											return strings.Join([]string{cont, v.codec, a, sz.name, col.name, sub.name, "on", dname, surfaces[si].name, "PC", string(rune('0' + pi))}, " ")
										}
										// How the film plays when nothing is wished for and no
										// subtitles are asked: converting it, or not.
										plain := DecideFor(m, first, plainWish, pc)
										for i, tr := range troubles {
											one := check(t, &n, id, m, plain, first, wish, pc, tr, 0)
											if !one.Changed {
												continue
											}
											for j, tr2 := range troubles {
												two := check(t, &n, id, m, plain, one.Caps, one.Want, one.Limits, tr2, 1)
												if !two.Changed {
													continue
												}
												// Whatever comes now, the play has had its two steps.
												tr3 := troubles[(i+j)%len(troubles)]
												if three := Replan(m, two.Caps, two.Want, two.Limits, tr3.evidence, tr3.pace, 2); three.Changed || three.Step.Lesson != "" || three.Notice != "" {
													t.Fatalf("%s: a third step: %+v", id(), three)
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
		return n
	}

	var mu sync.Mutex
	all := tally{taken: map[Lesson]int{}}
	t.Run("devices", func(t *testing.T) {
		for dname, base := range newDevices {
			t.Run(dname, func(t *testing.T) {
				t.Parallel()
				n := walk(t, dname, base)
				mu.Lock()
				defer mu.Unlock()
				all.changed, all.stayed, all.gaveWay = all.changed+n.changed, all.stayed+n.stayed, all.gaveWay+n.gaveWay
				for lesson, count := range n.taken {
					all.taken[lesson] += count
				}
			})
		}
	})
	for _, lesson := range []Lesson{LessonDevice, LessonPC, LessonNetwork, LessonDraw} {
		if all.taken[lesson] == 0 {
			t.Errorf("no step of the kind %q was taken: the test does not reach it", lesson)
		}
	}
	if all.stayed == 0 || all.gaveWay == 0 {
		t.Errorf("the test does not reach trouble that changes nothing (%d) or a conversion that gives way (%d)", all.stayed, all.gaveWay)
	}
	t.Logf("%d steps that changed the plan %v, %d of them a conversion giving way; %d times nothing could be changed", all.changed, all.taken, all.gaveWay, all.stayed)
}

// Apply hands back what to plan with and leaves what it was given alone:
// the device's report, the lessons and the speed table are shared by every
// play. Replan, which uses it, changes nothing either.
func TestApplyChangesNothingItWasGiven(t *testing.T) {
	dev := surfaces[0].on(newDevices["chrome+hevc"])
	l := middling
	l.MaxRate, l.MaxHeight = 8_000_000, 1080
	// A list with room to spare, which a careless append would write into.
	l.Trouble = append(make([]PictureKind, 0, 8), PictureKind{Codec: "vp9", BitDepth: 8, Size: Size4K})
	w := Want{Audio: 1, Subtitle: 2, Lower: 1}
	snapshot := func() string {
		raw, err := json.Marshal([]any{dev, l, l.Trouble[:cap(l.Trouble)], w, middling})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := snapshot()

	hevc10 := PictureKind{Codec: "hevc", BitDepth: 10, Size: Size1080}
	steps := []Step{
		{Lesson: LessonDevice, Device: DeviceLesson{Picture: hevc10}},
		{Lesson: LessonDevice, Device: DeviceLesson{Picture: hevc10}}, // learned twice, kept once
		{Lesson: LessonDevice, Device: DeviceLesson{Picture: PictureKind{Codec: "h264", BitDepth: 8, Size: Size4K}}},
		{Lesson: LessonDevice, Device: DeviceLesson{Picture: PictureKind{Codec: "h264", BitDepth: 8, Size: Size1080}, MaxHeight: 720}},
		{Lesson: LessonDevice}, // no kind named: nothing to learn
		{Lesson: LessonPC, PC: SpeedLesson{Class: ClassHD, Rung: 1, Encoder: "x264", Speed: 0.7}, Lower: 2},
		{Lesson: LessonNetwork, Network: NetworkLesson{MaxRate: 5_000_000, Hours: 24}},
		{Lesson: LessonDraw, Draw: Drawing{Styled: true}},
		{},
	}
	d2, w2, l2 := dev, w, l
	for _, s := range steps {
		d2, w2, l2 = s.Apply(d2, w2, l2)
	}
	for _, e := range []Evidence{DecodeTrouble, Starved, RendererFailed} {
		for _, m := range []Media{stepHD, stepOld, stepAnime} {
			for _, pace := range []float64{0, 0.8} {
				Replan(m, dev, Want{Audio: -1, Subtitle: 0}, l, e, pace, 0)
			}
		}
	}
	if after := snapshot(); after != before {
		t.Errorf("Apply or Replan changed what it was given:\n before %s\n after  %s", before, after)
	}
	// Everything was worked in. What the device reports about pictures is
	// left as it reported it: lessons about pictures are kept beside it.
	if !d2.Video["hevc10"] || !d2.Tall["hevc10"] || !d2.Video["h264"] || d2.Draw != (Drawing{Picture: true}) || !d2.PageFullscreen || !d2.HLS {
		t.Errorf("the device after every lesson: %+v", d2)
	}
	wantTrouble := []PictureKind{{Codec: "vp9", BitDepth: 8, Size: Size4K}, hevc10, {Codec: "h264", BitDepth: 8, Size: Size4K}}
	if !reflect.DeepEqual(l2.Trouble, wantTrouble) {
		t.Errorf("the kinds of picture that gave trouble: %+v, want %+v", l2.Trouble, wantTrouble)
	}
	if l2.MaxHeight != 720 || l2.MaxRate != 5_000_000 || l2.Speed[ClassHD][0] != 0.7 || l2.Speed[ClassHD][1] != 3.6 || l2.Speed[Class4KHDR] != middling.Speed[Class4KHDR] ||
		l2.Lower != 2 || !l2.Convert || !l2.ToneMap {
		t.Errorf("the limits after every lesson: %+v", l2)
	}
	// What the viewer wants is theirs: no lesson touches it.
	if w2 != w {
		t.Errorf("the wishes after every lesson: %+v", w2)
	}

	// A lesson never loosens anything: a cap already lower stays, a speed
	// already lower stays, a rung already lighter stays.
	tight := Limits{Convert: true, MaxHeight: 480, MaxRate: 2_000_000, Lower: 4, Speed: Speeds{ClassHD: {0.3}}}
	l3 := tight
	for _, s := range steps {
		_, _, l3 = s.Apply(dev, w, l3)
	}
	if l3.MaxHeight != 480 || l3.MaxRate != 2_000_000 || l3.Speed[ClassHD][0] != 0.3 || l3.Lower != 4 {
		t.Errorf("lessons loosened something: %+v", l3)
	}

	// A speed seen with one encoder says nothing about another, but the
	// lighter rung is asked for all the same.
	card := middling
	card.Encoder = "nvenc"
	_, _, l4 := steps[5].Apply(dev, Want{Audio: -1, Subtitle: -1}, card)
	if l4.Speed[ClassHD][0] != 3.2 || l4.Lower != 2 {
		t.Errorf("a software lesson on a graphics-card encoder: speeds %v, from rung %d", l4.Speed[ClassHD], l4.Lower)
	}
	// A kind of film that was never measured gets its first figure.
	_, _, l5 := Step{Lesson: LessonPC, PC: SpeedLesson{Class: Class4K, Rung: 3, Speed: 0.9}, Lower: 4}.Apply(nil, Want{}, able)
	if l5.Speed[Class4K] != ([NumRungs]float64{0, 0, 0.9, 0, 0}) || able.Speed != nil {
		t.Errorf("a first figure: %v (and the table it came from: %v)", l5.Speed, able.Speed)
	}
	// A device that has said nothing stays that way, and the lesson is
	// kept for when it does say.
	d, _, l6 := steps[0].Apply(nil, Want{}, Limits{})
	if d != nil || !reflect.DeepEqual(l6.Trouble, []PictureKind{hevc10}) {
		t.Errorf("a device that reported nothing became %+v, with the lessons %+v", d, l6.Trouble)
	}
}

// The server keeps a step as JSON and reads it back for later plays, and
// it sends an outcome to the page.
func TestStepSurvivesBeingStored(t *testing.T) {
	for _, e := range []Evidence{DecodeTrouble, Starved, RendererFailed} {
		for _, p := range []Plan{
			DecideFor(stepUHD, stepPhone, plainWish, middling),
			DecideFor(stepOld, stepPC, plainWish, middling),
			DecideFor(stepAnime, stepPC, Want{Audio: -1, Subtitle: 0}, middling),
		} {
			for _, pace := range []float64{0, 0.8} {
				s := StepDown(p, e, pace, 0)
				raw, err := json.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				var back Step
				if err := json.Unmarshal(raw, &back); err != nil || back != s {
					t.Errorf("%s: stored as %s, read back as %+v (%v)", e, raw, back, err)
				}
			}
		}
	}
	// The names a step and an outcome are stored and sent under.
	keys := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		var names []string
		for k := range m {
			names = append(names, k)
		}
		for i := range names { // few enough to sort by hand
			for j := i + 1; j < len(names); j++ {
				if names[j] < names[i] {
					names[i], names[j] = names[j], names[i]
				}
			}
		}
		return strings.Join(names, " ")
	}
	o := Replan(stepHD, stepPhone, plainWish, middling, DecodeTrouble, 0, 0)
	if got, want := keys(o), "caps changed limits notice plan step want"; got != want {
		t.Errorf("an outcome has the fields %q, want %q", got, want)
	}
	if got, want := keys(o.Step), "device draw lesson lower network pc"; got != want {
		t.Errorf("a step has the fields %q, want %q", got, want)
	}
	if got, want := keys(o.Step.Device), "maxHeight picture"; got != want {
		t.Errorf("a device lesson has the fields %q, want %q", got, want)
	}
	var back Limits
	raw, _ := json.Marshal(o.Limits)
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back.Trouble, o.Limits.Trouble) || len(back.Trouble) != 1 {
		t.Errorf("limits with a lesson stored as %s, read back as %+v (%v)", raw, back.Trouble, err)
	}
}
