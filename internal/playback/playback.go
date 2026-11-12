// Package playback decides how a file is played on a device. It is made of
// pure functions: what ffprobe said about the file, what the device says it
// can play and what the viewer wants go in; a decision with reasons in
// plain words comes out.
//
// There are five possible answers, tried in this order because each costs
// more than the one before:
//
//	direct   send the original file as it is
//	remux    repackage it without touching picture or sound (no quality
//	         loss, almost no work for the PC)
//	audio    repackage it and convert only the sound
//	convert  convert the picture too, to H.264 in normal colour, at a size
//	         and effort this PC can keep up with (see convert.go)
//	none     it cannot be played on this device from this PC, and the
//	         reasons say why
//
// When something goes wrong while a film plays, Replan says what to change,
// what to remember and what to tell the viewer (see stepdown.go).
package playback

import "strings"

// Caps is what a device reports it can play. Keys are ffprobe's names:
// containers "mp4", "matroska", "webm", "mov"; codecs "h264", "hevc", "ac3".
// "hevc10" stands for 10-bit HEVC, which many devices that play HEVC cannot,
// and "h264-10" for 10-bit H.264. "hevc-hev1" says the device plays HEVC
// under the label "hev1" as well as "hvc1" (see DecideFor). "dovi5" says it
// shows Dolby Vision profile 5, which looks green and purple on a device
// that plays it as ordinary HEVC.
type Caps struct {
	Containers map[string]bool `json:"containers"`
	Video      map[string]bool `json:"video"`
	Audio      map[string]bool `json:"audio"`
	// HLS says the device can play repackaged video (the chunked format
	// phones and browsers accept), natively or through the page's player.
	HLS bool `json:"hls"`
	// Tall says, under the same keys as Video, that the device plays that
	// format smoothly when the picture is larger than 1080p (4K). A format
	// that is not listed is played up to 1080p only.
	Tall map[string]bool `json:"tall"`
	// PageFullscreen says the page itself can fill the screen, so whatever
	// the page draws over the film is still there in fullscreen. It is
	// false on an iPhone, where only the video can: there the film leaves
	// the page for the phone's own player and takes nothing drawn with it.
	PageFullscreen bool `json:"pageFullscreen"`
	// Draw says which subtitles the page can draw over the film itself.
	Draw Drawing `json:"draw"`
}

// Drawing names the two kinds of subtitle that are not plain text: styled
// ones (with their own fonts, colours and positions) and ones stored as
// pictures.
type Drawing struct {
	Styled  bool `json:"styled"`
	Picture bool `json:"picture"`
}

// Track is one video, audio or subtitle track of a file.
type Track struct {
	Codec string
	// Tag is the four-letter label the file gives the codec, as ffprobe
	// reports it ("hvc1", "hev1"); "" where there is none or it was not read.
	Tag             string
	BitDepth        int
	Channels        int
	Default         bool
	Forced          bool
	Commentary      bool
	HearingImpaired bool
	Language        string
	Title           string
	// Width and Height are the picture's size in pixels, and FrameRate how
	// many pictures it shows a second; 0 where not read.
	Width, Height int
	FrameRate     float64
	// Transfer says how the picture stores brightness: SDR (normal
	// colour), PQ or HLG (the two kinds of HDR). "" counts as SDR.
	Transfer string
	// DoVi is the picture's Dolby Vision profile number, 0 for none. Only
	// profile 5 matters here: the others carry a picture that plays as
	// ordinary HDR or normal colour on a device without Dolby Vision.
	DoVi int
}

// How a picture stores brightness (Track.Transfer).
const (
	SDR = "sdr"
	PQ  = "pq"
	HLG = "hlg"
)

// Media is what is known about a file.
type Media struct {
	Probed    bool // false: the file has not been inspected
	Container string
	Video     []Track
	Audio     []Track
	// Subtitles are the subtitles a viewer could switch on, inside the
	// file and beside it. Want.Subtitle counts into this list.
	Subtitles []Subtitle
	// BitRate is how many bits the whole file spends on a second of film;
	// 0 where not known. Only a lesson about a slow connection asks.
	BitRate int
}

// Answer says whether a device can play a file today.
type Answer string

const (
	Yes     Answer = "yes"
	No      Answer = "no"
	Unknown Answer = "unknown"
)

// Method is how a file gets to the device.
type Method string

const (
	Direct  Method = "direct"
	Remux   Method = "remux"
	Audio   Method = "audio"
	Convert Method = "convert"
	None    Method = "none"
)

// Plan is a decision and why.
type Plan struct {
	Playable Answer `json:"playable"`
	Method   Method `json:"method"`
	// Reasons say why the original file is not simply sent as it is.
	// Empty for direct.
	Reasons []string `json:"reasons"`
	// HasAudio is false for a file with no sound track.
	HasAudio bool `json:"hasAudio"`
	// AudioTrack is which sound track plays, counted from 0; -1 for none.
	AudioTrack int `json:"audioTrack"`
	// AudioCopy says the sound is passed through untouched.
	AudioCopy bool `json:"audioCopy"`
	// DeviceUnknown says the answer is "unknown" because the device has not
	// said what it can play, not because the file has not been inspected.
	DeviceUnknown bool `json:"deviceUnknown"`
	// TooSlow says the answer is no for one reason only: this PC cannot
	// convert the film as fast as it plays. The film itself is fine, so
	// the page can offer to open it in another player.
	TooSlow bool `json:"tooSlow"`
	// PCCannot says the answer is no because of this PC, not the device:
	// the file would have to be repackaged or converted for the device,
	// and this PC cannot do that. It cannot convert pictures, or its
	// converter lacks what this film needs, or it is too slow (TooSlow is
	// then set as well).
	PCCannot bool `json:"pcCannot"`
	// Video says what the picture is converted to. It is empty unless the
	// method is convert.
	Video Video `json:"video"`
	// Subtitles says how the chosen subtitles reach the viewer, and
	// SubtitleKind what kind they are ("" when none are shown).
	Subtitles    SubtitleMode `json:"subtitles"`
	SubtitleKind SubtitleKind `json:"subtitleKind"`
	// Lost says in plain words what the viewer does not get compared with
	// the original: a smaller picture, normal colour in place of HDR,
	// stereo in place of surround. Empty when nothing is lost.
	Lost []string `json:"lost"`
	// Picture is the kind of picture the device is asked to show: the
	// film's own for direct, remux and audio, the converted one for
	// convert. StepDown needs it when the device turns out not to manage.
	Picture PictureKind `json:"picture"`
}

// packaged lists what the repackaged format can carry. Anything else in a
// file has to be converted before it can go into it.
var (
	packagedVideo = map[string]bool{"h264": true, "hevc": true, "av1": true}
	packagedAudio = map[string]bool{"aac": true, "ac3": true, "eac3": true}
)

// Decide chooses how to play a file where pictures cannot be converted and
// no subtitles are asked for. audioTrack is the sound track the viewer
// wants, counted from 0, or -1 for the file's own main track (see
// BrowserAudio). It is DecideFor with nothing more wanted and no limits
// given, and it answers exactly as it did before pictures could be
// converted (TestWithoutConversionNothingChanged holds it to that).
func Decide(m Media, c *Caps, audioTrack int) Plan {
	return DecideFor(m, c, Want{Audio: audioTrack, Subtitle: -1}, Limits{})
}

// DecideFor chooses how to play a file: m is the file, c what the device
// says it plays, w what the viewer wants, and l what this PC can do and
// what earlier trouble has taught (see Want and Limits).
//
// The original is sent whenever it would play, and repackaged when that is
// all it takes. The picture is converted when nothing less will do: the
// device cannot show the film's own picture, or the subtitles have to be
// put into it. Where this PC can, it is also converted when a lesson or the
// viewer's wish for a lighter picture advises it (see advised); where it
// cannot, advice is dropped and the film plays as it would have.
func DecideFor(m Media, c *Caps, w Want, l Limits) Plan {
	sub := wanted(m, w)
	if !m.Probed {
		// Nothing is known: send the file and let the player find out.
		return blind(Plan{Playable: Unknown, Method: Direct, HasAudio: true, AudioTrack: -1}, sub, w)
	}
	hasAudio := len(m.Audio) > 0
	if len(m.Video) == 0 {
		return refuse(Plan{HasAudio: hasAudio, AudioTrack: -1}, []string{"This file has no picture track."})
	}
	if c == nil || c.Containers == nil {
		return blind(Plan{Playable: Unknown, Method: Direct, HasAudio: hasAudio, AudioTrack: -1, DeviceUnknown: true,
			Picture: pictureOf(m.Video[0])}, sub, w)
	}
	mode := showing(sub, c, w)
	p := decide(m, c, w, l, sub, mode)
	if p.Playable == No && mode == SubsBurn && kindOf(sub) == StyledSubs {
		// The design cannot be put into the picture here. If that is all
		// that stands in the way, the words alone are worth more than no
		// film. (Subtitles stored as pictures have no words to fall back
		// on: there the answer stays no, and says why.)
		if q := decide(m, c, w, l, sub, SubsTrack); q.Playable != No {
			q.Lost = append(q.Lost, lostDesign)
			return q
		}
	}
	return p
}

// decide is DecideFor once the file and the device are known and the way
// the subtitles are shown is settled.
func decide(m Media, c *Caps, w Want, l Limits, sub *Subtitle, mode SubtitleMode) Plan {
	v := m.Video[0]
	key := videoKey(v.Codec, v.BitDepth)
	natural := BrowserAudio(m.Audio)
	audioTrack := w.Audio
	if audioTrack < 0 || audioTrack >= len(m.Audio) {
		audioTrack = natural
	}
	var a *Track
	if audioTrack >= 0 {
		a = &m.Audio[audioTrack]
	}
	p := Plan{Playable: Yes, Method: Direct, HasAudio: len(m.Audio) > 0, AudioTrack: audioTrack, Reasons: []string{},
		Subtitles: mode, Lost: []string{}, Picture: pictureOf(v)}
	if sub != nil {
		p.SubtitleKind = kindOf(sub)
	}

	// First, what keeps the film's own picture from being shown on this
	// device at all. Each of these means the picture has to be converted.
	var picture []string
	lacks := !c.Video[key]
	switch {
	case lacks:
		picture = append(picture, "Its picture is "+VideoName(v)+", which this browser cannot play.")
	case sizeOf(v.Width, v.Height) == Size4K && !c.Tall[key]:
		picture = append(picture, "Its picture is "+sizeWord(v.Width, v.Height)+", which this device does not say it plays smoothly.")
	}
	if v.DoVi == 5 && !lacks && !c.Video["dovi5"] {
		picture = append(picture, "Its picture is Dolby Vision of a kind (profile 5) that this device cannot show in the right colours.")
	}
	if mode == SubsBurn {
		picture = append(picture, burnReason(sub, c))
	}

	// Second, what keeps the original file from simply being sent although
	// the device could show its picture.
	var other []string
	if !c.Containers[m.Container] {
		other = append(other, "It is packaged as "+containerName(m.Container)+", which this browser cannot open.")
	}
	if a != nil && !c.Audio[a.Codec] {
		other = append(other, "Its sound is "+AudioName(a.Codec)+", which this browser cannot play.")
	}
	// HEVC in an MP4 or MOV carries one of two labels. Apple devices play
	// "hvc1" and refuse "hev1", which ffmpeg and many encoders write. The
	// repackaged stream is always labelled "hvc1" (see package hls), so
	// unless the device says it plays "hev1" too, that is the way to go.
	label := ""
	if v.Codec == "hevc" && v.Tag == "hev1" && (m.Container == "mp4" || m.Container == "mov") && !c.Video["hevc-hev1"] {
		label = `Its HEVC picture carries the label "hev1", which this device does not say it plays (Apple devices want "hvc1"). Repackaging puts the other label on it and leaves the picture itself untouched.`
		other = append(other, label)
	}
	copyable := a == nil || (c.Audio[a.Codec] && packagedAudio[a.Codec])
	// Which sound track a browser plays when simply handed a file is only
	// certain when the file has one, or when the wanted one comes first and
	// no other is marked as the main one (see BrowserAudio). Repackaging
	// names the track, so in any other case that is the way to be sure.
	inDoubt := len(m.Audio) > 1 && (audioTrack != 0 || natural != 0)
	if inDoubt && audioTrack == natural && len(other) == 0 && !(c.HLS && packagedVideo[v.Codec] && (copyable || c.Audio["aac"])) {
		// Nothing else is wrong and repackaging is not possible here: the
		// file plays as it is, with whichever track the browser picks.
		inDoubt = false
	}
	switch {
	case inDoubt && natural != 0:
		other = append(other, "The sound track this file marks as its main one is not its first, and browsers differ in which of the two they play by themselves.")
	case inDoubt:
		other = append(other, "You chose a sound track other than the one a browser plays by itself.")
	}

	// The reasons a converted film is given: why its picture is converted,
	// then whatever else is in the way. The sentence about the label is
	// left out, since it promises an untouched picture.
	why := func(more ...string) []string {
		out := append([]string{}, picture...)
		for _, r := range other {
			if r != label {
				out = append(out, r)
			}
		}
		return append(out, more...)
	}

	if len(picture) == 0 {
		// The device can show the picture as it is. What follows, down to
		// the end of this block, was decided this way before pictures
		// could be converted, and each sentence is still M3's.
		if len(other) == 0 {
			p.AudioCopy = true
			return advised(p, why(), m, a, c, w, l)
		}
		// The original cannot simply be sent. Can it be repackaged?
		if !c.HLS {
			return refuse(p, append(other, "This browser cannot play repackaged video either."))
		}
		if packagedVideo[v.Codec] {
			p.Reasons, p.Method, p.AudioCopy = other, Remux, copyable
			if !copyable {
				if !c.Audio["aac"] {
					return refuse(p, append(other, "This browser reports no sound format the sound could be converted to."))
				}
				p.Method = Audio
				if a.Channels > 2 {
					p.Lost = append(p.Lost, lostSurround)
				}
			}
			return advised(p, why(), m, a, c, w, l)
		}
		if !l.Convert {
			return refuse(p, append(other, "Its picture ("+VideoName(v)+") cannot be repackaged without converting it, which arrives in a later version."))
		}
		return converted(p, why("Its picture ("+VideoName(v)+") cannot be repackaged without converting it."), m, a, c, w, l)
	}

	// The picture has to be converted. Can this PC do that?
	switch {
	case l.Convert:
		return converted(p, why(), m, a, c, w, l)
	case lacks:
		// M3's answer and M3's words, kept while Decide stands in for
		// a PC without picture conversion.
		return refuse(p, []string{"Its picture is " + VideoName(v) + ", which this browser cannot play. Converting the picture arrives in a later version."})
	}
	return cannot(p, why("This PC cannot convert pictures."))
}

// refuse is the answer "this cannot be played here", with the reasons.
func refuse(p Plan, reasons []string) Plan {
	return Plan{Playable: No, Method: None, HasAudio: p.HasAudio, AudioTrack: p.AudioTrack, Reasons: reasons,
		Subtitles: SubsNone, Lost: []string{}}
}

// cannot is refuse where it is this PC, not the device, that stands in the
// way: the plan says so, and so does its one-sentence summary.
func cannot(p Plan, reasons []string) Plan {
	no := refuse(p, reasons)
	no.PCCannot = true
	return no
}

// videoKey is the name a picture format goes by in Caps.Video and
// Caps.Tall.
func videoKey(codec string, bitDepth int) string {
	if codec == "hevc" && bitDepth > 8 {
		return "hevc10"
	}
	if codec == "h264" && bitDepth > 8 {
		return "h264-10"
	}
	return codec
}

// BrowserAudio is the file's own main sound track: the first one flagged
// as default, else the first. It returns -1 for a file with no sound.
//
// It is also the best guess at what a browser plays when simply handed the
// file, but only a guess: Chrome plays the flagged track of an MP4 and the
// first track of an MKV whatever is flagged. Decide therefore sends a file
// with several sound tracks as it is only when this is the first track.
func BrowserAudio(tracks []Track) int {
	for i := range tracks {
		if tracks[i].Default {
			return i
		}
	}
	if len(tracks) > 0 {
		return 0
	}
	return -1
}

func containerName(c string) string {
	switch c {
	case "matroska":
		return "MKV"
	case "mp4":
		return "MP4"
	case "mov":
		return "QuickTime (MOV)"
	case "avi":
		return "AVI"
	case "mpegts":
		return "MPEG-TS"
	case "asf":
		return "WMV"
	case "webm":
		return "WebM"
	case "":
		return "an unknown format"
	}
	return strings.ToUpper(c)
}

// VideoName names a picture format the way people know it.
func VideoName(t Track) string {
	switch t.Codec {
	case "h264":
		if t.BitDepth > 8 {
			return "10-bit H.264"
		}
		return "H.264"
	case "hevc":
		if t.BitDepth > 8 {
			return "10-bit HEVC (H.265)"
		}
		return "HEVC (H.265)"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "vp8":
		return "VP8"
	case "mpeg4":
		return "MPEG-4 (DivX/Xvid)"
	case "mpeg2video":
		return "MPEG-2"
	case "vc1":
		return "VC-1"
	case "wmv3":
		return "WMV"
	}
	return strings.ToUpper(t.Codec)
}

// AudioName names a sound format the way people know it.
func AudioName(codec string) string {
	switch codec {
	case "aac":
		return "AAC"
	case "ac3":
		return "Dolby Digital (AC-3)"
	case "eac3":
		return "Dolby Digital Plus (E-AC-3)"
	case "truehd":
		return "Dolby TrueHD"
	case "dts":
		return "DTS"
	case "flac":
		return "FLAC"
	case "opus":
		return "Opus"
	case "vorbis":
		return "Vorbis"
	case "mp3":
		return "MP3"
	case "mp2":
		return "MP2"
	}
	if strings.HasPrefix(codec, "pcm_") {
		return "uncompressed sound (PCM)"
	}
	return strings.ToUpper(codec)
}

// Explain says in one plain sentence what the server is doing.
func (p Plan) Explain() string {
	switch p.Method {
	case Direct:
		if p.DeviceUnknown {
			return "Sending the original file. This device has not told the app what it can play yet, so the app cannot say in advance whether it will play. Reloading the page makes the device say it again."
		}
		if p.Playable == Unknown {
			return "Sending the original file. It has not been inspected yet, so the app cannot say in advance whether this browser can play it."
		}
		return "Playing the original file, untouched."
	case Remux:
		return "Repackaging the file as it plays. Picture and sound are untouched, so nothing is lost, and it costs the PC almost nothing."
	case Audio:
		return "Repackaging the file and converting its sound as it plays. The picture is untouched; the sound becomes stereo AAC."
	case Convert:
		s := "Converting the picture as it plays"
		if p.Subtitles == SubsBurn {
			s += ", with the subtitles put into it"
		}
		s += ", which is heavy work for the PC."
		switch {
		case !p.HasAudio:
		case p.AudioCopy:
			s += " The sound is untouched."
		default:
			s += " The sound becomes stereo AAC."
		}
		return s
	}
	if p.TooSlow {
		return "This PC cannot convert this film fast enough to watch."
	}
	if p.PCCannot {
		return "This device cannot play this file as it is, and this PC cannot convert it for the device."
	}
	return "This browser cannot play this file yet."
}
