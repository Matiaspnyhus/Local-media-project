// Package playback decides how a file is played on a device. It is made of
// pure functions: what ffprobe said about the file, what the device says it
// can play and what the viewer wants go in; a decision with reasons in
// plain words comes out.
//
// There are four possible answers, tried in this order because each costs
// more than the one before:
//
//	direct   send the original file as it is
//	remux    repackage it without touching picture or sound (no quality
//	         loss, almost no work for the PC)
//	audio    repackage it and convert only the sound
//	none     this device cannot play it until the picture is converted too,
//	         which arrives in M4
package playback

import "strings"

// Caps is what a device reports it can play. Keys are ffprobe's names:
// containers "mp4", "matroska", "webm", "mov"; codecs "h264", "hevc", "ac3".
// "hevc10" stands for 10-bit HEVC, which many devices that play HEVC cannot.
type Caps struct {
	Containers map[string]bool `json:"containers"`
	Video      map[string]bool `json:"video"`
	Audio      map[string]bool `json:"audio"`
	// HLS says the device can play repackaged video (the chunked format
	// phones and browsers accept), natively or through the page's player.
	HLS bool `json:"hls"`
}

// Track is one video, audio or subtitle track of a file.
type Track struct {
	Codec           string
	BitDepth        int
	Channels        int
	Default         bool
	Forced          bool
	Commentary      bool
	HearingImpaired bool
	Language        string
	Title           string
}

// Media is what is known about a file.
type Media struct {
	Probed    bool // false: the file has not been inspected
	Container string
	Video     []Track
	Audio     []Track
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
	Direct Method = "direct"
	Remux  Method = "remux"
	Audio  Method = "audio"
	None   Method = "none"
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
}

// packaged lists what the repackaged format can carry. Anything else in a
// file has to be converted before it can go into it.
var (
	packagedVideo = map[string]bool{"h264": true, "hevc": true, "av1": true}
	packagedAudio = map[string]bool{"aac": true, "ac3": true, "eac3": true}
)

// Decide chooses how to play a file. audioTrack is the sound track the
// viewer wants, counted from 0, or -1 to let BrowserAudio's choice stand.
func Decide(m Media, c *Caps, audioTrack int) Plan {
	if !m.Probed {
		// Nothing is known: send the file and let the player find out.
		return Plan{Playable: Unknown, Method: Direct, HasAudio: true, AudioTrack: -1, Reasons: []string{}}
	}
	p := Plan{Playable: Yes, Method: Direct, HasAudio: len(m.Audio) > 0, AudioTrack: -1, Reasons: []string{}}
	if len(m.Video) == 0 {
		return Plan{Playable: No, Method: None, HasAudio: p.HasAudio, AudioTrack: -1, Reasons: []string{"This file has no picture track."}}
	}
	if c == nil || c.Containers == nil {
		p.Playable, p.DeviceUnknown = Unknown, true
		return p
	}
	natural := BrowserAudio(m.Audio)
	if audioTrack < 0 || audioTrack >= len(m.Audio) {
		audioTrack = natural
	}
	p.AudioTrack = audioTrack

	v := m.Video[0]
	key := v.Codec
	if v.Codec == "hevc" && v.BitDepth > 8 {
		key = "hevc10"
	}
	if v.Codec == "h264" && v.BitDepth > 8 {
		key = "h264-10"
	}
	if !c.Video[key] {
		return Plan{Playable: No, Method: None, HasAudio: p.HasAudio, AudioTrack: audioTrack,
			Reasons: []string{"Its picture is " + VideoName(v) + ", which this browser cannot play. Converting the picture arrives in a later version."}}
	}
	var a *Track
	if audioTrack >= 0 {
		a = &m.Audio[audioTrack]
	}
	if !c.Containers[m.Container] {
		p.Reasons = append(p.Reasons, "It is packaged as "+containerName(m.Container)+", which this browser cannot open.")
	}
	if a != nil && !c.Audio[a.Codec] {
		p.Reasons = append(p.Reasons, "Its sound is "+AudioName(a.Codec)+", which this browser cannot play.")
	}
	if a != nil && audioTrack != natural {
		p.Reasons = append(p.Reasons, "You chose a sound track other than the one a browser plays by itself.")
	}
	if len(p.Reasons) == 0 {
		p.AudioCopy = true
		return p
	}
	// The original cannot simply be sent. Can it be repackaged?
	if !c.HLS {
		p.Playable, p.Method = No, None
		p.Reasons = append(p.Reasons, "This browser cannot play repackaged video either.")
		return p
	}
	if !packagedVideo[v.Codec] {
		return Plan{Playable: No, Method: None, HasAudio: p.HasAudio, AudioTrack: audioTrack,
			Reasons: append(p.Reasons, "Its picture ("+VideoName(v)+") cannot be repackaged without converting it, which arrives in a later version.")}
	}
	p.Method = Remux
	p.AudioCopy = a == nil || (c.Audio[a.Codec] && packagedAudio[a.Codec])
	if !p.AudioCopy {
		if !c.Audio["aac"] {
			p.Playable, p.Method = No, None
			p.Reasons = append(p.Reasons, "This browser reports no sound format the sound could be converted to.")
			return p
		}
		p.Method = Audio
	}
	return p
}

// BrowserAudio is the sound track a browser plays when simply handed the
// file: the first one flagged as default, else the first. It returns -1
// for a file with no sound.
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
	}
	return "This browser cannot play this file yet."
}
