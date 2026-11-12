package playback

import (
	"fmt"
	"reflect"
	"testing"
)

// decideM3 is Decide exactly as it was before pictures could be converted
// (the M3 release), kept here word for word so that the new planner can be
// held to it. Do not tidy or correct it: it is a record, not code in use.
func decideM3(m Media, c *Caps, audioTrack int) Plan {
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
	if v.Codec == "hevc" && v.Tag == "hev1" && (m.Container == "mp4" || m.Container == "mov") && !c.Video["hevc-hev1"] {
		p.Reasons = append(p.Reasons, `Its HEVC picture carries the label "hev1", which this device does not say it plays (Apple devices want "hvc1"). Repackaging puts the other label on it and leaves the picture itself untouched.`)
	}
	copyable := a == nil || (c.Audio[a.Codec] && packagedAudio[a.Codec])
	inDoubt := len(m.Audio) > 1 && (audioTrack != 0 || natural != 0)
	if inDoubt && audioTrack == natural && len(p.Reasons) == 0 && !(c.HLS && packagedVideo[v.Codec] && (copyable || c.Audio["aac"])) {
		inDoubt = false
	}
	switch {
	case inDoubt && natural != 0:
		p.Reasons = append(p.Reasons, "The sound track this file marks as its main one is not its first, and browsers differ in which of the two they play by themselves.")
	case inDoubt:
		p.Reasons = append(p.Reasons, "You chose a sound track other than the one a browser plays by itself.")
	}
	if len(p.Reasons) == 0 {
		p.AudioCopy = true
		return p
	}
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
	p.AudioCopy = copyable
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

// sameAsM3 compares an answer with what M3 gave: every field M3 had must
// be equal, word for word, and nothing M4 added may say anything.
func sameAsM3(got, m3 Plan) string {
	if got.Playable != m3.Playable || got.Method != m3.Method || got.HasAudio != m3.HasAudio || got.AudioTrack != m3.AudioTrack ||
		got.AudioCopy != m3.AudioCopy || got.DeviceUnknown != m3.DeviceUnknown {
		return fmt.Sprintf("got %+v, M3 gave %+v", got, m3)
	}
	if got.Reasons == nil || !reflect.DeepEqual(got.Reasons, m3.Reasons) {
		return fmt.Sprintf("reasons %q, M3 gave %q", got.Reasons, m3.Reasons)
	}
	if got.Explain() != m3.Explain() {
		return fmt.Sprintf("explained as %q, M3 said %q", got.Explain(), m3.Explain())
	}
	if got.Video != (Video{}) || got.Subtitles != SubsNone || got.SubtitleKind != "" || len(got.Lost) != 0 {
		return fmt.Sprintf("a plan without conversion says something about conversion: %+v", got)
	}
	return ""
}

// With no picture conversion (Limits{}), every answer must be exactly the
// one M3 gave: the method, the tracks and the reasons, word for word. This
// walks the same 1,890 combinations as M3's own table, once with the
// devices as M3 knew them and once with everything a device can say since,
// then the cases M3's other tests were written for: a sound track chosen by
// hand, a main track that is not the first, the "hev1" label, a file not
// yet inspected and a device that has said nothing.
func TestWithoutConversionNothingChanged(t *testing.T) {
	n := 0
	check := func(id string, m Media, dev *Caps, track int) {
		t.Helper()
		n++
		m3 := decideM3(m, dev, track)
		if diff := sameAsM3(Decide(m, dev, track), m3); diff != "" {
			t.Errorf("%s: Decide: %s", id, diff)
		}
		if diff := sameAsM3(DecideFor(m, dev, Want{Audio: track, Subtitle: -1}, Limits{}), m3); diff != "" {
			t.Errorf("%s: DecideFor with no limits: %s", id, diff)
		}
	}
	old := 0
	for dname, dev := range devices {
		for _, full := range []bool{false, true} {
			d := dev
			if full {
				d = surfaces[0].on(newDevices[dname])
			}
			for _, cont := range oldContainers {
				for _, v := range oldVideos {
					for _, a := range oldAudios {
						var m Media
						if a == "" {
							m = media(cont, v.codec, v.depth)
						} else {
							m = media(cont, v.codec, v.depth, a)
						}
						check(fmt.Sprintf("%s %s/%d %s on %s (all it can say: %v)", cont, v.codec, v.depth, a, dname, full), m, d, -1)
						if !full {
							old++
						}
					}
				}
			}
		}
	}
	if old != 1890 {
		t.Fatalf("%d of M3's combinations walked, want all 1,890", old)
	}

	// Several sound tracks, each asked for in turn, with the main one first,
	// second or not marked; and HEVC under both labels.
	for dname, dev := range devices {
		for _, cont := range oldContainers {
			for _, v := range oldVideos {
				for _, tag := range []string{"", "hev1", "hvc1"} {
					for _, main := range []int{-1, 0, 1} {
						m := media(cont, v.codec, v.depth, "aac", "dts", "opus")
						m.Video[0].Tag = tag
						for i := range m.Audio {
							m.Audio[i].Default = i == main
						}
						for track := -1; track <= 3; track++ {
							check(fmt.Sprintf("%s %s/%d %q, main track %d, asked for %d, on %s", cont, v.codec, v.depth, tag, main, track, dname), m, dev, track)
						}
					}
				}
			}
		}
	}
	for name, dev := range map[string]*Caps{"no report": nil, "an empty report": {}, "chrome": devices["chrome"]} {
		check("a file not inspected, "+name, Media{}, dev, -1)
		check("a known file, "+name, media("mp4", "h264", 8, "aac"), dev, -1)
		check("sound only, "+name, media("mp4", "", 0, "aac"), dev, -1)
	}
	t.Logf("%d answers compared with M3's", n)
}
