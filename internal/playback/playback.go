// Package playback decides how a file can be played on a device. It is a
// pure function: what ffprobe said about the file and what the device says
// it can play go in, a decision with reasons in plain words comes out.
//
// In M1 the only question is "can this browser play the original file as it
// is?". M3 adds the other answers (repackage, convert the sound, convert
// everything) to the same function.
package playback

import "strings"

// Caps is what a device reports it can play. Keys are ffprobe's names:
// containers "mp4", "matroska", "webm", "mov"; codecs "h264", "hevc", "ac3".
// "hevc10" stands for 10-bit HEVC, which many devices that play HEVC cannot.
type Caps struct {
	Containers map[string]bool `json:"containers"`
	Video      map[string]bool `json:"video"`
	Audio      map[string]bool `json:"audio"`
}

// Track is one video or audio track of a file.
type Track struct {
	Codec      string
	BitDepth   int
	Default    bool
	Commentary bool
	Language   string
}

// Media is what is known about a file.
type Media struct {
	Probed    bool // false: the file has not been inspected
	Container string
	Video     []Track
	Audio     []Track
}

// Answer is the decision.
type Answer string

const (
	Yes     Answer = "yes"     // the browser can play the original file
	No      Answer = "no"      // it cannot; conversion is needed
	Unknown Answer = "unknown" // not enough is known to say
)

// Plan is a decision and why.
type Plan struct {
	Direct   Answer   `json:"direct"`
	Reasons  []string `json:"reasons"`
	HasAudio bool     `json:"hasAudio"`
}

// Decide answers whether the device can play the original file.
func Decide(m Media, c *Caps) Plan {
	if !m.Probed {
		return Plan{Direct: Unknown, HasAudio: true}
	}
	p := Plan{Direct: Yes, HasAudio: len(m.Audio) > 0}
	if len(m.Video) == 0 {
		return Plan{Direct: No, HasAudio: p.HasAudio, Reasons: []string{"This file has no picture track."}}
	}
	if c == nil || c.Containers == nil {
		p.Direct = Unknown
		return p
	}
	if !c.Containers[m.Container] {
		p.Reasons = append(p.Reasons, "It is packaged as "+containerName(m.Container)+", which this browser cannot open.")
	}
	v := m.Video[0]
	key := v.Codec
	if v.Codec == "hevc" && v.BitDepth > 8 {
		key = "hevc10"
	}
	if !c.Video[key] {
		p.Reasons = append(p.Reasons, "Its picture is "+VideoName(v)+", which this browser cannot play.")
	}
	if a := PickAudio(m.Audio); a != nil && !c.Audio[a.Codec] {
		p.Reasons = append(p.Reasons, "Its sound is "+AudioName(a.Codec)+", which this browser cannot play.")
	}
	if len(p.Reasons) > 0 {
		p.Direct = No
	}
	return p
}

// PickAudio chooses the track a player would start with: the default one
// if it is not a commentary, else the first that is not a commentary.
func PickAudio(tracks []Track) *Track {
	for i := range tracks {
		if tracks[i].Default && !tracks[i].Commentary {
			return &tracks[i]
		}
	}
	for i := range tracks {
		if !tracks[i].Commentary {
			return &tracks[i]
		}
	}
	if len(tracks) > 0 {
		return &tracks[0]
	}
	return nil
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
