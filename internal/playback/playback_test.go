package playback

import (
	"fmt"
	"strings"
	"testing"
)

func caps(hls bool, containers, video, audio string) *Caps {
	set := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, k := range strings.Fields(s) {
			m[k] = true
		}
		return m
	}
	return &Caps{Containers: set(containers), Video: set(video), Audio: set(audio), HLS: hls}
}

// What the browsers this project targets report, as measured or documented.
var devices = map[string]*Caps{
	"chrome":      caps(true, "mp4 mov webm matroska", "h264 h264-10 vp9 vp8 av1", "aac mp3 opus flac vorbis"),
	"chrome+hevc": caps(true, "mp4 mov webm matroska", "h264 h264-10 hevc hevc10 vp9 vp8 av1", "aac mp3 opus flac vorbis"),
	"safari":      caps(true, "mp4 mov", "h264 hevc hevc10", "aac mp3 ac3 eac3 flac"),
	"firefox":     caps(true, "mp4 mov webm", "h264 vp9 vp8 av1", "aac mp3 opus flac vorbis"),
	"old-tv":      caps(false, "mp4", "h264", "aac mp3"),
}

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
		name, device string
		m            Media
		want         Method
		reason       string // a phrase the reasons must contain ("" = no reasons)
	}{
		{"mp4 h264 aac", "chrome", media("mp4", "h264", 8, "aac"), Direct, ""},
		{"mp4 h264 aac", "safari", media("mp4", "h264", 8, "aac"), Direct, ""},
		{"mp4 h264 aac", "firefox", media("mp4", "h264", 8, "aac"), Direct, ""},
		{"mp4 h264 aac", "old-tv", media("mp4", "h264", 8, "aac"), Direct, ""},
		{"iphone video (mov hevc aac)", "safari", media("mov", "hevc", 8, "aac"), Direct, ""},
		{"iphone video", "chrome", media("mov", "hevc", 8, "aac"), None, "HEVC"},
		{"iphone video", "chrome+hevc", media("mov", "hevc", 8, "aac"), Direct, ""},
		{"mkv h264 aac", "chrome", media("matroska", "h264", 8, "aac"), Direct, ""},
		// The owner's iPhone: Safari cannot open MKV, so it is repackaged.
		{"mkv h264 aac", "safari", media("matroska", "h264", 8, "aac"), Remux, "MKV"},
		{"mkv h264 aac", "firefox", media("matroska", "h264", 8, "aac"), Remux, "MKV"},
		{"mkv h264 aac", "old-tv", media("matroska", "h264", 8, "aac"), None, "repackaged"},
		// Dolby in Chrome: only the sound is converted.
		{"mkv h264 ac3", "chrome", media("matroska", "h264", 8, "ac3"), Audio, "Dolby Digital"},
		{"mp4 h264 eac3", "chrome", media("mp4", "h264", 8, "eac3"), Audio, "Dolby Digital Plus"},
		// Dolby in Safari: MKV is repackaged and the Dolby sound is kept.
		{"mkv h264 ac3", "safari", media("matroska", "h264", 8, "ac3"), Remux, "MKV"},
		{"mkv h264 eac3", "safari", media("matroska", "h264", 8, "eac3"), Remux, "MKV"},
		{"mp4 h264 ac3", "safari", media("mp4", "h264", 8, "ac3"), Direct, ""},
		{"mkv h264 dts", "chrome", media("matroska", "h264", 8, "dts"), Audio, "DTS"},
		{"mkv h264 dts", "safari", media("matroska", "h264", 8, "dts"), Audio, "DTS"},
		{"mkv h264 truehd", "firefox", media("matroska", "h264", 8, "truehd"), Audio, "TrueHD"},
		{"mkv hevc10 truehd", "safari", media("matroska", "hevc", 10, "truehd"), Audio, "TrueHD"},
		{"mkv hevc10 eac3", "safari", media("matroska", "hevc", 10, "eac3"), Remux, "MKV"},
		{"mp4 hevc10 aac", "safari", media("mp4", "hevc", 10, "aac"), Direct, ""},
		{"mkv hevc10 aac", "chrome", media("matroska", "hevc", 10, "aac"), None, "10-bit HEVC"},
		{"mp4 hevc10 aac, 8-bit hevc only", "", media("mp4", "hevc", 10, "aac"), None, "10-bit HEVC"},
		{"mp4 av1 opus", "chrome", media("mp4", "av1", 8, "opus"), Direct, ""},
		{"mp4 av1 aac", "safari", media("mp4", "av1", 8, "aac"), None, "AV1"},
		{"webm vp9 opus", "chrome", media("webm", "vp9", 8, "opus"), Direct, ""},
		// VP9 cannot go into the repackaged format, and Safari lacks it anyway.
		{"webm vp9 opus", "safari", media("webm", "vp9", 8, "opus"), None, "VP9"},
		{"avi xvid mp3", "chrome", media("avi", "mpeg4", 8, "mp3"), None, "DivX"},
		{"wmv", "chrome", media("asf", "wmv3", 8, "wmav2"), None, "WMV"},
		{"mpeg2 ts", "chrome", media("mpegts", "mpeg2video", 8, "mp2"), None, "MPEG-2"},
		{"ts h264 aac", "chrome", media("mpegts", "h264", 8, "aac"), Remux, "MPEG-TS"},
		{"avi h264 mp3", "chrome", media("avi", "h264", 8, "mp3"), Audio, "AVI"},
		{"silent clip", "chrome", media("mp4", "h264", 8), Direct, ""},
		{"silent mkv", "safari", media("matroska", "h264", 8), Remux, "MKV"},
		{"sound only", "chrome", media("mp4", "", 0, "aac"), None, "no picture"},
		{"mkv h264 flac", "chrome", media("matroska", "h264", 8, "flac"), Direct, ""},
		// FLAC plays in Safari but cannot be passed through when repackaging.
		{"mkv h264 flac", "safari", media("matroska", "h264", 8, "flac"), Audio, "MKV"},
		// 10-bit H.264 (common in anime) plays in Chrome, which says so
		// under the key "h264-10"; Safari and Firefox do not say so. This
		// case used to demand that Chrome refuse it, which was the bug: the
		// page never reported that key.
		{"10-bit h264", "chrome", media("matroska", "h264", 10, "aac"), Direct, ""},
		{"10-bit h264 with Dolby sound", "chrome", media("matroska", "h264", 10, "ac3"), Audio, "Dolby Digital"},
		{"10-bit h264", "safari", media("mp4", "h264", 10, "aac"), None, "10-bit H.264"},
		{"10-bit h264", "firefox", media("mp4", "h264", 10, "aac"), None, "10-bit H.264"},
		// WebM is not MKV: Firefox plays it although it does not open MKV.
		{"webm vp9 opus", "firefox", media("webm", "vp9", 8, "opus"), Direct, ""},
		{"webm av1 opus", "firefox", media("webm", "av1", 8, "opus"), Direct, ""},
		// And where it cannot be opened, it is called by its own name.
		{"a .webm on a device without WebM", "safari", media("webm", "h264", 8, "aac"), Remux, "packaged as WebM"},
	}
	for _, c := range cases {
		dev := devices[c.device]
		if dev == nil {
			dev = caps(true, "mp4", "h264 hevc", "aac")
		}
		got := Decide(c.m, dev, -1)
		name := c.name + " on " + c.device
		if got.Method != c.want {
			t.Errorf("%s: %s %v, want %s", name, got.Method, got.Reasons, c.want)
			continue
		}
		joined := strings.Join(got.Reasons, " ")
		if c.reason == "" && len(got.Reasons) != 0 {
			t.Errorf("%s: playing the original came with reasons %v", name, got.Reasons)
		}
		if c.reason != "" && !strings.Contains(joined, c.reason) {
			t.Errorf("%s: reasons %v do not mention %q", name, got.Reasons, c.reason)
		}
		if (got.Method == None) != (got.Playable == No) {
			t.Errorf("%s: method %s but playable %s", name, got.Method, got.Playable)
		}
	}
}

// Every combination of package, picture, sound and device must obey the
// rules below, whatever the answer is. This is where most of the cases are.
func TestDecideRulesHoldForEveryCombination(t *testing.T) {
	containers := []string{"mp4", "mov", "matroska", "webm", "avi", "mpegts"}
	videos := []struct {
		codec string
		depth int
	}{{"h264", 8}, {"h264", 10}, {"hevc", 8}, {"hevc", 10}, {"av1", 8}, {"vp9", 8}, {"mpeg4", 8}}
	audios := []string{"", "aac", "ac3", "eac3", "dts", "truehd", "flac", "opus", "mp3"}
	n := 0
	for dname, dev := range devices {
		for _, cont := range containers {
			for _, v := range videos {
				for _, a := range audios {
					var m Media
					if a == "" {
						m = media(cont, v.codec, v.depth)
					} else {
						m = media(cont, v.codec, v.depth, a)
					}
					p := Decide(m, dev, -1)
					n++
					id := fmt.Sprintf("%s %s/%d %s on %s -> %s", cont, v.codec, v.depth, a, dname, p.Method)
					key := v.codec
					if v.depth > 8 {
						key = map[string]string{"hevc": "hevc10", "h264": "h264-10"}[v.codec]
					}
					videoOK := dev.Video[key]
					audioOK := a == "" || dev.Audio[a]
					switch p.Method {
					case Direct:
						if !dev.Containers[cont] || !videoOK || !audioOK {
							t.Errorf("%s: the original was sent to a device that cannot play it", id)
						}
					case Remux, Audio:
						if !dev.HLS || !videoOK || !packagedVideo[v.codec] {
							t.Errorf("%s: repackaged for a device that cannot take it", id)
						}
						if dev.Containers[cont] && audioOK {
							t.Errorf("%s: repackaged although the original would have played", id)
						}
						if p.Method == Remux && a != "" && !(dev.Audio[a] && packagedAudio[a]) {
							t.Errorf("%s: sound passed through that the device or the format cannot take", id)
						}
						if p.Method == Audio && dev.Audio[a] && packagedAudio[a] {
							t.Errorf("%s: sound converted although it could have been passed through", id)
						}
					case None:
						if dev.Containers[cont] && videoOK && audioOK {
							t.Errorf("%s: refused although the original would have played", id)
						}
						if dev.HLS && videoOK && packagedVideo[v.codec] && dev.Audio["aac"] {
							t.Errorf("%s: refused although repackaging would have worked", id)
						}
					}
					if p.Method != Direct && len(p.Reasons) == 0 {
						t.Errorf("%s: no reason given", id)
					}
					if p.HasAudio != (a != "") || p.AudioCopy != (p.Method == Direct || p.Method == Remux) && p.Method != None {
						t.Errorf("%s: hasAudio=%v audioCopy=%v", id, p.HasAudio, p.AudioCopy)
					}
					if p.Explain() == "" {
						t.Errorf("%s: nothing to tell the viewer", id)
					}
				}
			}
		}
	}
	if n < 200 {
		t.Fatalf("only %d combinations checked", n)
	}
	t.Logf("%d combinations checked", n)
}

func TestUnknownWhenFactsAreMissing(t *testing.T) {
	if p := Decide(Media{}, devices["chrome"], -1); p.Playable != Unknown || p.Method != Direct {
		t.Errorf("an uninspected file got %+v", p)
	}
	if p := Decide(Media{}, devices["chrome"], -1); p.DeviceUnknown || !strings.Contains(p.Explain(), "not been inspected") {
		t.Errorf("an uninspected file is explained with %q", p.Explain())
	}
	// The file is known and the device is not: say that, not that the file
	// has not been inspected.
	for _, dev := range []*Caps{nil, {}} {
		p := Decide(media("mp4", "h264", 8, "aac"), dev, -1)
		if p.Playable != Unknown || p.Method != Direct || !p.DeviceUnknown {
			t.Errorf("a device that reported nothing got %+v", p)
		}
		if e := p.Explain(); strings.Contains(e, "inspected") || !strings.Contains(e, "This device has not told the app what it can play") {
			t.Errorf("a device that reported nothing is explained with %q", e)
		}
	}
	if p := Decide(media("mp4", "h264", 8), devices["chrome"], -1); p.HasAudio || p.AudioTrack != -1 {
		t.Errorf("a silent file: %+v", p)
	}
}

func TestEveryProblemIsNamed(t *testing.T) {
	p := Decide(media("matroska", "h264", 8, "dts"), devices["firefox"], -1)
	if p.Method != Audio || len(p.Reasons) != 2 {
		t.Fatalf("want two reasons (package and sound), got %s %v", p.Method, p.Reasons)
	}
}

func TestChoosingAnotherSoundTrack(t *testing.T) {
	m := Media{Probed: true, Container: "mp4", Video: []Track{{Codec: "h264", BitDepth: 8}},
		Audio: []Track{{Codec: "aac", Default: true, Language: "eng"}, {Codec: "aac", Language: "jpn"}, {Codec: "dts", Language: "nor"}}}
	if p := Decide(m, devices["chrome"], -1); p.Method != Direct || p.AudioTrack != 0 {
		t.Fatalf("by default: %+v", p)
	}
	// A browser handed this file plays its first track, which is also the
	// one marked as main. Another track means repackaging, which costs
	// nothing.
	if p := Decide(m, devices["chrome"], 1); p.Method != Remux || p.AudioTrack != 1 || !strings.Contains(strings.Join(p.Reasons, " "), "sound track") {
		t.Fatalf("second track: %+v", p)
	}
	if p := Decide(m, devices["chrome"], 2); p.Method != Audio || p.AudioTrack != 2 {
		t.Fatalf("a DTS track: %+v", p)
	}
	if p := Decide(m, devices["chrome"], 9); p.AudioTrack != 0 {
		t.Fatalf("a track that does not exist: %+v", p)
	}
	if p := Decide(m, devices["old-tv"], 1); p.Method != None {
		t.Fatalf("another track on a device that cannot take repackaged video: %+v", p)
	}
}

// Browsers do not agree on which sound track of a file they play by
// themselves: Chrome plays the track an MP4 marks as main, but the first
// track of an MKV whatever it marks. So the original is only sent when
// there is no doubt: the wanted track is the first one and the file does
// not mark another as main, or there is only one.
func TestTheMainSoundTrackIsNotTheFirst(t *testing.T) {
	// What MKVToolNix makes when someone sets the default language without
	// reordering: Japanese first, English marked as the main track.
	m := Media{Probed: true, Container: "matroska", Video: []Track{{Codec: "h264", BitDepth: 8}},
		Audio: []Track{{Codec: "aac", Language: "jpn"}, {Codec: "aac", Language: "eng", Default: true}}}
	for _, track := range []int{-1, 0, 1} {
		want := track
		if track < 0 {
			want = 1 // the one the file marks as main
		}
		p := Decide(m, devices["chrome"], track)
		if p.Method != Remux || !p.AudioCopy || p.AudioTrack != want || !strings.Contains(strings.Join(p.Reasons, " "), "sound track") {
			t.Errorf("asking for track %d: %+v; want it repackaged with track %d and a reason that names the sound tracks", track, p, want)
		}
	}
	// Where the file cannot be repackaged, it is still sent rather than
	// refused: it plays, with the track the browser picks.
	if p := Decide(m, devices["old-tv"], -1); p.Method != None {
		t.Errorf("an MKV on a device that opens only MP4: %+v", p)
	}
	mp4 := m
	mp4.Container = "mp4"
	if p := Decide(mp4, devices["old-tv"], -1); p.Method != Direct || p.Playable != Yes || len(p.Reasons) != 0 || p.AudioTrack != 1 {
		t.Errorf("a device that cannot take repackaged video: %+v; want the original sent", p)
	}
	webm := Media{Probed: true, Container: "webm", Video: []Track{{Codec: "vp9", BitDepth: 8}},
		Audio: []Track{{Codec: "opus"}, {Codec: "opus", Default: true}}}
	if p := Decide(webm, devices["chrome"], -1); p.Method != Direct || p.Playable != Yes || len(p.Reasons) != 0 {
		t.Errorf("a picture that cannot be repackaged: %+v; want the original sent", p)
	}
	// But a track the viewer asked for by hand is not quietly swapped.
	if p := Decide(webm, devices["chrome"], 0); p.Method != None {
		t.Errorf("asking for the other track of a file that cannot be repackaged: %+v", p)
	}

	// No doubt, no repackaging: one track, whatever it is marked as; and
	// several with the first as the main one or none marked at all.
	for name, audio := range map[string][]Track{
		"one track, not marked":         {{Codec: "aac"}},
		"two tracks, the first is main": {{Codec: "aac", Default: true}, {Codec: "aac"}},
		"two tracks, none marked":       {{Codec: "aac"}, {Codec: "aac"}},
		"two tracks, both marked":       {{Codec: "aac", Default: true}, {Codec: "aac", Default: true}},
	} {
		m.Audio = audio
		if p := Decide(m, devices["chrome"], -1); p.Method != Direct || p.AudioTrack != 0 || len(p.Reasons) != 0 {
			t.Errorf("%s: %+v; want the original sent with its first track", name, p)
		}
	}
}
