package server

import (
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// Play tells the page when a film's picture is HDR and is sent as it is,
// which is when the page offers "Colours look wrong on this device". It
// says so on a PC that cannot turn HDR into normal colour too (the
// viewer's word is still kept there), and never for a film in normal
// colour, a film that is converted anyway, or one that cannot be played.
func TestPlaySaysWhenAnHDRFilmIsSentAsItIs(t *testing.T) {
	e := newTroubleEnv(t)
	hdr := func(item string) (said, there bool, method string) {
		t.Helper()
		var p struct {
			HDR  *bool
			Plan struct{ Method string }
		}
		w := e.pc.do("POST", "/api/items/"+item+"/play", nil)
		if w.Code != 200 {
			t.Fatalf("play: %d %s", w.Code, w.Body)
		}
		decode(t, w, &p)
		return p.HDR != nil && *p.HDR, p.HDR != nil, p.Plan.Method
	}
	// Nothing in this library is HDR yet.
	for name, item := range map[string]string{"a large film in normal colour": e.big, "a small film": e.small, "a film that is converted": e.oldFilm} {
		if said, there, _ := hdr(item); said || !there {
			t.Errorf("%s: Play says hdr=%v (said at all: %v)", name, said, there)
		}
	}
	// One film is HDR (the PQ kind), another HLG, a third Dolby Vision of
	// the kind that has to be converted to be shown right.
	e.must(`UPDATE streams SET color_transfer='smpte2084' WHERE file_id=? AND kind='video'`, e.fileOf(e.big))
	e.must(`UPDATE streams SET color_transfer='arib-std-b67' WHERE file_id=? AND kind='video'`, e.fileOf(e.big2))
	for name, item := range map[string]string{"an HDR film": e.big, "an HLG film": e.big2} {
		if said, _, method := hdr(item); !said || method != "direct" {
			t.Errorf("%s the device plays as it is: Play says hdr=%v for method %s", name, said, method)
		}
	}
	if said, _, _ := hdr(e.big3); said {
		t.Error("a film in normal colour is called HDR once others are")
	}

	// This PC cannot turn HDR into normal colour (see limits), so the
	// viewer's word changes nothing about how the film is sent, and Play
	// goes on saying that it is HDR sent as it is.
	if l := e.s.limits(nil); l.ToneMap {
		t.Skip("this PC's converter now turns HDR into normal colour: the rest of this test is about one that cannot")
	}
	if w := e.pc.do("POST", "/api/device/hdr-wrong", map[string]bool{"on": true}); w.Code != 200 {
		t.Fatalf("saying the colours look wrong: %d %s", w.Code, w.Body)
	}
	if said, _, method := hdr(e.big); !said || method != "direct" {
		t.Errorf("after the viewer's word, on a PC that cannot convert HDR: hdr=%v, method %s", said, method)
	}

	// An HDR film the device cannot play at all is not "sent as it is".
	e.must(`UPDATE streams SET codec='vc1', codec_tag='' WHERE file_id=? AND kind='video'`, e.fileOf(e.big2))
	if said, _, method := hdr(e.big2); said {
		t.Errorf("an HDR film that is not sent as it is (method %s): Play says hdr", method)
	}
}

// The question is put to package playback, so what counts as HDR is decided
// in one place. This holds the server to playback's answer for each kind.
func TestWhatCountsAsHDRIsPlaybacksWord(t *testing.T) {
	caps := &playback.Caps{Containers: map[string]bool{"mp4": true}, Video: map[string]bool{"hevc": true, "hevc10": true, "dovi5": true}, Audio: map[string]bool{"aac": true}, HLS: true}
	for name, tc := range map[string]struct {
		v    playback.Track
		want bool
	}{
		"normal colour":  {playback.Track{Codec: "hevc", BitDepth: 10, Width: 1920, Height: 1080}, false},
		"PQ":             {playback.Track{Codec: "hevc", BitDepth: 10, Width: 1920, Height: 1080, Transfer: playback.PQ}, true},
		"HLG":            {playback.Track{Codec: "hevc", BitDepth: 10, Width: 1920, Height: 1080, Transfer: playback.HLG}, true},
		"Dolby Vision 5": {playback.Track{Codec: "hevc", BitDepth: 10, Width: 1920, Height: 1080, DoVi: 5}, true},
	} {
		m := playback.Media{Probed: true, Container: "mp4", Video: []playback.Track{tc.v}, Audio: []playback.Track{{Codec: "aac", Channels: 2}}}
		want := playback.Want{Audio: -1, Subtitle: -1}
		p := playback.DecideFor(m, caps, want, playback.Limits{})
		if p.Method != playback.Direct {
			t.Fatalf("%s: the device should play this as it is, got %s (%v)", name, p.Method, p.Reasons)
		}
		if got := hdrAsItIs(m, caps, want, playback.Limits{}, p); got != tc.want {
			t.Errorf("%s: hdrAsItIs says %v", name, got)
		}
		// A device that takes no chunks cannot be sent a converted picture,
		// so there is nothing the viewer's word could change.
		none := *caps
		none.HLS = false
		if q := playback.DecideFor(m, &none, want, playback.Limits{}); q.Method == playback.Direct && hdrAsItIs(m, &none, want, playback.Limits{}, q) {
			t.Errorf("%s: called HDR sent as it is on a device that cannot be sent anything else", name)
		}
	}
}
