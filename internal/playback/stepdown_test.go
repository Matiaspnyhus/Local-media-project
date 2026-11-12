package playback

import (
	"encoding/json"
	"strings"
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

	network := Step{Lesson: LessonNetwork, Network: NetworkLesson{MaxRate: 4_000_000, Hours: 24}}
	slower := func(n int, speed float64) Step {
		return Step{Lesson: LessonPC, PC: SpeedLesson{Class: ClassHD, Rung: n, Encoder: "x264", Speed: speed}, Lower: n + 1}
	}
	device := func(codec string, depth int, size Size, noVideo, noTall string, maxHeight int) Step {
		return Step{Lesson: LessonDevice, Device: DeviceLesson{Picture: PictureKind{Codec: codec, BitDepth: depth, Size: size},
			NoVideo: noVideo, NoTall: noTall, MaxHeight: maxHeight}}
	}
	none := Step{}

	for _, c := range []struct {
		name     string
		plan     Plan
		evidence Evidence
		pace     float64
		steps    int
		want     Step
		notice   string // a phrase the notice must contain
	}{
		// The device cannot show the picture.
		{"4K HEVC sent as it is", uhd, DecodeTrouble, 0, 0, device("hevc", 10, Size4K, "", "hevc10", 0), "a picture this large"},
		{"1080p HEVC sent as it is", hd, DecodeTrouble, 0, 0, device("hevc", 10, Size1080, "hevc10", "", 0), "converts it to a kind every device plays"},
		{"a repackaged MKV", mkv, DecodeTrouble, 0, 0, device("h264", 8, Size1080, "h264", "", 0), "converts it"},
		{"only the sound converted", dts, DecodeTrouble, 3.1, 0, device("h264", 8, Size1080, "h264", "", 0), "converts it"},
		{"a picture converted to 1080p", rung(1), DecodeTrouble, 2.9, 0, device("h264", 8, Size1080, "", "", 720), "a smaller one"},
		{"a picture converted to 1080p, second rung", rung(2), DecodeTrouble, 0, 0, device("h264", 8, Size1080, "", "", 720), "a smaller one"},
		{"a picture converted to 720p", rung(3), DecodeTrouble, 0, 0, device("h264", 8, Size720, "", "", 480), "a smaller one"},
		{"a picture converted to 480p: nothing smaller is made", rung(5), DecodeTrouble, 0, 0, none, ""},
		{"a small film converted at the best rung: it is already small", small, DecodeTrouble, 0, 0, none, ""},
		{"a file that was never inspected", Decide(Media{}, stepPC, -1), DecodeTrouble, 0, 0, none, ""},
		{"a device that has not said what it plays: the kind is known all the same", Decide(media("mp4", "hevc", 10, "aac"), nil, -1), DecodeTrouble, 0, 0,
			device("hevc", 10, "", "hevc10", "", 0), "converts it"},

		// The player ran out of film while the PC was not keeping up.
		{"converting at rung 1 at 0.8 of the film's speed", rung(1), Starved, 0.8, 0, slower(1, 0.8), "could not convert the film fast enough"},
		{"converting at rung 2 at 0.94", rung(2), Starved, 0.94, 0, slower(2, 0.94), "made lighter"},
		{"converting at the last rung at 0.5", rung(5), Starved, 0.5, 0, slower(5, 0.5), "made lighter"},
		// ... or although it was: the connection is the slow part.
		{"converting at rung 2 at exactly 0.95", rung(2), Starved, 0.95, 0, network, "reaching this device too slowly"},
		{"converting at rung 2 at 1.4", rung(2), Starved, 1.4, 0, network, "a lighter picture"},
		{"converting at rung 2, speed not known", rung(2), Starved, 0, 0, network, "a lighter picture"},
		{"converting at rung 4", rung(4), Starved, 2, 0, network, "a lighter picture"},
		{"converting at the last rung, fast enough: nothing lighter to send", rung(5), Starved, 1.2, 0, none, ""},
		{"the original sent as it is", uhd, Starved, 0, 0, network, "a lighter picture"},
		{"only the sound converted, fast", dts, Starved, 3, 0, network, "a lighter picture"},
		{"a file that was never inspected, starved", Decide(Media{}, stepPC, -1), Starved, 0, 0, network, "a lighter picture"},
		// Nothing is converted and the film still was not ready: the disk
		// is slow, and converting would not help.
		{"repackaging at 0.6 of the film's speed", mkv, Starved, 0.6, 0, none, ""},
		{"only the sound converted, at 0.9", dts, Starved, 0.9, 0, none, ""},

		// The page could not draw the subtitles.
		{"styled subtitles drawn by the page", styled, RendererFailed, 0, 0, Step{Lesson: LessonDraw, Draw: Drawing{Styled: true}}, "could not draw the subtitles"},
		{"picture subtitles drawn by the page", pictures, RendererFailed, 0, 0, Step{Lesson: LessonDraw, Draw: Drawing{Picture: true}}, "shown another way"},
		{"text subtitles: nothing is drawn", text, RendererFailed, 0, 0, none, ""},
		{"no subtitles", uhd, RendererFailed, 0, 0, none, ""},

		// At most two steps in one play, whatever happens.
		{"the second step", uhd, DecodeTrouble, 0, 1, device("hevc", 10, Size4K, "", "hevc10", 0), "a picture this large"},
		{"a third step", uhd, DecodeTrouble, 0, 2, none, ""},
		{"a third step, starved", rung(1), Starved, 0.5, 2, none, ""},
		{"a third step, subtitles", styled, RendererFailed, 0, 2, none, ""},
		{"a fourth", uhd, Starved, 0, 3, none, ""},

		// Nothing to step down from.
		{"a film that was refused", refused, DecodeTrouble, 0, 0, none, ""},
		{"a film that was refused, starved", refused, Starved, 0, 0, none, ""},
		{"trouble of a kind nobody knows", uhd, Evidence("smoke"), 0, 0, none, ""},
	} {
		got := StepDown(c.plan, c.evidence, c.pace, c.steps)
		notice := got.Notice
		got.Notice = ""
		if got != c.want {
			t.Errorf("%s, %s: step %+v, want %+v", c.name, c.evidence, got, c.want)
		}
		if (c.notice == "") != (notice == "") || !strings.Contains(notice, c.notice) || c.notice != "" && !strings.HasSuffix(notice, ".") {
			t.Errorf("%s, %s: the viewer is told %q, want something about %q", c.name, c.evidence, notice, c.notice)
		}
	}
}

// What plays after a step, for the cases that matter most.
func TestWhatPlaysAfterAStep(t *testing.T) {
	again := func(m Media, dev *Caps, w Want, l Limits, e Evidence, pace float64, steps int) (Plan, *Caps, Want, Limits) {
		t.Helper()
		s := StepDown(DecideFor(m, dev, w, l), e, pace, steps)
		if s.Lesson == "" {
			t.Fatalf("no step was taken")
		}
		dev, w, l = s.Apply(dev, w, l)
		return DecideFor(m, dev, w, l), dev, w, l
	}

	// A phone that claims 4K and cannot show it gets 720p: the middling
	// PC does not keep up with 4K HDR at 1080p.
	p, _, _, _ := again(stepUHD, stepPhone, plainWish, middling, DecodeTrouble, 0, 0)
	if p.Method != Convert || p.Video.Rung != 3 || !has(p.Reasons, "does not say it plays smoothly") {
		t.Errorf("4K after decode trouble: %+v", p)
	}
	// 1080p of the same format is still sent as it is.
	uhdLesson := StepDown(DecideFor(stepUHD, stepPhone, plainWish, middling), DecodeTrouble, 0, 0)
	dev, w, l := uhdLesson.Apply(stepPhone, plainWish, middling)
	if p = DecideFor(stepHD, dev, w, l); p.Method != Direct {
		t.Errorf("1080p after decode trouble with 4K: %+v", p)
	}

	// A repackaged film the phone cannot show is converted; if the phone
	// cannot show that either it is sent smaller; then nothing more.
	p, dev, w, l = again(stepMKV, stepPhone, plainWish, middling, DecodeTrouble, 0, 0)
	if p.Method != Convert || p.Video.Rung != 1 || p.Video.Height != 1080 {
		t.Errorf("an MKV after decode trouble: %+v", p)
	}
	p, dev, w, l = again(stepMKV, dev, w, l, DecodeTrouble, 0, 1)
	if p.Method != Convert || p.Video.Rung != 3 || p.Video.Height != 720 {
		t.Errorf("an MKV after decode trouble twice: %+v", p)
	}
	if s := StepDown(p, DecodeTrouble, 0, 2); s.Lesson != "" {
		t.Errorf("a third step: %+v", s)
	}

	// A PC that turns out slower than measured: the next rung, and the
	// figure is kept, so the film does not start at the slow rung again.
	p, _, w, l = again(stepOld, stepPC, plainWish, middling, Starved, 0.8, 0)
	if p.Method != Convert || p.Video.Rung != 2 || w.Lower != 2 || l.Speed[ClassHD][0] != 0.8 {
		t.Errorf("after converting too slowly at rung 1: %+v, lighter=%d, speeds %v", p, w.Lower, l.Speed[ClassHD])
	}
	if p = DecideFor(stepOld, stepPC, plainWish, l); p.Video.Rung != 2 {
		t.Errorf("the next film on that PC: %+v", p)
	}
	// Too slow at the last rung: the honest answer is no, with the figure.
	p, _, _, _ = again(stepOld, stepPC, lighter(5), middling, Starved, 0.5, 0)
	if p.Playable != No || !has(p.Reasons, "at best it manages 0.5 seconds of film a second") {
		t.Errorf("after converting too slowly at the last rung: %+v", p)
	}

	// A connection that starves the player: the lightest picture, for the
	// film that was sent as it is and for the one that was converted.
	p, _, _, l = again(stepUHD, stepPhone, plainWish, middling, Starved, 0, 0)
	if p.Method != Convert || p.Video.Rung != 5 || l.MaxRate != 4_000_000 {
		t.Errorf("4K after starving: %+v", p)
	}
	p, _, _, _ = again(stepOld, stepPC, plainWish, middling, Starved, 2.5, 0)
	if p.Method != Convert || p.Video.Rung != 5 {
		t.Errorf("a converted film after starving: %+v", p)
	}
	// Where this PC cannot convert, the lesson changes nothing; the
	// server can see that the plan is the same and spare the restart.
	p, _, _, _ = again(stepUHD, stepPhone, plainWish, Limits{}, Starved, 0, 0)
	if p.Method != Direct {
		t.Errorf("4K after starving, on a PC that cannot convert: %+v", p)
	}

	// Subtitles the page failed to draw are put into the picture, or
	// shown as plain text if the viewer prefers; the other kind is still
	// drawn.
	sub := Want{Audio: -1, Subtitle: 0}
	p, dev, _, _ = again(stepAnime, stepPC, sub, middling, RendererFailed, 0, 0)
	if p.Method != Convert || p.Subtitles != SubsBurn {
		t.Errorf("styled subtitles after the renderer failed: %+v", p)
	}
	if p = DecideFor(stepDisc, dev, sub, middling); p.Subtitles != SubsDraw {
		t.Errorf("picture subtitles after the styled renderer failed: %+v", p)
	}
	p, _, _, _ = again(stepAnime, stepPC, Want{Audio: -1, Subtitle: 0, Styled: Plain}, middling, RendererFailed, 0, 0)
	if p.Method != Direct || p.Subtitles != SubsTrack {
		t.Errorf("styled subtitles as plain text after the renderer failed: %+v", p)
	}
	p, _, _, _ = again(stepDisc, stepPC, sub, middling, RendererFailed, 0, 0)
	if p.Method != Convert || p.Subtitles != SubsBurn {
		t.Errorf("picture subtitles after the renderer failed: %+v", p)
	}
}

// Steps only go down. For a spread of films, devices, PCs and kinds of
// trouble: after a step the viewer never gets a better picture than
// before, the thing that failed is not tried again, and a play takes at
// most two steps.
//
// The speed tables here are like real ones in that a lighter rung is never
// slower than a heavier one. With a table where it is, a cap can leave
// only rungs that are too slow, and a lesson that is advice then gives way
// to the original; see TestLessonsAreAdvice.
func TestStepsOnlyGoDown(t *testing.T) {
	busy := middling
	busy.Busy = 1
	type trouble struct {
		evidence Evidence
		pace     float64
	}
	troubles := []trouble{{DecodeTrouble, 0}, {Starved, 0}, {Starved, 0.5}, {Starved, 1.2}, {RendererFailed, 0}}
	taken := map[Lesson]int{}
	n := 0
	for dname, base := range newDevices {
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
									for pi, pc := range []Limits{able, middling, busy} {
										for _, tr := range troubles {
											dev, w, l := first, wish, pc
											p := DecideFor(m, dev, w, l)
											for steps := 0; ; steps++ {
												s := StepDown(p, tr.evidence, tr.pace, steps)
												if s.Lesson == "" {
													break
												}
												n++
												taken[s.Lesson]++
												was := p
												dev, w, l = s.Apply(dev, w, l)
												q := DecideFor(m, dev, w, l)
												fail := func(what string) {
													t.Helper()
													t.Fatalf("%s %s/%d %s %s %s, %s, on %s (surface %d), PC %d, %s at pace %.1f, step %d: %s %d -> %s %d %v: %s",
														cont, v.codec, v.depth, a, sz.name, col.name, sub.name, dname, si, pi, tr.evidence, tr.pace, steps+1,
														was.Method, was.Video.Rung, q.Method, q.Video.Rung, q.Reasons, what)
												}
												if steps >= 2 {
													fail("a third step")
												}
												if rank(q) < rank(p) {
													fail("a better picture after a step")
												}
												switch s.Lesson {
												case LessonDevice:
													// The film's own picture is not sent again, and a
													// converted one not at the same size. (H.264 that a
													// device could not show is first converted at its own
													// size: what trips a player is more often something odd
													// about the file than its size.)
													if q.Playable != No && (q.Method != Convert || p.Method == Convert && q.Picture == p.Picture) {
														fail("the device is sent what it could not show")
													}
												case LessonPC:
													if q.Playable != No && q.Video.Rung <= p.Video.Rung {
														fail("the PC is asked for a rung it did not keep up with")
													}
												case LessonNetwork:
													if q.Method == Convert && q.Video.Rung != NumRungs {
														fail("more is sent than the connection is trusted with")
													}
												case LessonDraw:
													if q.Subtitles == SubsDraw {
														fail("the page is asked to draw again")
													}
												}
												p = q
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
	for _, lesson := range []Lesson{LessonDevice, LessonPC, LessonNetwork, LessonDraw} {
		if taken[lesson] == 0 {
			t.Errorf("no step of the kind %q was taken: the test does not reach it", lesson)
		}
	}
	t.Logf("%d steps followed: %v", n, taken)
}

// Apply hands back what to plan with and leaves what it was given alone:
// the device's report and the speed table are shared by every play.
func TestApplyChangesNothingItWasGiven(t *testing.T) {
	dev := surfaces[0].on(newDevices["chrome+hevc"])
	l := middling
	l.MaxRate, l.MaxHeight = 8_000_000, 1080
	w := Want{Audio: 1, Subtitle: 2, Lower: 1}
	snapshot := func() string {
		raw, err := json.Marshal([]any{dev, l, w, middling})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := snapshot()

	steps := []Step{
		{Lesson: LessonDevice, Device: DeviceLesson{NoVideo: "hevc10"}},
		{Lesson: LessonDevice, Device: DeviceLesson{NoTall: "h264"}},
		{Lesson: LessonDevice, Device: DeviceLesson{MaxHeight: 720}},
		{Lesson: LessonPC, PC: SpeedLesson{Class: ClassHD, Rung: 1, Encoder: "x264", Speed: 0.7}, Lower: 2},
		{Lesson: LessonNetwork, Network: NetworkLesson{MaxRate: 4_000_000, Hours: 24}},
		{Lesson: LessonDraw, Draw: Drawing{Styled: true}},
		{},
	}
	d2, w2, l2 := dev, w, l
	for _, s := range steps {
		d2, w2, l2 = s.Apply(d2, w2, l2)
	}
	if after := snapshot(); after != before {
		t.Errorf("Apply changed what it was given:\n before %s\n after  %s", before, after)
	}
	// Everything was worked in.
	if d2.Video["hevc10"] || d2.Tall["hevc10"] || d2.Tall["h264"] || !d2.Video["h264"] || !d2.Tall["hevc"] || d2.Draw != (Drawing{Picture: true}) ||
		!d2.PageFullscreen || !d2.HLS {
		t.Errorf("the device after every lesson: %+v", d2)
	}
	if l2.MaxHeight != 720 || l2.MaxRate != 4_000_000 || l2.Speed[ClassHD][0] != 0.7 || l2.Speed[ClassHD][1] != 3.6 || l2.Speed[Class4KHDR] != middling.Speed[Class4KHDR] ||
		!l2.Convert || !l2.ToneMap {
		t.Errorf("the limits after every lesson: %+v", l2)
	}
	if w2 != (Want{Audio: 1, Subtitle: 2, Lower: 2}) {
		t.Errorf("the wishes after every lesson: %+v", w2)
	}

	// A lesson never loosens anything: a cap already lower stays, a speed
	// already lower stays, a lighter wish stays.
	tight := Limits{Convert: true, MaxHeight: 480, MaxRate: 2_000_000, Speed: Speeds{ClassHD: {0.3}}}
	d3, w3, l3 := dev, Want{Audio: -1, Subtitle: -1, Lower: 4}, tight
	for _, s := range steps {
		d3, w3, l3 = s.Apply(d3, w3, l3)
	}
	if l3.MaxHeight != 480 || l3.MaxRate != 2_000_000 || l3.Speed[ClassHD][0] != 0.3 || w3.Lower != 4 {
		t.Errorf("lessons loosened something: %+v, lighter=%d", l3, w3.Lower)
	}
	_ = d3

	// A speed seen with one encoder says nothing about another, but the
	// lighter rung is asked for all the same.
	card := middling
	card.Encoder = "nvenc"
	_, w4, l4 := steps[3].Apply(dev, Want{Audio: -1, Subtitle: -1}, card)
	if l4.Speed[ClassHD][0] != 3.2 || w4.Lower != 2 {
		t.Errorf("a software lesson on a graphics-card encoder: speeds %v, lighter=%d", l4.Speed[ClassHD], w4.Lower)
	}
	// A kind of film that was never measured gets its first figure.
	_, _, l5 := Step{Lesson: LessonPC, PC: SpeedLesson{Class: Class4K, Rung: 3, Speed: 0.9}, Lower: 4}.Apply(nil, Want{}, able)
	if l5.Speed[Class4K] != ([NumRungs]float64{0, 0, 0.9, 0, 0}) || able.Speed != nil {
		t.Errorf("a first figure: %v (and the table it came from: %v)", l5.Speed, able.Speed)
	}
	// A device that has said nothing stays that way.
	if d, _, _ := steps[0].Apply(nil, Want{}, Limits{}); d != nil {
		t.Errorf("a device that reported nothing became %+v", d)
	}
}

// The server keeps a step as JSON and reads it back for later plays.
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
}
