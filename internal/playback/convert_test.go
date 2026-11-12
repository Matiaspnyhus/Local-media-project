package playback

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The same devices as in M3's table, with what a device can say since
// pictures can be converted: which formats it plays in 4K, and whether it
// shows Dolby Vision profile 5.
var newDevices = map[string]*Caps{
	"chrome":      says(devices["chrome"], "h264 vp9 av1", false),
	"chrome+hevc": says(devices["chrome+hevc"], "h264 hevc hevc10 vp9 av1", false),
	"safari":      says(devices["safari"], "h264 hevc hevc10", true),
	"firefox":     says(devices["firefox"], "h264", false),
	"old-tv":      says(devices["old-tv"], "", false),
}

func says(c *Caps, tall string, dovi bool) *Caps {
	d := *c
	d.Tall = map[string]bool{}
	for _, k := range strings.Fields(tall) {
		d.Tall[k] = true
	}
	if dovi {
		d.Video = map[string]bool{"dovi5": true}
		for k, v := range c.Video {
			d.Video[k] = v
		}
	}
	return &d
}

// surface is where the film is shown: whether the page itself can go
// fullscreen, and what the page can draw over the film.
type surface struct {
	name       string
	fullscreen bool
	draw       Drawing
}

var surfaces = []surface{
	{"a page that goes fullscreen and draws everything", true, Drawing{Styled: true, Picture: true}},
	{"a page that goes fullscreen and draws styled subtitles only", true, Drawing{Styled: true}},
	{"a page that goes fullscreen and draws nothing", true, Drawing{}},
	{"an iPhone, where the page draws but only the video goes fullscreen", false, Drawing{Styled: true, Picture: true}},
}

func (s surface) on(c *Caps) *Caps {
	d := *c
	d.PageFullscreen, d.Draw = s.fullscreen, s.draw
	return &d
}

type pictureSize struct {
	name          string
	width, height int
}

type colour struct {
	name, transfer string
	dovi           int
}

type subtitleWish struct {
	name   string
	kind   SubtitleKind // "" = no subtitles
	styled Styling
}

// What the table grew by.
var (
	sizes     = []pictureSize{{"480p", 854, 480}, {"1080p", 1920, 1080}, {"4K", 3840, 2160}}
	colours   = []colour{{"SDR", SDR, 0}, {"PQ", PQ, 0}, {"HLG", HLG, 0}, {"Dolby Vision 5", PQ, 5}}
	subtitles = []subtitleWish{{"no subtitles", "", ""}, {"text subtitles", TextSubs, ""}, {"styled subtitles as designed", StyledSubs, Designed},
		{"styled subtitles as plain text", StyledSubs, Plain}, {"picture subtitles", PictureSubs, ""}}
)

// film is media() with a size, a colour, surround where the sound format
// usually has it, and one subtitle to choose.
func film(container, video string, depth int, audio string, size pictureSize, col colour, sub SubtitleKind) Media {
	var m Media
	if audio == "" {
		m = media(container, video, depth)
	} else {
		m = media(container, video, depth, audio)
		m.Audio[0].Channels = 2
		switch audio {
		case "ac3", "eac3", "dts", "truehd":
			m.Audio[0].Channels = 6
		}
	}
	v := &m.Video[0]
	v.Width, v.Height, v.FrameRate, v.Transfer, v.DoVi = size.width, size.height, 24, col.transfer, col.dovi
	if sub != "" {
		m.Subtitles = []Subtitle{{Key: "e0", Kind: sub}}
	}
	return m
}

// able is a PC that converts anything and has not been measured.
var able = Limits{Convert: true, ToneMap: true, DoViTone: true}

func measured(l Limits, hd, hdr, k4, k4hdr [NumRungs]float64) Limits {
	l.Speed = Speeds{ClassHD: hd, ClassHDR: hdr, Class4K: k4, Class4KHDR: k4hdr}
	return l
}

func every(speed float64) [NumRungs]float64 {
	return [NumRungs]float64{speed, speed, speed, speed, speed}
}

// middling is a four-core PC as the benchmark might find it: ordinary
// films with room to spare, 4K HDR only at the lighter rungs.
var middling = measured(able, [NumRungs]float64{3.2, 3.6, 5.0, 6.0, 9.0}, [NumRungs]float64{1.4, 1.6, 2.8, 3.4, 5.0},
	[NumRungs]float64{1.2, 1.4, 2.6, 3.1, 4.6}, [NumRungs]float64{0.7, 0.8, 1.3, 1.6, 2.3})

// fast is a PC that converts anything at four times the speed it plays.
var fast = measured(able, every(4), every(4), every(4), every(4))

// The PCs the table is walked for. The first three meet every combination
// there is. The others differ from those only in how a picture is
// converted, which has nothing to do with where subtitles are drawn, so
// they meet every film on the two surfaces that differ most (the first and
// the last) and no others; that keeps the table to a few seconds.
//
// sweep names a surface on which every lighter picture there is gets asked
// for as well (Want.Lower 1 to 6), to check that asking never gives a
// better picture; -1 for none.
var pcs = []struct {
	name  string
	l     Limits
	full  bool
	sweep int
}{
	{"a PC that cannot convert pictures", Limits{}, true, -1},
	{"a PC that converts everything, not yet measured", able, true, 0},
	{"a middling PC that is converting one film already", func() Limits { l := middling; l.Busy = 1; return l }(), true, len(surfaces) - 1},
	{"a PC whose converter cannot turn HDR into normal colour", Limits{Convert: true}, false, -1},
	{"a PC without the Dolby Vision filter", Limits{Convert: true, ToneMap: true}, false, -1},
	{"a fast PC", fast, false, -1},
	{"a PC too slow for anything", measured(able, every(0.5), every(0.5), every(0.5), every(0.5)), false, -1},
	{"a device with lessons: a slow connection, a small picture, no HDR", func() Limits {
		l := able
		l.MaxRate, l.MaxHeight, l.NoHDR = 4_000_000, 720, true
		return l
	}(), false, -1},
}

// rank orders answers by what the viewer gets: the film's own picture,
// then each rung of the ladder, then nothing.
func rank(p Plan) int {
	switch {
	case p.Playable == No:
		return NumRungs + 1
	case p.Method == Convert:
		return p.Video.Rung
	}
	return 0
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func has(list []string, phrase string) bool {
	for _, s := range list {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

// broken checks one answer against the rules that must hold for every
// combination and names the first one it breaks ("" for none). It works
// the facts out for itself, from the file and the device, and takes what
// M3 would have answered (m3, from decideM3) as the measure of "would play
// without converting the picture".
func broken(m Media, dev *Caps, w Want, l Limits, p, m3 Plan) string {
	v := m.Video[0]
	key := v.Codec
	if v.BitDepth > 8 && v.Codec == "hevc" {
		key = "hevc10"
	}
	if v.BitDepth > 8 && v.Codec == "h264" {
		key = "h264-10"
	}
	// What the device makes of the film's own picture.
	shows := dev.Video[key]
	large := v.Height > 1088 || v.Width > 1920
	tooLarge := shows && large && !dev.Tall[key]
	dovi := v.DoVi == 5
	wrongColours := shows && dovi && !dev.Video["dovi5"]
	hdr := v.Transfer == PQ || v.Transfer == HLG || dovi

	plays := m3.Playable == Yes

	// How the subtitles would have to be shown.
	var kind SubtitleKind
	mode := SubsNone
	if w.Subtitle >= 0 {
		kind = m.Subtitles[w.Subtitle].Kind
		switch {
		case kind == StyledSubs && dev.Draw.Styled && dev.PageFullscreen, kind == PictureSubs && dev.Draw.Picture && dev.PageFullscreen:
			mode = SubsDraw
		case kind == PictureSubs, kind == StyledSubs && w.Styled != Plain:
			mode = SubsBurn
		default:
			mode = SubsTrack
		}
	}

	// must: nothing but a converted picture will do. may: the design of
	// the subtitles is worth converting for, but not worth losing the film
	// over. advice: a lesson would rather have the picture converted.
	must := !plays || tooLarge || wrongColours || mode == SubsBurn && kind == PictureSubs || w.Lower > 0
	may := mode == SubsBurn && kind == StyledSubs
	advice := l.MaxRate > 0 && (m.BitRate == 0 || m.BitRate > l.MaxRate) || l.NoHDR && hdr

	// Whether this PC and this device can do a conversion at all.
	var a *Track
	if m3.AudioTrack >= 0 {
		a = &m.Audio[m3.AudioTrack]
	}
	keeps := a == nil || dev.Audio[a.Codec] && (a.Codec == "ac3" || a.Codec == "eac3" ||
		a.Codec == "aac" && (m.Container == "matroska" || m.Container == "mp4" || m.Container == "mov"))
	can := l.Convert && dev.HLS && (keeps || dev.Audio["aac"])
	switch {
	case dovi:
		can = can && l.DoViTone
	case hdr:
		can = can && l.ToneMap
	}
	// Whether a rung is certain to be found: nothing measured means nothing
	// is known to be too slow.
	certain := can && l.Speed == nil && w.Lower <= NumRungs
	tooSlow := p.Playable == No && len(p.Reasons) > 0 && strings.HasPrefix(p.Reasons[len(p.Reasons)-1], "This PC cannot convert this film fast enough to watch")

	if p.Reasons == nil || p.Lost == nil {
		return "a list is missing (the page would be sent null)"
	}
	if (p.Method == None) != (p.Playable == No) {
		return "the method and the answer disagree"
	}
	if (p.Method == Direct) != (len(p.Reasons) == 0) {
		return "only the original sent as it is comes without a reason, and it always does"
	}
	if p.TooSlow != tooSlow || tooSlow != (p.Explain() == "This PC cannot convert this film fast enough to watch.") {
		return "the answer is unsure whether this PC is too slow"
	}
	for _, said := range [3][]string{{p.Explain()}, p.Reasons, p.Lost} {
		for _, s := range said {
			if !strings.HasSuffix(s, ".") {
				return fmt.Sprintf("not a sentence: %q", s)
			}
		}
	}

	// Never convert when direct, remux or audio would play.
	sameAsBefore := p.Method == m3.Method && p.AudioTrack == m3.AudioTrack && p.AudioCopy == m3.AudioCopy && sameStrings(p.Reasons, m3.Reasons)
	switch {
	case p.Method == Convert && !must && !may && !advice:
		return "the picture is converted although the film would have played without"
	case p.Method == Convert && !can:
		return "the picture is converted although this PC or this device cannot do that"
	case p.Method != Convert && p.Playable != No && must:
		return "the film's own picture is sent although only a converted one will do"
	case p.Method != Convert && p.Playable != No && !sameAsBefore:
		return fmt.Sprintf("an answer without conversion that differs from M3's (%s %v)", m3.Method, m3.Reasons)
	case plays && !must && p.Playable != Yes:
		return "refused although the film would have played without converting the picture"
	case must && !can && p.Playable != No:
		return "plays although the picture must be converted and cannot be"
	case must && can && p.Method != Convert && !tooSlow:
		return "refused although the picture could have been converted"
	case tooSlow && (certain || !can):
		return "called too slow where speed cannot be the reason"
	case plays && !must && (advice || may) && certain && p.Method != Convert:
		return "a lesson's advice or the subtitles' design was dropped although this PC could have converted"
	}

	// Subtitles: burn implies convert, draw only with Draw and PageFullscreen.
	fellBack := may && !must && p.Subtitles == SubsTrack && p.Method != Convert && has(p.Lost, "plain text") && !certain
	switch {
	case p.Playable == No && (p.Subtitles != SubsNone || p.SubtitleKind != ""):
		return "subtitles on a film that does not play"
	case p.Playable == No:
	case p.Subtitles == SubsBurn && p.Method != Convert:
		return "subtitles put into a picture that is not converted"
	case p.Subtitles == SubsDraw && !(dev.PageFullscreen && (kind == StyledSubs && dev.Draw.Styled || kind == PictureSubs && dev.Draw.Picture)):
		return "subtitles drawn by a page that cannot draw them, or cannot keep them in fullscreen"
	case p.Subtitles != mode && !fellBack:
		return fmt.Sprintf("subtitles shown as %q, want %q", p.Subtitles, mode)
	case p.SubtitleKind != kind:
		return fmt.Sprintf("subtitles of kind %q, want %q", p.SubtitleKind, kind)
	}

	// What the answer says about the converted picture, and what is lost.
	lost := []string{}
	if p.Method == Convert {
		n := p.Video.Rung
		if n < 1 || n > NumRungs {
			return "no such rung"
		}
		r := Ladder()[n-1]
		class, need := ClassHD, 1.5
		switch {
		case large && hdr:
			class, need = Class4KHDR, 1.2
		case large:
			class = Class4K
		case hdr:
			class = ClassHDR
		}
		height := min(v.Height, r.Height) // every picture in the table is 16:9
		switch {
		case n < w.Lower:
			return "a better picture than was asked for"
		case l.Speed[class][n-1] > 0 && l.Speed[class][n-1]/float64(l.Busy+1) < need:
			return "a rung this PC was measured too slow for"
		case n < NumRungs && l.MaxRate > 0 && r.MaxRate > l.MaxRate:
			return "more bits a second than the connection is trusted with"
		case n < NumRungs && l.MaxHeight > 0 && height > l.MaxHeight:
			return "a larger picture than the device is trusted with"
		case p.Video != (Video{Rung: n, Height: height, Encoder: "x264", HDRToSDR: hdr, Class: class}):
			return fmt.Sprintf("the converted picture is described as %+v", p.Video)
		case p.Picture.Codec != "h264" || p.Picture.BitDepth != 8:
			return "the converted picture is not said to be 8-bit H.264"
		case p.AudioCopy != keeps:
			return "the sound beside a converted picture is passed on, or converted, wrongly"
		}
		if height < v.Height {
			lost = append(lost, sizeName(v)+" picture shown as "+strconv.Itoa(r.Height)+"p.")
		}
		switch {
		case dovi:
			lost = append(lost, "Dolby Vision shown as normal colour.")
		case hdr:
			lost = append(lost, "HDR shown as normal colour.")
		}
		if !keeps && a.Channels > 2 {
			lost = append(lost, "Surround sound is played as stereo.")
		}
	} else {
		if p.Video != (Video{}) {
			return "a converted picture is described although nothing is converted"
		}
		if p.Method == Audio && a.Channels > 2 {
			lost = append(lost, "Surround sound is played as stereo.")
		}
		if fellBack && p.Playable != No {
			lost = append(lost, "The subtitles are shown as plain text, without their design.")
		}
	}
	if !sameStrings(p.Lost, lost) {
		return fmt.Sprintf("what is lost is given as %q, want %q", p.Lost, lost)
	}
	return ""
}

func sizeName(v Track) string {
	if v.Height > 1088 {
		return "4K"
	}
	return strconv.Itoa(v.Height) + "p"
}

// M3's table of 1,890 combinations of package, picture, sound and device,
// grown by the picture's size and colour, the subtitles asked for, where
// the film is shown and what the PC can do. The rules in broken must hold
// for every one of them, whatever the answer is; and asking for a lighter
// picture must never give a better one.
func TestRulesHoldForEveryCombination(t *testing.T) {
	combinations, n, failed := 0, 0, 0
	answers := map[Method]int{}
	last := len(surfaces) - 1
	for dname, base := range newDevices {
		var shown []*Caps
		for _, s := range surfaces {
			shown = append(shown, s.on(base))
		}
		for _, cont := range oldContainers {
			for _, v := range oldVideos {
				for _, a := range oldAudios {
					for _, sz := range sizes {
						for _, col := range colours {
							// What M3 would have answered depends on none
							// of what follows.
							m3 := decideM3(film(cont, v.codec, v.depth, a, sz, col, ""), base, -1)
							for _, sub := range subtitles {
								m := film(cont, v.codec, v.depth, a, sz, col, sub.kind)
								w := Want{Audio: -1, Subtitle: -1, Styled: sub.styled}
								if sub.kind != "" {
									w.Subtitle = 0
								}
								for si, dev := range shown {
									if sub.kind == "" && si > 0 {
										break // without subtitles the surface changes nothing
									}
									combinations++
									for _, pc := range pcs {
										if !pc.full && si != 0 && si != last {
											continue
										}
										lightest := 0
										if pc.sweep == si || pc.sweep >= 0 && sub.kind == "" {
											lightest = NumRungs + 1
										}
										before := 0
										for w.Lower = 0; w.Lower <= lightest; w.Lower++ {
											p := DecideFor(m, dev, w, pc.l)
											n++
											answers[p.Method]++
											msg := broken(m, dev, w, pc.l, p, m3)
											if msg == "" && rank(p) < before {
												msg = "asking for a lighter picture gave a better one"
											}
											before = rank(p)
											if msg != "" {
												failed++
												t.Errorf("%s %s/%d %s, %s %s, %s, on %s (%s), %s, lighter=%d -> %s %v: %s", cont, v.codec, v.depth, a, sz.name, col.name,
													sub.name, dname, surfaces[si].name, pc.name, w.Lower, p.Method, p.Reasons, msg)
												if failed >= 25 {
													t.Fatalf("stopping after %d broken rules", failed)
												}
											}
										}
										w.Lower = 0
									}
								}
							}
						}
					}
				}
			}
		}
	}
	for _, method := range []Method{Direct, Remux, Audio, Convert, None} {
		if answers[method] == 0 {
			t.Errorf("no combination was answered %q: the table does not reach it", method)
		}
	}
	// 1,890 x 3 sizes x 4 colours x (no subtitles, or one of four kinds of
	// wish on one of four surfaces).
	if want := 1890 * len(sizes) * len(colours) * (1 + (len(subtitles)-1)*len(surfaces)); combinations != want {
		t.Errorf("%d combinations walked, want %d", combinations, want)
	}
	t.Logf("%d combinations, %d answers checked: %v", combinations, n, answers)
}

// The planner never picks a setting this PC was measured too slow for,
// whatever the measurements are: this draws speed tables at random and
// checks every pick against the bar (1.5 times the film's own speed, 1.2
// for 4K HDR, shared with what the PC is converting already). It also
// checks that the pick is the best one allowed, that "too slow" is only
// said when every allowed rung was measured too slow, with the best
// figure quoted, and that neither a lighter wish nor a busier PC ever
// gives a better picture. The seed is fixed, so a failure can be repeated.
func TestNoRungBelowItsBar(t *testing.T) {
	rnd := rand.New(rand.NewSource(20261112))
	sources := []struct{ w, h int }{{0, 0}, {854, 480}, {1280, 720}, {1920, 1080}, {3840, 2160}}
	heightCaps := []int{0, 0, 480, 720, 1080}
	rateCaps := []int{0, 0, 2_000_000, 4_000_000, 6_000_000, 11_000_000}
	classes := []Class{ClassHD, ClassHDR, Class4K, Class4KHDR}
	// Normal colour twice, so that it comes up as often as it does in a
	// library, and Dolby Vision that does not say it is HDR.
	drawn := append([]colour{colours[0], {"Dolby Vision 5, not marked as HDR", "", 5}}, colours...)
	dev := newDevices["chrome"]
	picked := map[int]int{}
	for i := 0; i < 10000; i++ {
		src := sources[rnd.Intn(len(sources))]
		col := drawn[rnd.Intn(len(drawn))]
		// A film every device must have converted: DivX in an AVI.
		m := media("avi", "mpeg4", 8, "mp3")
		v := &m.Video[0]
		v.Width, v.Height, v.Transfer, v.DoVi = src.w, src.h, col.transfer, col.dovi
		l := able
		l.Speed = Speeds{}
		for _, c := range classes {
			if rnd.Intn(8) == 0 {
				continue // this kind of film was never measured
			}
			var row [NumRungs]float64
			for n := range row {
				if rnd.Intn(4) > 0 {
					row[n] = rnd.Float64() * 4
				}
			}
			l.Speed[c] = row
		}
		l.MaxHeight, l.MaxRate = heightCaps[rnd.Intn(len(heightCaps))], rateCaps[rnd.Intn(len(rateCaps))]
		busy := rnd.Intn(3)

		large, hdr := src.h > 1080, col.transfer == PQ || col.transfer == HLG || col.dovi == 5
		class, need, start := ClassHD, 1.5, 1
		switch {
		case large && hdr:
			class, need, start = Class4KHDR, 1.2, 3
		case large:
			class, start = Class4K, 3
		case hdr:
			class, start = ClassHDR, 3
		}
		for l.Busy = 0; l.Busy <= busy; l.Busy++ {
			before := 0
			for lower := 0; lower <= NumRungs+1; lower++ {
				p := DecideFor(m, dev, Want{Audio: -1, Subtitle: -1, Lower: lower}, l)
				where := func() string {
					return fmt.Sprintf("draw %d: %dx%d %s, speeds %v, at most %d lines and %d bits, %d others, lighter=%d -> rung %d %v",
						i, src.w, src.h, col.name, l.Speed[class], l.MaxHeight, l.MaxRate, l.Busy, lower, p.Video.Rung, p.Reasons)
				}
				if p.Method != Convert && p.Playable != No {
					t.Fatalf("%s: neither converted nor refused", where())
				}
				if rank(p) < before {
					t.Fatalf("%s: a lighter wish gave a better picture (before: %d)", where(), before)
				}
				before = rank(p)
				if l.Busy > 0 {
					calm := l
					calm.Busy--
					if q := DecideFor(m, dev, Want{Audio: -1, Subtitle: -1, Lower: lower}, calm); rank(p) < rank(q) {
						t.Fatalf("%s: a busier PC gave a better picture", where())
					}
				}
				// Which rungs were allowed, and how each stands.
				best, bestFigure := 0, 0.0
				for n := 1; n <= NumRungs; n++ {
					r := Ladder()[n-1]
					height := r.Height
					if src.h > 0 && src.h < height {
						height = src.h
					}
					if n < lower || n < NumRungs && (l.MaxRate > 0 && r.MaxRate > l.MaxRate || l.MaxHeight > 0 && height > l.MaxHeight) {
						continue
					}
					speed := l.Speed[class][n-1] / float64(l.Busy+1)
					if speed == 0 && n >= start || speed >= need {
						if best == 0 {
							best = n
						}
						continue
					}
					bestFigure = math.Max(bestFigure, speed)
				}
				if p.Method == Convert {
					picked[p.Video.Rung]++
					if s := l.Speed[class][p.Video.Rung-1]; s > 0 && s/float64(l.Busy+1) < need {
						t.Fatalf("%s: picked a rung measured at %.2f, below its bar of %.1f", where(), s/float64(l.Busy+1), need)
					}
					if p.Video.Rung != best {
						t.Fatalf("%s: want rung %d, the best one allowed", where(), best)
					}
					continue
				}
				picked[0]++
				if best != 0 {
					t.Fatalf("%s: refused although rung %d would do", where(), best)
				}
				last := p.Reasons[len(p.Reasons)-1]
				if !strings.HasPrefix(last, "This PC cannot convert this film fast enough to watch") {
					t.Fatalf("%s: the refusal does not say the PC is too slow", where())
				}
				if lower <= NumRungs {
					// The figure quoted is the best one measured, cut off
					// to one decimal: never more than was measured, and
					// so never the bar itself.
					var quoted, needed float64
					if _, err := fmt.Sscanf(strings.TrimPrefix(last, "This PC cannot convert this film fast enough to watch: "),
						"at best it manages %f seconds of film a second, and it takes %f to keep up", &quoted, &needed); err != nil {
						t.Fatalf("%s: the refusal quotes no figure: %v", where(), err)
					}
					if quoted > bestFigure || quoted < bestFigure-0.1001 || quoted >= need || needed != need {
						t.Fatalf("%s: the refusal quotes %.1f against %.1f; measured %.4f against %.1f", where(), quoted, needed, bestFigure, need)
					}
				}
				if (l.Busy > 0) != strings.Contains(last, "converting for") {
					t.Fatalf("%s: the refusal is wrong about what else the PC is doing", where())
				}
			}
		}
	}
	for n := 0; n <= NumRungs; n++ {
		if picked[n] == 0 {
			t.Errorf("the draws never led to rung %d (0 = too slow): %v", n, picked)
		}
	}
	t.Logf("picked, by rung (0 = too slow): %v", picked)
}

// Nothing measured: an ordinary film starts at the best rung, 4K and HDR
// at 720p, since those are the ones a four-core PC may not keep up with.
func TestWhereAnUnmeasuredPCStarts(t *testing.T) {
	for _, c := range []struct {
		name          string
		width, height int
		transfer      string
		rung, lines   int
	}{
		{"1080p", 1920, 1080, SDR, 1, 1080},
		{"a DVD", 720, 576, SDR, 1, 576},
		{"a size that was not read", 0, 0, SDR, 1, 1080},
		{"a wide film: 1080p by its width, 800 lines", 1920, 800, SDR, 1, 800},
		{"1080p HDR", 1920, 1080, PQ, 3, 720},
		{"1080p HLG", 1920, 1080, HLG, 3, 720},
		{"4K", 3840, 2160, SDR, 3, 720},
		{"4K HDR", 3840, 2160, PQ, 3, 720},
		{"a wide 4K film", 3840, 1600, PQ, 3, 532},
		{"a phone video held upright: 1080p, not 4K, and not shrunk", 1080, 1920, SDR, 1, 1920},
		{"a 4K phone video held upright", 2160, 3840, SDR, 3, 1280},
	} {
		m := media("avi", "mpeg4", 8, "mp3")
		m.Video[0].Width, m.Video[0].Height, m.Video[0].Transfer = c.width, c.height, c.transfer
		p := DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, able)
		if p.Method != Convert || p.Video.Rung != c.rung || p.Video.Height != c.lines {
			t.Errorf("%s: %s, rung %d, %d lines; want rung %d, %d lines", c.name, p.Method, p.Video.Rung, p.Video.Height, c.rung, c.lines)
		}
	}
	// An upright 1080p video is not "larger than 1080p": a device that
	// plays its format up to 1080p is sent it as it is.
	upTo1080p := caps(true, "mp4", "h264", "aac")
	upright := film("mp4", "h264", 8, "aac", pictureSize{"upright 1080p", 1080, 1920}, colours[0], "")
	if p := DecideFor(upright, upTo1080p, Want{Audio: -1, Subtitle: -1}, able); p.Method != Direct {
		t.Errorf("an upright 1080p video on a device that plays nothing larger than 1080p: %+v", p)
	}
	upright.Video[0].Width, upright.Video[0].Height = 2160, 3840
	if p := DecideFor(upright, upTo1080p, Want{Audio: -1, Subtitle: -1}, able); p.Method != Convert || !has(p.Lost, "4K picture shown as 720p.") {
		t.Errorf("an upright 4K video on that device: %+v", p)
	}
}

// The ladder is part of what the converter is told, so its figures are
// pinned here: a change must be a decision, not a slip.
func TestLadder(t *testing.T) {
	want := [NumRungs]Rung{
		{1080, "veryfast", 10_000_000, false},
		{1080, "superfast", 12_000_000, false},
		{720, "veryfast", 5_000_000, false},
		{720, "superfast", 5_000_000, true},
		{480, "ultrafast", 2_500_000, false},
	}
	if got := Ladder(); got != want {
		t.Errorf("the ladder is %+v, want %+v", got, want)
	}
	// What a caller does with its copy must not reach the planner.
	l := Ladder()
	l[0].Height = 1
	if Ladder()[0].Height != 1080 {
		t.Error("the ladder can be changed from outside")
	}
}

func TestPictureSizes(t *testing.T) {
	for _, c := range []struct {
		width, height, lines int
		wantW, wantH         int
	}{
		{1920, 1080, 1080, 1920, 1080},
		{1280, 720, 1080, 1280, 720}, // never enlarged
		{3840, 2160, 1080, 1920, 1080},
		{3840, 2160, 720, 1280, 720},
		{3840, 2160, 480, 852, 480},
		{3840, 1600, 1080, 1920, 800}, // wider than 16:9: the width decides
		{1920, 800, 1080, 1920, 800},
		{1920, 800, 720, 1280, 532},
		{1920, 700, 720, 1280, 466}, // few enough lines, still too wide
		{1440, 1080, 720, 960, 720}, // narrower: the height decides
		{1920, 1088, 1080, 1904, 1080},
		{854, 480, 480, 854, 480},
		{0, 2160, 1080, 0, 1080}, // width not read
		{0, 480, 1080, 0, 480},
		{0, 0, 720, 0, 720}, // nothing read: taken to fill the frame
		// A phone held upright: as much picture as the same lying down.
		{1080, 1920, 1080, 1080, 1920},
		{1080, 1920, 720, 720, 1280},
		{2160, 3840, 1080, 1080, 1920},
		{886, 1920, 480, 394, 854},
	} {
		if w, h := fit(c.width, c.height, c.lines); w != c.wantW || h != c.wantH {
			t.Errorf("%dx%d into %d lines: %dx%d, want %dx%d", c.width, c.height, c.lines, w, h, c.wantW, c.wantH)
		}
	}
	for _, c := range []struct {
		width, height int
		want          Size
		word          string
	}{
		{854, 480, Size480, "480p"}, {640, 360, Size480, "480p"}, {720, 576, Size720, "720p"}, {1280, 720, Size720, "720p"},
		{1280, 536, Size720, "720p"}, {1280, 400, Size720, "720p"}, {1920, 800, Size1080, "1080p"}, {1920, 700, Size1080, "1080p"},
		{1920, 1080, Size1080, "1080p"}, {1920, 1088, Size1080, "1080p"},
		{1440, 1080, Size1080, "1080p"}, {2560, 1440, Size4K, "1440p"}, {3840, 1600, Size4K, "4K"}, {3840, 2160, Size4K, "4K"},
		{0, 2160, Size4K, "4K"}, {0, 0, "", ""},
		{1080, 1920, Size1080, "1080p"}, {720, 1280, Size720, "720p"}, {480, 854, Size480, "480p"}, {2160, 3840, Size4K, "4K"}, {1440, 2560, Size4K, "1440p"},
	} {
		if got := sizeOf(c.width, c.height); got != c.want {
			t.Errorf("%dx%d is %q, want %q", c.width, c.height, got, c.want)
		}
		if got := sizeWord(c.width, c.height); got != c.word {
			t.Errorf("%dx%d is called %q, want %q", c.width, c.height, got, c.word)
		}
	}
}

// The iPhone rule. On an iPhone only the video goes fullscreen, not the
// page, so nothing the page draws over the film is seen there, in
// picture-in-picture or over AirPlay. Subtitles stored as pictures are
// therefore put into the picture from the first chunk, and styled ones
// too, unless the viewer prefers their words as plain text. It is decided
// before the film starts and does not depend on whether the page could
// draw.
func TestIPhoneRule(t *testing.T) {
	// The owner's phone as the page reports it: plays HEVC in 4K, cannot
	// open MKV, the page cannot go fullscreen.
	phone := func(draw Drawing) *Caps {
		c := *newDevices["safari"]
		c.PageFullscreen, c.Draw = false, draw
		return &c
	}
	pc := surfaces[0].on(newDevices["chrome+hevc"])
	anime := film("matroska", "hevc", 10, "flac", sizes[1], colours[0], StyledSubs)
	disc := film("matroska", "h264", 8, "ac3", sizes[1], colours[0], PictureSubs)
	plain := film("matroska", "h264", 8, "aac", sizes[1], colours[0], TextSubs)
	both, neither := Drawing{Styled: true, Picture: true}, Drawing{}

	for _, c := range []struct {
		name      string
		m         Media
		dev       *Caps
		styled    Styling
		method    Method
		subtitles SubtitleMode
		reason    string
	}{
		{"styled, as designed, the page could draw", anime, phone(both), Designed, Convert, SubsBurn, "only while the film plays inside the page"},
		{"styled, as designed, the page cannot draw", anime, phone(neither), Designed, Convert, SubsBurn, "cannot draw"},
		{"styled, nothing said: as designed is the default", anime, phone(both), "", Convert, SubsBurn, "have their own design"},
		{"picture, the page could draw", disc, phone(both), Designed, Convert, SubsBurn, "only while the film plays inside the page"},
		{"picture, the page cannot draw", disc, phone(neither), Designed, Convert, SubsBurn, "stored as pictures"},
		{"picture, whatever is said about styled ones", disc, phone(both), Plain, Convert, SubsBurn, "stored as pictures"},
		// Plain words cost no conversion: the MKV is only repackaged (the
		// FLAC beside it is converted, as in M3).
		{"styled, as plain text", anime, phone(both), Plain, Audio, SubsTrack, "MKV"},
		{"styled, as plain text, the page cannot draw", anime, phone(neither), Plain, Audio, SubsTrack, "MKV"},
		{"text", plain, phone(both), Designed, Remux, SubsTrack, "MKV"},
		// The same films on a PC, where the page goes fullscreen: drawn,
		// and the picture left alone.
		{"styled on a PC", anime, pc, Designed, Direct, SubsDraw, ""},
		{"picture on a PC", disc, pc, Designed, Audio, SubsDraw, "Dolby Digital"},
	} {
		p := DecideFor(c.m, c.dev, Want{Audio: -1, Subtitle: 0, Styled: c.styled}, middling)
		if p.Method != c.method || p.Subtitles != c.subtitles || p.Playable != Yes {
			t.Errorf("%s: %s with subtitles as %q %v; want %s with %q", c.name, p.Method, p.Subtitles, p.Reasons, c.method, c.subtitles)
			continue
		}
		if c.reason != "" && !has(p.Reasons, c.reason) {
			t.Errorf("%s: reasons %q do not mention %q", c.name, p.Reasons, c.reason)
		}
		if (p.Subtitles == SubsBurn) != strings.Contains(p.Explain(), "subtitles put into it") {
			t.Errorf("%s: explained as %q", c.name, p.Explain())
		}
	}

	// A burned-in film is a converted one, with all that follows: the
	// sound the phone plays is kept, so surround survives.
	p := DecideFor(disc, phone(both), Want{Audio: -1, Subtitle: 0}, middling)
	if !p.AudioCopy || p.Video.Rung != 1 || p.Video.Height != 1080 || len(p.Lost) != 0 {
		t.Errorf("a disc copy with picture subtitles on the phone: %+v", p)
	}
}

// Where the design of styled subtitles cannot be put into the picture
// (this PC cannot convert, or not fast enough), the film still plays, with
// their words as plain text, and the viewer is told. Subtitles stored as
// pictures have no words to fall back on: there the answer is no, and the
// reasons say that it is the subtitles.
func TestSubtitlesThatCannotBePutIntoThePicture(t *testing.T) {
	phone := surfaces[3].on(newDevices["safari"])
	slow := measured(able, every(0.9), every(0.9), every(0.9), every(0.9))
	for name, l := range map[string]Limits{"a PC that cannot convert": {}, "a PC too slow to convert": slow} {
		anime := film("mp4", "h264", 8, "aac", sizes[1], colours[0], StyledSubs)
		p := DecideFor(anime, phone, Want{Audio: -1, Subtitle: 0, Styled: Designed}, l)
		if p.Method != Direct || p.Subtitles != SubsTrack || len(p.Reasons) != 0 || !has(p.Lost, "plain text, without their design") {
			t.Errorf("styled subtitles on %s: %+v; want the film as it is, with plain words", name, p)
		}
		disc := film("mp4", "h264", 8, "aac", sizes[1], colours[0], PictureSubs)
		p = DecideFor(disc, phone, Want{Audio: -1, Subtitle: 0}, l)
		if p.Playable != No || !has(p.Reasons, "The subtitles you chose are stored as pictures") {
			t.Errorf("picture subtitles on %s: %+v; want a refusal that names the subtitles", name, p)
		}
		// Without those subtitles the film plays, which is what the server
		// can offer instead.
		if p = DecideFor(disc, phone, Want{Audio: -1, Subtitle: -1}, l); p.Method != Direct {
			t.Errorf("the same film without subtitles on %s: %+v", name, p)
		}
	}
	// A subtitle that is not in the file is no subtitle.
	m := film("mp4", "h264", 8, "aac", sizes[1], colours[0], PictureSubs)
	for _, n := range []int{-1, 1, 99} {
		if p := DecideFor(m, phone, Want{Audio: -1, Subtitle: n}, able); p.Method != Direct || p.Subtitles != SubsNone {
			t.Errorf("subtitle %d of a file with one: %+v", n, p)
		}
	}
}

// What the viewer loses is said in plain words, and nothing is said to be
// lost that is not.
func TestWhatIsLost(t *testing.T) {
	uhd := film("matroska", "hevc", 10, "truehd", sizes[2], colours[1], "")
	uhd.Audio[0].Channels = 8
	for _, c := range []struct {
		name string
		m    Media
		dev  string
		w    Want
		l    Limits
		want []string
	}{
		{"a 4K HDR film with TrueHD in Chrome without HEVC", uhd, "chrome", Want{Audio: -1, Subtitle: -1}, able,
			[]string{"4K picture shown as 720p.", "HDR shown as normal colour.", "Surround sound is played as stereo."}},
		{"the same on a fast PC", uhd, "chrome", Want{Audio: -1, Subtitle: -1}, fast,
			[]string{"4K picture shown as 1080p.", "HDR shown as normal colour.", "Surround sound is played as stereo."}},
		{"the same on an iPhone: only the sound is converted", uhd, "safari", Want{Audio: -1, Subtitle: -1}, able,
			[]string{"Surround sound is played as stereo."}},
		{"Dolby Digital beside a converted picture is kept where the device plays it", film("avi", "mpeg4", 8, "ac3", sizes[0], colours[0], ""), "safari",
			Want{Audio: -1, Subtitle: -1}, able, []string{}},
		{"a small old film loses nothing but its format", film("avi", "mpeg4", 8, "mp3", sizes[0], colours[0], ""), "chrome",
			Want{Audio: -1, Subtitle: -1}, able, []string{}},
		{"Dolby Vision 5 in Chrome with HEVC", film("mp4", "hevc", 10, "aac", sizes[2], colours[3], ""), "chrome+hevc", Want{Audio: -1, Subtitle: -1}, able,
			[]string{"4K picture shown as 720p.", "Dolby Vision shown as normal colour."}},
		{"1080p made lighter by two steps", film("mp4", "h264", 8, "aac", sizes[1], colours[0], ""), "chrome", Want{Audio: -1, Subtitle: -1, Lower: 3}, able,
			[]string{"1080p picture shown as 720p."}},
	} {
		p := DecideFor(c.m, newDevices[c.dev], c.w, c.l)
		if !sameStrings(p.Lost, c.want) {
			t.Errorf("%s (%s): lost %q, want %q", c.name, p.Method, p.Lost, c.want)
		}
	}

	// Rung 4 leaves out every second picture of a film with more than 30 a
	// second, and says so; a film at 24 loses none.
	sports := film("mpegts", "mpeg2video", 8, "mp2", sizes[1], colours[0], "")
	sports.Video[0].FrameRate = 50
	for rate, want := range map[float64]bool{50: true, 59.94: true, 30: false, 24: false, 0: false} {
		sports.Video[0].FrameRate = rate
		p := DecideFor(sports, newDevices["chrome"], Want{Audio: -1, Subtitle: -1, Lower: 4}, able)
		if p.Video.Rung != 4 || has(p.Lost, "Every second picture is left out") != want {
			t.Errorf("%v pictures a second at rung %d: lost %q", rate, p.Video.Rung, p.Lost)
		}
		if p = DecideFor(sports, newDevices["chrome"], Want{Audio: -1, Subtitle: -1, Lower: 3}, able); has(p.Lost, "Every second picture") {
			t.Errorf("%v pictures a second at rung %d: lost %q", rate, p.Video.Rung, p.Lost)
		}
	}
}

// Dolby Vision profile 5 looks green and purple on a device that plays it
// as ordinary HEVC, so it is never sent as it is to a device that does not
// say it shows it. Converting it takes a filter of its own; a PC without
// that filter says no, and says why.
func TestDolbyVisionProfile5(t *testing.T) {
	m := film("mp4", "hevc", 10, "eac3", sizes[2], colours[3], "")
	want := Want{Audio: -1, Subtitle: -1}
	withFilter, without := able, Limits{Convert: true, ToneMap: true}

	if p := DecideFor(m, newDevices["safari"], want, without); p.Method != Direct {
		t.Errorf("on a device that shows it: %+v; want the original", p)
	}
	p := DecideFor(m, newDevices["chrome+hevc"], want, withFilter)
	if p.Method != Convert || !p.Video.HDRToSDR || !has(p.Reasons, "Dolby Vision (profile 5), which this device cannot show in the right colours") {
		t.Errorf("on a device that plays HEVC but not Dolby Vision: %+v", p)
	}
	for name, l := range map[string]Limits{"no Dolby Vision filter": without, "no tone mapping at all": {Convert: true}} {
		p = DecideFor(m, newDevices["chrome+hevc"], want, l)
		if p.Playable != No || !has(p.Reasons, "Dolby Vision (profile 5)") || !has(p.Reasons, "This PC's converter cannot turn Dolby Vision of this kind into normal colour") {
			t.Errorf("%s: %+v; want a plain no", name, p)
		}
	}
	p = DecideFor(m, newDevices["chrome+hevc"], want, Limits{})
	if p.Playable != No || !has(p.Reasons, "Dolby Vision (profile 5)") || !has(p.Reasons, "This PC cannot convert pictures.") {
		t.Errorf("a PC that cannot convert: %+v; want a plain no", p)
	}
	// A device without HEVC needs the filter just the same.
	if p = DecideFor(m, newDevices["chrome"], want, without); p.Playable != No || !has(p.Reasons, "Dolby Vision") {
		t.Errorf("a device without HEVC, a PC without the filter: %+v", p)
	}
	// Many such files do not say that they are HDR. They are, and are
	// treated so: the filter is needed, the film counts as heavy work and
	// the viewer is told the colours change.
	untagged := film("mp4", "hevc", 10, "aac", sizes[2], colour{"Dolby Vision 5 that does not call itself HDR", "", 5}, "")
	p = DecideFor(untagged, newDevices["chrome+hevc"], want, withFilter)
	if p.Method != Convert || p.Video.Class != Class4KHDR || p.Video.Rung != 3 || !p.Video.HDRToSDR || !has(p.Lost, "Dolby Vision shown as normal colour.") {
		t.Errorf("Dolby Vision 5 that does not call itself HDR: %+v", p)
	}
	if p = DecideFor(untagged, newDevices["chrome+hevc"], want, without); p.Playable != No {
		t.Errorf("the same on a PC without the filter: %+v", p)
	}
	// The other profiles carry a picture any HDR device plays: here only
	// the sound, which Chrome cannot play, is converted.
	m.Video[0].DoVi = 8
	if p = DecideFor(m, newDevices["chrome+hevc"], want, without); p.Method != Audio {
		t.Errorf("profile 8 on a device that plays HEVC: %+v; want the picture left alone", p)
	}
	// And ordinary HDR is converted by the ordinary filter, or not at all.
	if p = DecideFor(m, newDevices["chrome"], want, Limits{Convert: true}); p.Playable != No || !has(p.Reasons, "cannot turn HDR into normal colour") {
		t.Errorf("HDR on a PC without tone mapping: %+v", p)
	}
}

// A lesson is advice. A film that would play as it is gets converted when
// a lesson says so and this PC can; where it cannot, the film plays as it
// would have without the lesson.
func TestLessonsAreAdvice(t *testing.T) {
	want := Want{Audio: -1, Subtitle: -1}
	dev := newDevices["chrome"]
	m := film("mp4", "h264", 8, "aac", sizes[1], colours[0], "")
	slowLink := able
	slowLink.MaxRate = 4_000_000

	p := DecideFor(m, dev, want, slowLink)
	if p.Method != Convert || p.Video.Rung != 5 || !has(p.Reasons, "reaching this device too slowly") {
		t.Errorf("a slow connection: %+v; want the lightest picture", p)
	}
	// A file known to be lighter than the cap is left alone.
	m.BitRate = 2_500_000
	if p = DecideFor(m, dev, want, slowLink); p.Method != Direct {
		t.Errorf("a slow connection and a light file: %+v; want the original", p)
	}
	m.BitRate = 9_000_000
	if p = DecideFor(m, dev, want, slowLink); p.Method != Convert {
		t.Errorf("a slow connection and a heavy file: %+v", p)
	}
	// A PC that cannot convert, or not fast enough, sends the original.
	tooSlow := measured(slowLink, every(1), every(1), every(1), every(1))
	for name, l := range map[string]Limits{"cannot convert": {MaxRate: 4_000_000}, "too slow": tooSlow} {
		if p = DecideFor(m, dev, want, l); p.Method != Direct || len(p.Reasons) != 0 {
			t.Errorf("a slow connection and a PC that %s: %+v; want the original", name, p)
		}
	}
	// A repackaged film is advised the same way, and the sentence about
	// leaving the picture untouched goes.
	hev1 := film("mp4", "hevc", 8, "aac", sizes[1], colours[0], "")
	hev1.Video[0].Tag = "hev1"
	if p = DecideFor(hev1, newDevices["safari"], want, able); p.Method != Remux || !has(p.Reasons, "untouched") {
		t.Fatalf("hev1 on an iPhone: %+v", p)
	}
	if p = DecideFor(hev1, newDevices["safari"], want, slowLink); p.Method != Convert || has(p.Reasons, "untouched") {
		t.Errorf("hev1 on an iPhone with a slow connection: %+v", p)
	}

	// "Colours look wrong on this device": HDR is converted for it, normal
	// colour is not.
	noHDR := able
	noHDR.NoHDR = true
	hdr := film("mp4", "hevc", 10, "aac", sizes[1], colours[1], "")
	if p = DecideFor(hdr, newDevices["safari"], want, noHDR); p.Method != Convert || !p.Video.HDRToSDR || !has(p.Reasons, "HDR colours were reported to look wrong") {
		t.Errorf("HDR on a device that shows it wrongly: %+v", p)
	}
	if p = DecideFor(hdr, newDevices["safari"], want, Limits{Convert: true, NoHDR: true}); p.Method != Direct {
		t.Errorf("the same on a PC without tone mapping: %+v; want the original", p)
	}
	sdr := film("mp4", "hevc", 10, "aac", sizes[1], colours[0], "")
	if p = DecideFor(sdr, newDevices["safari"], want, noHDR); p.Method != Direct {
		t.Errorf("normal colour on that device: %+v; want the original", p)
	}

	// A cap on the size of a converted picture does not convert a film
	// that plays as it is: it is about the converted picture only.
	small := able
	small.MaxHeight = 480
	if p = DecideFor(film("mp4", "h264", 8, "aac", sizes[1], colours[0], ""), dev, want, small); p.Method != Direct {
		t.Errorf("a film that plays, with a cap on converted pictures: %+v", p)
	}
	if p = DecideFor(film("avi", "mpeg4", 8, "mp3", sizes[1], colours[0], ""), dev, want, small); p.Video.Rung != 5 || p.Video.Height != 480 {
		t.Errorf("a film that is converted, with that cap: %+v", p)
	}
}

// The sound beside a converted picture is the track the viewer chose,
// passed on untouched where the device plays it and the chunks can carry
// it, and converted to stereo otherwise.
func TestSoundBesideAConvertedPicture(t *testing.T) {
	m := Media{Probed: true, Container: "avi", Video: []Track{{Codec: "mpeg4", BitDepth: 8, Width: 720, Height: 576}},
		Audio: []Track{{Codec: "mp3", Default: true, Channels: 2}, {Codec: "ac3", Channels: 6}, {Codec: "dts", Channels: 6}}}
	for _, c := range []struct {
		name         string
		dev          string
		asked, track int
		passedOn     bool
		lost         []string
	}{
		{"the main track, MP3: the chunks cannot carry it", "safari", -1, 0, false, []string{}},
		{"Dolby Digital on a device that plays it: kept, surround and all", "safari", 1, 1, true, []string{}},
		{"DTS", "safari", 2, 2, false, []string{"Surround sound is played as stereo."}},
		{"Dolby Digital on a device that does not play it", "chrome", 1, 1, false, []string{"Surround sound is played as stereo."}},
		{"a track that is not there: the main one", "chrome", 9, 0, false, []string{}},
	} {
		p := DecideFor(m, newDevices[c.dev], Want{Audio: c.asked, Subtitle: -1}, able)
		if p.Method != Convert || p.AudioTrack != c.track || p.AudioCopy != c.passedOn || !p.HasAudio || !sameStrings(p.Lost, c.lost) {
			t.Errorf("%s: %+v", c.name, p)
		}
	}

	// AAC is passed on out of an MKV or an MP4. Out of a TV recording it
	// would have to be rewritten on the way, so there it is converted.
	for cont, passedOn := range map[string]bool{"matroska": true, "mp4": true, "mov": true, "mpegts": false, "avi": false} {
		p := DecideFor(film(cont, "mpeg2video", 8, "aac", sizes[1], colours[0], ""), newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, able)
		if p.Method != Convert || p.AudioCopy != passedOn {
			t.Errorf("AAC beside a converted picture out of %s: %+v", cont, p)
		}
	}

	// A WebM cannot be repackaged, so M3 could not reach a sound track
	// chosen by hand in one. Converting the picture can, and does; the
	// file's own main track still plays as it is.
	webm := Media{Probed: true, Container: "webm", Video: []Track{{Codec: "vp9", BitDepth: 8, Width: 1920, Height: 1080}},
		Audio: []Track{{Codec: "opus", Default: true}, {Codec: "opus"}}}
	if p := Decide(webm, newDevices["chrome"], 1); p.Playable != No {
		t.Fatalf("without conversion: %+v", p)
	}
	p := DecideFor(webm, newDevices["chrome"], Want{Audio: 1, Subtitle: -1}, able)
	want := []string{"You chose a sound track other than the one a browser plays by itself.", "Its picture (VP9) cannot be repackaged without converting it."}
	if p.Method != Convert || p.AudioTrack != 1 || p.AudioCopy || !sameStrings(p.Reasons, want) {
		t.Errorf("the second sound track of a WebM: %+v", p)
	}
	if p = DecideFor(webm, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, able); p.Method != Direct {
		t.Errorf("the main sound track of a WebM: %+v", p)
	}
}

// What a PC that is too slow answers, word for word: the page shows it.
func TestTooSlowIsSaidPlainly(t *testing.T) {
	m := film("avi", "mpeg4", 8, "mp3", sizes[1], colours[0], "")
	slow := measured(able, [NumRungs]float64{0.4, 0.5, 0.8, 1.0, 1.49}, every(0), every(0), every(0))
	p := DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, slow)
	want := []string{"Its picture is MPEG-4 (DivX/Xvid), which this browser cannot play.", "It is packaged as AVI, which this browser cannot open.",
		"This PC cannot convert this film fast enough to watch: at best it manages 1.4 seconds of film a second, and it takes 1.5 to keep up."}
	if p.Playable != No || p.Method != None || !sameStrings(p.Reasons, want) {
		t.Errorf("got %s %q, want %q", p.Method, p.Reasons, want)
	}
	// The plan says so in a way the page can act on (it offers another
	// player), and in its one-sentence summary.
	if !p.TooSlow || p.Explain() != "This PC cannot convert this film fast enough to watch." {
		t.Errorf("too slow: tooSlow=%v, explained as %q", p.TooSlow, p.Explain())
	}
	// Other refusals are not about speed.
	for name, q := range map[string]Plan{
		"a PC that cannot convert":                             DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, Limits{}),
		"a device that takes no chunks":                        DecideFor(m, newDevices["old-tv"], Want{Audio: -1, Subtitle: -1}, slow),
		"a film that plays":                                    DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, able),
		"a lesson this PC is too slow for":                     DecideFor(film("mp4", "h264", 8, "aac", sizes[1], colours[0], ""), newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, func() Limits { l := slow; l.MaxRate = 4_000_000; return l }()),
		"a design this PC is too slow to put into the picture": DecideFor(film("mp4", "h264", 8, "aac", sizes[1], colours[0], StyledSubs), surfaces[3].on(newDevices["safari"]), Want{Audio: -1, Subtitle: 0}, slow),
	} {
		if q.TooSlow || strings.Contains(q.Explain(), "fast enough") {
			t.Errorf("%s: tooSlow=%v, explained as %q", name, q.TooSlow, q.Explain())
		}
	}
	slow.Busy = 2
	p = DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, slow)
	if last := p.Reasons[len(p.Reasons)-1]; last != "This PC cannot convert this film fast enough to watch: at best it manages 0.4 seconds of film a second, and it takes 1.5 to keep up while it is converting for 2 other screens as well." {
		t.Errorf("busy: %q", last)
	}
	// One hundredth faster and it plays, at the lightest rung.
	slow.Busy = 0
	slow.Speed = Speeds{ClassHD: {0.4, 0.5, 0.8, 1.0, 1.5}}
	if p = DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, slow); p.Video.Rung != 5 {
		t.Errorf("exactly on the bar: %+v", p)
	}
	// A lighter picture than the lightest there is: no, with the lightest
	// rung's figure where that was measured too slow.
	p = DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1, Lower: NumRungs + 1}, able)
	if last := p.Reasons[len(p.Reasons)-1]; p.Playable != No || last != "This PC cannot convert this film fast enough to watch." {
		t.Errorf("lighter than the lightest, nothing measured: %s %q", p.Method, p.Reasons)
	}
	slow.Speed = Speeds{ClassHD: {0.4, 0.5, 0.8, 1.0, 0.7}}
	p = DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1, Lower: NumRungs + 1}, slow)
	if last := p.Reasons[len(p.Reasons)-1]; !strings.Contains(last, "at best it manages 0.7 seconds") {
		t.Errorf("lighter than the lightest, measured: %q", last)
	}
}

// A graphics-card encoder that passed the owner's test is named in the
// plan, with its own speeds; nothing else about the decision changes.
func TestEncoderIsPassedOn(t *testing.T) {
	m := film("avi", "mpeg4", 8, "mp3", sizes[1], colours[0], "")
	l := able
	if p := DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, l); p.Video.Encoder != "x264" {
		t.Errorf("no encoder named: %+v", p.Video)
	}
	l.Encoder = "nvenc"
	if p := DecideFor(m, newDevices["chrome"], Want{Audio: -1, Subtitle: -1}, l); p.Video.Encoder != "nvenc" || p.Video.Rung != 1 {
		t.Errorf("a graphics card: %+v", p.Video)
	}
}

// What a converted film's plan says, in the words the page shows.
func TestExplainConversion(t *testing.T) {
	for _, c := range []struct {
		m    Media
		dev  string
		w    Want
		want string
	}{
		{film("avi", "mpeg4", 8, "mp3", sizes[0], colours[0], ""), "chrome", Want{Audio: -1, Subtitle: -1},
			"Converting the picture as it plays, which is heavy work for the PC. The sound becomes stereo AAC."},
		{film("matroska", "vp9", 8, "aac", sizes[0], colours[0], ""), "safari", Want{Audio: -1, Subtitle: -1},
			"Converting the picture as it plays, which is heavy work for the PC. The sound is untouched."},
		{film("avi", "mpeg4", 8, "", sizes[0], colours[0], ""), "chrome", Want{Audio: -1, Subtitle: -1},
			"Converting the picture as it plays, which is heavy work for the PC."},
		{film("mp4", "h264", 8, "aac", sizes[0], colours[0], PictureSubs), "old-tv-with-chunks", Want{Audio: -1, Subtitle: 0},
			"Converting the picture as it plays, with the subtitles put into it, which is heavy work for the PC. The sound is untouched."},
	} {
		dev := newDevices[c.dev]
		if dev == nil {
			dev = caps(true, "mp4", "h264", "aac")
		}
		p := DecideFor(c.m, dev, c.w, able)
		if p.Method != Convert || p.Explain() != c.want {
			t.Errorf("%s: %q, want %q", p.Method, p.Explain(), c.want)
		}
	}
}

// A file not yet inspected and a device that has said nothing are still
// sent the original, as in M3. Text subtitles can be tried as a track;
// whether anything else can be shown cannot be told yet.
func TestSubtitlesWhenLittleIsKnown(t *testing.T) {
	for name, c := range map[string]struct {
		m   Media
		dev *Caps
	}{
		"a file not inspected":           {Media{Subtitles: []Subtitle{{Kind: TextSubs}, {Kind: StyledSubs}, {Kind: PictureSubs}}}, newDevices["chrome"]},
		"a device that has said nothing": {Media{Probed: true, Container: "mp4", Video: []Track{{Codec: "h264"}}, Subtitles: []Subtitle{{}, {Kind: StyledSubs}, {Kind: PictureSubs}}}, nil},
		"a device with an empty report":  {Media{Probed: true, Container: "mp4", Video: []Track{{Codec: "h264"}}, Subtitles: []Subtitle{{}, {Kind: StyledSubs}, {Kind: PictureSubs}}}, &Caps{}},
		"neither is known":               {Media{Subtitles: []Subtitle{{}, {Kind: StyledSubs}, {Kind: PictureSubs}}}, nil},
	} {
		for i, want := range []struct {
			mode SubtitleMode
			lost string
		}{{SubsTrack, ""}, {SubsTrack, "plain text"}, {SubsNone, "stored as pictures"}} {
			// Whatever is wished for and whatever the PC can do.
			p := DecideFor(c.m, c.dev, Want{Audio: -1, Subtitle: i, Lower: 3}, middling)
			lostRight := len(p.Lost) == 0
			if want.lost != "" {
				lostRight = len(p.Lost) == 1 && has(p.Lost, want.lost)
			}
			if p.Playable != Unknown || p.Method != Direct || len(p.Reasons) != 0 || p.Subtitles != want.mode || !lostRight {
				t.Errorf("%s, subtitle %d: %+v", name, i, p)
			}
		}
		if p := DecideFor(c.m, c.dev, Want{Audio: -1, Subtitle: 1, Styled: Plain}, able); p.Subtitles != SubsTrack || len(p.Lost) != 0 {
			t.Errorf("%s, styled subtitles as plain text: %+v", name, p)
		}
	}
}

// The page reads a plan by these names. (A test beside the server holds
// the page's own list to them; this one keeps a rename from slipping
// through here.)
func TestPlanJSON(t *testing.T) {
	p := DecideFor(film("avi", "mpeg4", 8, "dts", sizes[2], colours[1], PictureSubs), surfaces[3].on(newDevices["chrome"]), Want{Audio: -1, Subtitle: 0}, able)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) string {
		var names []string
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		return strings.Join(names, " ")
	}
	if want := "audioCopy audioTrack deviceUnknown hasAudio lost method picture playable reasons subtitleKind subtitles tooSlow video"; keys(got) != want {
		t.Errorf("a plan has the fields %q, want %q", keys(got), want)
	}
	if want := "class encoder hdrToSdr height rung"; keys(got["video"].(map[string]any)) != want {
		t.Errorf("its video has the fields %q, want %q", keys(got["video"].(map[string]any)), want)
	}
	if want := "bitDepth codec size"; keys(got["picture"].(map[string]any)) != want {
		t.Errorf("its picture has the fields %q, want %q", keys(got["picture"].(map[string]any)), want)
	}
	if got["method"] != "convert" || got["subtitles"] != "burn" || got["subtitleKind"] != "picture" || len(got["lost"].([]any)) != 3 {
		t.Errorf("a converted plan reads %s", raw)
	}
	// Lists are never null, whatever the answer.
	for name, q := range map[string]Plan{
		"not inspected":  DecideFor(Media{}, nil, Want{}, Limits{}),
		"no picture":     Decide(media("mp4", "", 0, "aac"), devices["chrome"], -1),
		"device unknown": Decide(media("mp4", "h264", 8, "aac"), nil, -1),
		"direct":         Decide(media("mp4", "h264", 8, "aac"), devices["chrome"], -1),
		"refused":        Decide(media("avi", "mpeg4", 8, "mp3"), devices["chrome"], -1),
	} {
		raw, _ := json.Marshal(q)
		if strings.Contains(string(raw), "null") {
			t.Errorf("%s: %s", name, raw)
		}
	}
	// A device report from before this version says nothing about 4K,
	// fullscreen or drawing; it must still be read.
	var old Caps
	if err := json.Unmarshal([]byte(`{"containers":{"mp4":true},"video":{"h264":true},"audio":{"aac":true},"hls":true}`), &old); err != nil || old.Tall != nil || old.PageFullscreen || old.Draw != (Drawing{}) {
		t.Errorf("an old device report: %+v, %v", old, err)
	}
	var now Caps
	report := `{"containers":{"mp4":true},"video":{"hevc10":true,"dovi5":true},"audio":{},"hls":true,"tall":{"hevc10":true},"pageFullscreen":true,"draw":{"styled":true,"picture":false}}`
	if err := json.Unmarshal([]byte(report), &now); err != nil || !now.Tall["hevc10"] || !now.PageFullscreen || now.Draw != (Drawing{Styled: true}) {
		t.Errorf("a device report: %+v, %v", now, err)
	}
}
