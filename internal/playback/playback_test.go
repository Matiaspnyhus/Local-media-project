package playback

import (
	"fmt"
	"reflect"
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

// The packages, pictures and sounds that M3's table of combinations was
// built from. With the five devices they make 1,890 combinations. The
// table has since grown by size, colour, subtitles and surface (see
// TestRulesHoldForEveryCombination in convert_test.go).
var (
	oldContainers = []string{"mp4", "mov", "matroska", "webm", "avi", "mpegts"}
	oldVideos     = []struct {
		codec string
		depth int
	}{{"h264", 8}, {"h264", 10}, {"hevc", 8}, {"hevc", 10}, {"av1", 8}, {"vp9", 8}, {"mpeg4", 8}}
	oldAudios = []string{"", "aac", "ac3", "eac3", "dts", "truehd", "flac", "opus", "mp3"}
)

// Every combination of package, picture, sound and device must obey the
// rules below, whatever the answer is. This is M3's table, for a PC that
// cannot convert pictures.
func TestDecideRulesHoldForEveryCombination(t *testing.T) {
	containers, videos, audios := oldContainers, oldVideos, oldAudios
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

// HEVC in an MP4 or MOV carries one of two labels. Apple devices play
// "hvc1" and refuse "hev1", which is what ffmpeg and many encoders write.
// Repackaging writes "hvc1", so a file labelled "hev1" goes that way unless
// the device says it plays that label too (key "hevc-hev1").
func TestHEVCLabelledHev1IsRepackaged(t *testing.T) {
	file := func(container, label string, depth int) Media {
		m := media(container, "hevc", depth, "aac")
		m.Video[0].Tag = label
		return m
	}
	both := caps(true, "mp4 mov matroska", "h264 hevc hevc10 hevc-hev1", "aac")
	for _, c := range []struct {
		name string
		m    Media
		dev  *Caps
		want Method
	}{
		{"hev1 on an iPhone", file("mp4", "hev1", 8), devices["safari"], Remux},
		{"10-bit hev1 on an iPhone", file("mp4", "hev1", 10), devices["safari"], Remux},
		{"hev1 in a .mov", file("mov", "hev1", 8), devices["safari"], Remux},
		{"hvc1 on an iPhone", file("mp4", "hvc1", 8), devices["safari"], Direct},
		{"a label not yet read (inspected by an earlier version)", file("mp4", "", 8), devices["safari"], Direct},
		{"hev1 on a device that says it plays it", file("mp4", "hev1", 8), both, Direct},
		{"hev1 on a device that has not said", file("mp4", "hev1", 8), devices["chrome+hevc"], Remux},
		{"hev1 where repackaging is not possible", file("mp4", "hev1", 8), caps(false, "mp4", "hevc", "aac"), None},
		{"an MKV has no such label; only its packaging is in the way", file("matroska", "", 8), devices["safari"], Remux},
	} {
		p := Decide(c.m, c.dev, -1)
		reasons := strings.Join(p.Reasons, " ")
		if p.Method != c.want {
			t.Errorf("%s: %s %v, want %s", c.name, p.Method, p.Reasons, c.want)
			continue
		}
		if labelled := strings.Contains(reasons, "hev1"); labelled != (c.m.Video[0].Tag == "hev1" && c.want != Direct) {
			t.Errorf("%s: reasons %v", c.name, p.Reasons)
		}
		if c.want == Remux && !p.AudioCopy {
			t.Errorf("%s: relabelling converted the sound", c.name)
		}
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

// A PC without a converter cannot repackage anything. The answer then
// names the PC, not the browser, which may well play repackaged video.
// (Before, the server hid the missing converter by telling the planner
// that the device takes no repackaged video, and the viewer was told
// "This browser cannot play repackaged video either.")
func TestAPCWithoutAConverterIsNamed(t *testing.T) {
	none := Limits{NoConverter: true}
	pcCannot := "This device cannot play this file as it is, and this PC cannot convert it for the device."
	mkv := media("matroska", "h264", 8, "aac")

	p := DecideFor(mkv, devices["safari"], Want{Audio: -1, Subtitle: -1}, none)
	want := []string{"It is packaged as MKV, which this browser cannot open.", "This PC has no converter, so the file cannot be repackaged for this device."}
	if p.Playable != No || !sameStrings(p.Reasons, want) || !p.PCCannot || p.TooSlow || p.Explain() != pcCannot {
		t.Errorf("an MKV on an iPhone, no converter: %+v, explained as %q", p, p.Explain())
	}
	// A device that takes no repackaged video is still told so itself,
	// with or without a converter on the PC.
	for _, l := range []Limits{{}, none} {
		p = DecideFor(mkv, devices["old-tv"], Want{Audio: -1, Subtitle: -1}, l)
		if p.Playable != No || !has(p.Reasons, "This browser cannot play repackaged video either.") || p.PCCannot || p.Explain() != "This browser cannot play this file yet." {
			t.Errorf("an MKV on a device that takes no repackaged video (%+v): %+v", l, p)
		}
	}
	// A picture the device cannot show: it is the PC that cannot convert
	// it, and nothing is promised for a later version.
	p = DecideFor(media("avi", "mpeg4", 8, "mp3"), devices["chrome"], Want{Audio: -1, Subtitle: -1}, none)
	want = []string{"Its picture is MPEG-4 (DivX/Xvid), which this browser cannot play.", "It is packaged as AVI, which this browser cannot open.",
		"This PC has no converter, so the picture cannot be converted for this device."}
	if p.Playable != No || !sameStrings(p.Reasons, want) || !p.PCCannot || p.Explain() != pcCannot {
		t.Errorf("DivX in Chrome, no converter: %+v, explained as %q", p, p.Explain())
	}
	// Having no converter wins over a claim to convert pictures.
	if q := DecideFor(media("avi", "mpeg4", 8, "mp3"), devices["chrome"], Want{Audio: -1, Subtitle: -1}, Limits{NoConverter: true, Convert: true, ToneMap: true}); !reflect.DeepEqual(q, p) {
		t.Errorf("no converter, and a claim to convert pictures: %+v", q)
	}
	// Whatever plays as it is still plays, and a sound track that only
	// repackaging could reach gives way to the one the browser plays, as
	// it does on a device that takes no repackaged video.
	if p = DecideFor(media("mp4", "h264", 8, "aac"), devices["chrome"], Want{Audio: -1, Subtitle: -1}, none); p.Method != Direct || len(p.Reasons) != 0 {
		t.Errorf("an MP4 in Chrome, no converter: %+v", p)
	}
	two := Media{Probed: true, Container: "matroska", Video: []Track{{Codec: "h264", BitDepth: 8}},
		Audio: []Track{{Codec: "aac", Language: "jpn"}, {Codec: "aac", Language: "eng", Default: true}}}
	if p = DecideFor(two, devices["chrome"], Want{Audio: -1, Subtitle: -1}, none); p.Method != Direct || p.Playable != Yes || len(p.Reasons) != 0 {
		t.Errorf("a main track that is not the first, no converter: %+v; want the original sent", p)
	}
	// For every one of M3's combinations the answer is the one the server
	// used to get by hiding the device's word on repackaged video, except
	// for who is named.
	for dname, dev := range devices {
		hidden := *dev
		hidden.HLS = false
		for _, cont := range oldContainers {
			for _, v := range oldVideos {
				for _, a := range oldAudios {
					m := media(cont, v.codec, v.depth)
					if a != "" {
						m = media(cont, v.codec, v.depth, a)
					}
					got, old := DecideFor(m, dev, Want{Audio: -1, Subtitle: -1}, none), Decide(m, &hidden, -1)
					if got.Playable != old.Playable || got.Method != old.Method || got.AudioTrack != old.AudioTrack || got.AudioCopy != old.AudioCopy {
						t.Errorf("%s %s/%d %s on %s: %s with no converter, %s with repackaging hidden", cont, v.codec, v.depth, a, dname, got.Method, old.Method)
					}
					if got.Playable == No && dev.HLS && (!got.PCCannot || has(got.Reasons, "later version") || has(got.Reasons, "This browser cannot play repackaged video")) {
						t.Errorf("%s %s/%d %s on %s, no converter: %q", cont, v.codec, v.depth, a, dname, got.Reasons)
					}
				}
			}
		}
	}
}
