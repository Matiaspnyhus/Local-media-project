package playback

import (
	"strings"
	"testing"
)

func caps(containers, video, audio string) *Caps {
	set := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, k := range strings.Fields(s) {
			m[k] = true
		}
		return m
	}
	return &Caps{Containers: set(containers), Video: set(video), Audio: set(audio)}
}

// What the browsers this project targets report, as measured or documented.
var (
	chrome  = caps("mp4 mov webm matroska", "h264 vp9 vp8 av1", "aac mp3 opus flac vorbis")
	chromeH = caps("mp4 mov webm matroska", "h264 hevc hevc10 vp9 vp8 av1", "aac mp3 opus flac vorbis")
	safari  = caps("mp4 mov", "h264 hevc hevc10", "aac mp3 ac3 eac3 flac")
	firefox = caps("mp4 mov webm", "h264 vp9 vp8 av1", "aac mp3 opus flac vorbis")
)

func media(container, video string, depth int, audio ...string) Media {
	m := Media{Probed: true, Container: container}
	if video != "" {
		m.Video = []Track{{Codec: video, BitDepth: depth}}
	}
	for i, a := range audio {
		m.Audio = append(m.Audio, Track{Codec: a, Default: i == 0})
	}
	return m
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name   string
		m      Media
		c      *Caps
		want   Answer
		reason string // a phrase each "no" must contain
	}{
		{"mp4 h264 aac in chrome", media("mp4", "h264", 8, "aac"), chrome, Yes, ""},
		{"mp4 h264 aac in safari", media("mp4", "h264", 8, "aac"), safari, Yes, ""},
		{"mp4 h264 aac in firefox", media("mp4", "h264", 8, "aac"), firefox, Yes, ""},
		{"iphone video (mov hevc aac) in safari", media("mov", "hevc", 8, "aac"), safari, Yes, ""},
		{"iphone video in chrome without hevc", media("mov", "hevc", 8, "aac"), chrome, No, "HEVC"},
		{"iphone video in chrome with hevc", media("mov", "hevc", 8, "aac"), chromeH, Yes, ""},
		{"mkv h264 aac in chrome", media("matroska", "h264", 8, "aac"), chrome, Yes, ""},
		{"mkv h264 aac in safari", media("matroska", "h264", 8, "aac"), safari, No, "MKV"},
		{"mkv h264 aac in firefox", media("matroska", "h264", 8, "aac"), firefox, No, "MKV"},
		{"mkv h264 ac3 in chrome", media("matroska", "h264", 8, "ac3"), chrome, No, "Dolby Digital"},
		{"mp4 h264 ac3 in safari", media("mp4", "h264", 8, "ac3"), safari, Yes, ""},
		{"mp4 h264 eac3 in safari", media("mp4", "h264", 8, "eac3"), safari, Yes, ""},
		{"mp4 h264 eac3 in chrome", media("mp4", "h264", 8, "eac3"), chrome, No, "Dolby Digital Plus"},
		{"mkv h264 dts in chrome", media("matroska", "h264", 8, "dts"), chrome, No, "DTS"},
		{"mkv hevc10 truehd in safari", media("matroska", "hevc", 10, "truehd"), safari, No, "TrueHD"},
		{"mp4 hevc10 aac in safari", media("mp4", "hevc", 10, "aac"), safari, Yes, ""},
		{"mp4 hevc10 aac in chrome with 8-bit hevc only", media("mp4", "hevc", 10, "aac"), caps("mp4", "h264 hevc", "aac"), No, "10-bit HEVC"},
		{"mp4 av1 opus in chrome", media("mp4", "av1", 8, "opus"), chrome, Yes, ""},
		{"mp4 av1 aac in safari without av1", media("mp4", "av1", 8, "aac"), safari, No, "AV1"},
		{"webm vp9 opus in chrome", media("webm", "vp9", 8, "opus"), chrome, Yes, ""},
		{"webm vp9 opus in safari", media("webm", "vp9", 8, "opus"), safari, No, "WebM"},
		{"avi xvid mp3 anywhere", media("avi", "mpeg4", 8, "mp3"), chrome, No, "AVI"},
		{"avi xvid mp3 names the picture too", media("avi", "mpeg4", 8, "mp3"), chrome, No, "DivX"},
		{"wmv anywhere", media("asf", "wmv3", 8, "wmav2"), chrome, No, "WMV"},
		{"mpeg2 ts", media("mpegts", "mpeg2video", 8, "mp2"), chrome, No, "MPEG-2"},
		{"silent clip plays and is known to be silent", media("mp4", "h264", 8), chrome, Yes, ""},
		{"sound only", media("mp4", "", 0, "aac"), chrome, No, "no picture"},
		{"mkv with flac in chrome", media("matroska", "h264", 8, "flac"), chrome, Yes, ""},
		{"10-bit h264 is not claimed playable when the browser lacks it", media("matroska", "h264", 10, "aac"), caps("matroska", "", "aac"), No, "10-bit H.264"},
	}
	for _, c := range cases {
		got := Decide(c.m, c.c)
		if got.Direct != c.want {
			t.Errorf("%s: %s %v, want %s", c.name, got.Direct, got.Reasons, c.want)
			continue
		}
		if c.want == No && !strings.Contains(strings.Join(got.Reasons, " "), c.reason) {
			t.Errorf("%s: reasons %v do not mention %q", c.name, got.Reasons, c.reason)
		}
		if c.want == Yes && len(got.Reasons) != 0 {
			t.Errorf("%s: a yes came with reasons %v", c.name, got.Reasons)
		}
	}
}

func TestUnknownWhenFactsAreMissing(t *testing.T) {
	if p := Decide(Media{}, chrome); p.Direct != Unknown {
		t.Errorf("an uninspected file got %s", p.Direct)
	}
	if p := Decide(media("mp4", "h264", 8, "aac"), nil); p.Direct != Unknown {
		t.Errorf("a device that reported nothing got %s", p.Direct)
	}
	if p := Decide(media("mp4", "h264", 8), chrome); p.HasAudio {
		t.Error("a silent file was reported as having sound")
	}
}

func TestEveryProblemIsNamed(t *testing.T) {
	p := Decide(media("matroska", "hevc", 10, "dts"), firefox)
	if p.Direct != No || len(p.Reasons) != 3 {
		t.Fatalf("want three reasons (package, picture, sound), got %v", p.Reasons)
	}
}

func TestPickAudioSkipsCommentary(t *testing.T) {
	tracks := []Track{{Codec: "ac3", Default: true, Commentary: true}, {Codec: "aac"}, {Codec: "dts"}}
	if a := PickAudio(tracks); a == nil || a.Codec != "aac" {
		t.Fatalf("picked %+v", a)
	}
	m := Media{Probed: true, Container: "mp4", Video: []Track{{Codec: "h264", BitDepth: 8}}, Audio: tracks}
	if p := Decide(m, chrome); p.Direct != Yes {
		t.Errorf("a commentary track in Dolby made the file unplayable: %v", p.Reasons)
	}
	if PickAudio(nil) != nil {
		t.Error("picked a track from nothing")
	}
	only := []Track{{Codec: "ac3", Commentary: true}}
	if a := PickAudio(only); a == nil || a.Codec != "ac3" {
		t.Error("with only a commentary track, that track is the sound")
	}
}
