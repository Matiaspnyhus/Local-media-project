package playback

import "testing"

func TestLang(t *testing.T) {
	for in, want := range map[string]string{"eng": "en", "EN": "en", "en-US": "en", "nob": "no", "nor": "no", "nb": "no", "jpn": "ja", "und": "", "": "", "xyz": "xyz", "pt_BR": "pt"} {
		if got := Lang(in); got != want {
			t.Errorf("Lang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChooseSound(t *testing.T) {
	en := Track{Codec: "ac3", Language: "eng", Default: true}
	enCommentary := Track{Codec: "aac", Language: "eng", Title: "Director's Commentary"}
	enDescribed := Track{Codec: "aac", Language: "eng", Title: "English - Audio Description"}
	ja := Track{Codec: "flac", Language: "jpn"}
	no := Track{Codec: "aac", Language: "nor"}
	cases := []struct {
		name  string
		audio []Track
		prefs Prefs
		mem   *Memory
		want  int
	}{
		{"nothing to choose from", nil, Prefs{}, nil, -1},
		{"one track", []Track{en}, Prefs{}, nil, 0},
		{"the file's main track when the viewer has no wish", []Track{ja, en}, Prefs{}, nil, 1},
		{"never the commentary", []Track{{Codec: "aac", Language: "eng", Default: true, Commentary: true}, en}, Prefs{}, nil, 1},
		{"never the commentary, even first and in the right language", []Track{enCommentary, ja}, Prefs{AudioLang: "en"}, nil, 1},
		{"never the audio description", []Track{enDescribed, en}, Prefs{AudioLang: "en"}, nil, 1},
		{"the viewer's language over the file's main track", []Track{en, no}, Prefs{AudioLang: "no"}, nil, 1},
		{"anime with a dub: the viewer wants Japanese", []Track{en, ja}, Prefs{AudioLang: "ja"}, nil, 1},
		{"the viewer's language is not there", []Track{en, ja}, Prefs{AudioLang: "no"}, nil, 0},
		{"what was chosen for this show wins", []Track{en, ja}, Prefs{AudioLang: "en"}, &Memory{AudioLang: "ja"}, 1},
		{"what was chosen is missing from this episode", []Track{en, no}, Prefs{AudioLang: "en"}, &Memory{AudioLang: "ja"}, 0},
		{"only a commentary: it is the sound", []Track{enCommentary}, Prefs{}, nil, 0},
		{"two in the viewer's language: the main one", []Track{{Codec: "aac", Language: "eng"}, en}, Prefs{AudioLang: "en"}, nil, 1},
	}
	for _, c := range cases {
		if got := Choose(c.audio, nil, c.prefs, c.mem).Audio; got != c.want {
			t.Errorf("%s: track %d, want %d", c.name, got, c.want)
		}
	}
}

func TestChooseSubtitles(t *testing.T) {
	en := Track{Codec: "aac", Language: "eng", Default: true}
	ja := Track{Codec: "aac", Language: "jpn", Default: true}
	subs := []Subtitle{
		{Language: "eng", HearingImpaired: true}, // 0
		{Language: "eng"},                        // 1
		{Language: "eng", Forced: true},          // 2
		{Language: "nor"},                        // 3
		{Language: "nor", Forced: true},          // 4
	}
	cases := []struct {
		name  string
		audio Track
		prefs Prefs
		mem   *Memory
		want  int
	}{
		{"off means off", en, Prefs{AudioLang: "en", Subs: "off"}, nil, -1},
		{"sound in the viewer's language: only the forced ones", en, Prefs{AudioLang: "en"}, nil, 2},
		{"sound in another language: full subtitles", ja, Prefs{AudioLang: "en"}, nil, 1},
		{"full subtitles prefer the plain ones over those for the hard of hearing", ja, Prefs{AudioLang: "en", Subs: "always"}, nil, 1},
		{"a separate subtitle language", ja, Prefs{AudioLang: "en", SubLang: "no"}, nil, 3},
		{"always, in Norwegian, with English sound", en, Prefs{AudioLang: "en", SubLang: "no", Subs: "always"}, nil, 3},
		{"forced subtitles in the viewer's subtitle language", en, Prefs{AudioLang: "en", SubLang: "no"}, nil, 3},
		{"no wish at all: forced subtitles in the sound's language", en, Prefs{}, nil, 2},
		{"switched on for this show", en, Prefs{AudioLang: "en"}, &Memory{SubsOn: true, SubLang: "no"}, 3},
		{"switched off for this show, although the sound is foreign", ja, Prefs{AudioLang: "en"}, &Memory{SubsOn: false}, 2},
		{"switched on for this show although the default is off", en, Prefs{AudioLang: "en", Subs: "off"}, &Memory{SubsOn: true, SubLang: "en"}, 1},
		{"only the sound was ever picked: the preferences decide", ja, Prefs{AudioLang: "en"}, &Memory{AudioLang: "ja", SubKind: "untouched"}, 1},
		{"only the sound was ever picked, and the default is off", en, Prefs{AudioLang: "en", Subs: "off"}, &Memory{AudioLang: "en", SubKind: "untouched"}, -1},
	}
	for _, c := range cases {
		if got := Choose([]Track{c.audio}, subs, c.prefs, c.mem).Subtitle; got != c.want {
			t.Errorf("%s: subtitle %d, want %d", c.name, got, c.want)
		}
	}
	// With nothing suitable there is no subtitle, not a wrong one.
	if got := Choose([]Track{ja}, []Subtitle{{Language: "fra"}}, Prefs{AudioLang: "en"}, nil).Subtitle; got != -1 {
		t.Errorf("a French subtitle was shown to an English viewer: %d", got)
	}
	if got := Choose([]Track{ja}, []Subtitle{{Language: "fra"}}, Prefs{AudioLang: "en", Subs: "always"}, nil).Subtitle; got != 0 {
		t.Errorf("'always' with only French available: %d, want the French one", got)
	}
	if got := Choose([]Track{en}, []Subtitle{{Forced: true}}, Prefs{}, nil).Subtitle; got != -1 {
		t.Errorf("a forced subtitle in an unknown language was shown: %d", got)
	}
	// "Always" with only the signs-and-foreign-lines kind in the viewer's
	// language: those are shown, as they would be on "auto".
	if got := Choose([]Track{en}, []Subtitle{{Language: "eng", Forced: true}}, Prefs{AudioLang: "en", Subs: "always"}, nil).Subtitle; got != 0 {
		t.Errorf("'always' with only forced subtitles available: %d, want them", got)
	}
}

// A record that holds only languages and on/off, which is what an earlier
// version kept and what a page from before the update still sends.
func TestChooseFromALanguageOnlyMemory(t *testing.T) {
	ja := Track{Codec: "aac", Language: "jpn", Default: true}
	// The commonest subtitle of all, "Film.srt", has no language. Switched
	// on, it must come back on, whatever language the viewer prefers.
	if got := Choose([]Track{ja}, []Subtitle{{}}, Prefs{AudioLang: "en"}, &Memory{AudioLang: "ja", SubsOn: true}).Subtitle; got != 0 {
		t.Errorf("a subtitle without a language was not switched on again: %d", got)
	}
	if got := Choose([]Track{ja}, []Subtitle{{Language: "eng"}, {}}, Prefs{AudioLang: "en"}, &Memory{AudioLang: "ja", SubsOn: true}).Subtitle; got != 1 {
		t.Errorf("the subtitle without a language was picked, the English one came back: %d", got)
	}
	// Subtitles were on in a language this episode lacks: the viewer's own
	// language, then any, beats none.
	if got := Choose([]Track{ja}, []Subtitle{{Language: "fra"}, {Language: "eng"}}, Prefs{AudioLang: "en"}, &Memory{SubsOn: true, SubLang: "no"}).Subtitle; got != 1 {
		t.Errorf("subtitles on, remembered language missing: %d, want the viewer's language", got)
	}
	if got := Choose([]Track{ja}, []Subtitle{{Language: "fra"}}, Prefs{AudioLang: "en"}, &Memory{SubsOn: true, SubLang: "no"}).Subtitle; got != 0 {
		t.Errorf("subtitles on, only French here: %d, want it", got)
	}
}

// What a viewer picks is kept as the pick itself plus its language and
// kind. Playing the same film again must give the very same tracks, and
// the next episode of a show the nearest thing to them.
func TestThePickItselfIsRemembered(t *testing.T) {
	film := []Track{
		{Codec: "dts", Language: "eng", Default: true, Title: "Surround"}, // 0
		{Codec: "aac", Language: "eng", Title: "Stereo"},                  // 1
		{Codec: "aac", Language: "eng", Title: "Director's Commentary"},   // 2
		{Codec: "aac"}, // 3: no language
		{Codec: "aac"}, // 4: no language either
	}
	subs := []Subtitle{
		{Key: "e0", Language: "eng"},                        // 0
		{Key: "e1", Language: "eng", Forced: true},          // 1
		{Key: "e2", Language: "eng", HearingImpaired: true}, // 2
		{Key: "e3", Language: "eng", Title: "Signs"},        // 3: a second full one
		{Key: "x1"},               // 4: "Film.srt", no language
		{Key: "e5", Forced: true}, // 5: forced, no language
	}
	prefs := Prefs{AudioLang: "en"}
	again := func(audioIndex int, subKey string) Choice {
		t.Helper()
		mem, ok := Remember(film, subs, audioIndex, subKey)
		if !ok {
			t.Fatalf("Remember(%d, %q) refused a pick that exists", audioIndex, subKey)
		}
		return Choose(film, subs, prefs, &mem)
	}
	// With nothing remembered: the main sound, and the forced subtitles.
	if c := Choose(film, subs, prefs, nil); c != (Choice{0, 1}) {
		t.Fatalf("with nothing remembered: %+v", c)
	}
	for _, c := range []struct {
		name     string
		audio    int
		subKey   string
		wantSubs int
	}{
		{"the second of two tracks in one language", 1, "e1", 1},
		{"the commentary", 2, "e1", 1},
		{"a track with no language", 3, "e1", 1},
		{"the second track with no language", 4, "e1", 1},
		{"subtitles switched off: off, the forced ones too", 0, "", -1},
		{"the forced subtitles, picked by hand", 0, "e1", 1},
		{"the full subtitles", 0, "e0", 0},
		{"the ones with sounds described", 0, "e2", 2},
		{"the second full ones", 0, "e3", 3},
		{"a subtitle file with no language", 0, "x1", 4},
		{"forced subtitles with no language", 0, "e5", 5},
	} {
		if got := again(c.audio, c.subKey); got != (Choice{c.audio, c.wantSubs}) {
			t.Errorf("%s: picked sound %d and subtitle %q, the film came back with %+v", c.name, c.audio, c.subKey, got)
		}
	}
	// A pick that is not in the file is refused, not stored.
	for _, bad := range []struct {
		audio int
		key   string
	}{{5, ""}, {-2, ""}, {0, "e9"}} {
		if _, ok := Remember(film, subs, bad.audio, bad.key); ok {
			t.Errorf("Remember(%d, %q) accepted a track the file does not have", bad.audio, bad.key)
		}
	}
	// What is kept of the forced subtitles is not "subtitles on": the app
	// showed those by itself.
	if mem, _ := Remember(film, subs, 0, "e1"); mem.SubsOn || mem.SubKind != "forced" || mem.SubLang != "en" {
		t.Errorf("forced subtitles were kept as %+v", mem)
	}

	// The next episode has its tracks in the same places: same choice.
	next := func(audioIndex int, subKey string, audio []Track, subs2 []Subtitle) Choice {
		t.Helper()
		mem, _ := Remember(film, subs, audioIndex, subKey)
		return Choose(audio, subs2, prefs, &mem)
	}
	if c := next(1, "e3", film, subs); c != (Choice{1, 3}) {
		t.Errorf("next episode, same layout: %+v, want the second English sound and the second full subtitles", c)
	}
	if c := next(2, "e2", film, subs); c != (Choice{2, 2}) {
		t.Errorf("next episode after picking the commentary and described subtitles: %+v", c)
	}
	// The next episode is laid out differently: language and kind decide.
	other := []Track{{Codec: "aac", Language: "jpn", Default: true}, {Codec: "aac", Language: "eng", Title: "Commentary"}, {Codec: "ac3", Language: "eng"}}
	otherSubs := []Subtitle{{Key: "e0", Language: "nor"}, {Key: "e1", Language: "eng", HearingImpaired: true}, {Key: "e2", Language: "eng", Forced: true}, {Key: "e3", Language: "eng"}}
	if c := next(1, "e0", other, otherSubs); c != (Choice{2, 3}) {
		t.Errorf("next episode, other layout, English sound and full subtitles were picked: %+v", c)
	}
	if c := next(2, "e2", other, otherSubs); c != (Choice{1, 1}) {
		t.Errorf("next episode, other layout, commentary and described subtitles were picked: %+v", c)
	}
	if c := next(0, "e1", other, otherSubs); c != (Choice{2, 2}) {
		t.Errorf("next episode, other layout, forced subtitles were picked: %+v", c)
	}
	if c := next(0, "", other, otherSubs); c != (Choice{2, -1}) {
		t.Errorf("next episode, other layout, subtitles were off: %+v", c)
	}
	// An episode without a commentary plays its ordinary sound.
	if c := next(2, "", []Track{{Codec: "aac", Language: "eng", Default: true}}, nil); c.Audio != 0 {
		t.Errorf("an episode without a commentary: %+v", c)
	}
	// Forced subtitles were picked and this episode has none: nothing, not
	// the full ones.
	if c := next(0, "e1", film, []Subtitle{{Key: "e0", Language: "eng"}}); c.Subtitle != -1 {
		t.Errorf("forced subtitles were picked, this episode has only full ones: %+v", c)
	}
}

// viewer watches a show the way the page lets them: an episode starts with
// what Choose says, and every pick is sent with the sound track and the
// subtitle showing at that moment, whichever of the two was changed.
type viewer struct {
	t     *testing.T
	prefs Prefs
	mem   *Memory // what is kept for the show

	audio []Track
	subs  []Subtitle
	sound int    // the sound track playing
	sub   string // the subtitle showing, "" for none
}

func (v *viewer) open(audio []Track, subs []Subtitle) {
	v.audio, v.subs = audio, subs
	c := Choose(audio, subs, v.prefs, v.mem)
	v.sound, v.sub = c.Audio, ""
	if c.Subtitle >= 0 {
		v.sub = subs[c.Subtitle].Key
	}
}

func (v *viewer) send() {
	v.t.Helper()
	pick, ok := Remember(v.audio, v.subs, v.sound, v.sub)
	if !ok {
		v.t.Fatalf("Remember(%d, %q) refused a pick that exists", v.sound, v.sub)
	}
	kept := AfterPick(v.audio, v.subs, v.prefs, v.mem, pick)
	v.mem = &kept
}

func (v *viewer) pickSound(i int)       { v.t.Helper(); v.sound = i; v.send() }
func (v *viewer) pickSubtitle(k string) { v.t.Helper(); v.sub = k; v.send() }

func (v *viewer) expect(when string, sound int, sub string) {
	v.t.Helper()
	if v.sound != sound || v.sub != sub {
		v.t.Errorf("%s: sound %d and subtitle %q, want sound %d and subtitle %q", when, v.sound, v.sub, sound, sub)
	}
}

// Changing the sound is not a wish about subtitles: what the app had
// showing at that moment, or nothing, must not become a rule for the show.
// Changing the subtitles is such a wish, and stays one.
func TestAPickOfSoundAloneLeavesTheSubtitlesToThePreferences(t *testing.T) {
	en51 := Track{Codec: "ac3", Language: "eng", Default: true, Title: "5.1"}
	enStereo := Track{Codec: "aac", Language: "eng", Title: "Stereo"}
	ja := Track{Codec: "aac", Language: "jpn"}
	full := Subtitle{Key: "e0", Language: "eng"}
	forced := Subtitle{Key: "e1", Language: "eng", Forced: true}
	twoEnglish := []Track{en51, enStereo}

	// An episode without subtitles; the next has them for its foreign lines.
	v := &viewer{t: t, prefs: Prefs{AudioLang: "en"}}
	v.open(twoEnglish, nil)
	v.pickSound(1)
	v.open(twoEnglish, []Subtitle{full, forced})
	v.expect("the sound was changed in an episode without subtitles; the next one", 1, "e1")
	// Switched off by hand, they stay off, and a change of sound afterwards
	// does not bring them back.
	v.pickSubtitle("")
	v.pickSound(0)
	v.open(twoEnglish, []Subtitle{full, forced})
	v.expect("subtitles were switched off by hand, then the sound changed; the next episode", 0, "")

	// A viewer who always wants subtitles, an episode with only the
	// foreign-lines kind (shown for want of better), then one with both.
	v = &viewer{t: t, prefs: Prefs{AudioLang: "en", Subs: "always"}}
	v.open(twoEnglish, []Subtitle{{Key: "e0", Language: "eng", Forced: true}})
	v.expect("always, only forced subtitles here", 0, "e0")
	v.pickSound(1)
	v.open(twoEnglish, []Subtitle{full, forced})
	v.expect("always, the sound was changed while forced subtitles were the stand-in; the next episode", 1, "e0")
	// Picked by hand, the foreign-lines kind is a wish like any other.
	v.pickSubtitle("e1")
	v.open(twoEnglish, []Subtitle{full, forced})
	v.expect("always, forced subtitles picked by hand; the next episode", 1, "e1")

	// To the Japanese sound and back. The page goes on showing what the
	// app chose for the English sound, which is not what it would choose
	// for the Japanese one; neither change is a wish about subtitles.
	v = &viewer{t: t, prefs: Prefs{AudioLang: "en"}}
	dub := []Track{en51, ja}
	v.open(dub, []Subtitle{full})
	v.expect("an English dub, nothing picked yet", 0, "")
	v.pickSound(1)
	v.pickSound(0)
	if v.mem.SubKind != "untouched" {
		t.Errorf("to the Japanese sound and back: subtitles were kept as %q although they were never touched", v.mem.SubKind)
	}
	v.open(dub, []Subtitle{full, forced})
	v.expect("to the Japanese sound and back; the next episode", 0, "e1")
	// Left on the Japanese sound, the app gives it subtitles next time.
	v.pickSound(1)
	v.open(dub, []Subtitle{full, forced})
	v.expect("left on the Japanese sound; the next episode", 1, "e0")

	// Subtitles picked first, then the sound changed: the pick is kept,
	// with the very track it was.
	v = &viewer{t: t, prefs: Prefs{AudioLang: "en"}}
	two := []Subtitle{full, forced, {Key: "e2", Language: "eng", Title: "Songs too"}}
	v.open(twoEnglish, two)
	v.pickSubtitle("e2")
	v.pickSound(1)
	v.open(twoEnglish, two)
	v.expect("full subtitles picked, then the sound changed; the same film again", 1, "e2")

	// Both changed in one message (the page never does, another app may).
	v = &viewer{t: t, prefs: Prefs{AudioLang: "en"}}
	v.open(twoEnglish, []Subtitle{full, forced})
	v.sound, v.sub = 1, ""
	v.send()
	v.open(twoEnglish, []Subtitle{full, forced})
	v.expect("stereo and no subtitles sent together; the same film again", 1, "")

	// What an earlier version kept (languages, and subtitles "not on") is
	// left as it was by a change of sound.
	v = &viewer{t: t, prefs: Prefs{AudioLang: "en"}, mem: &Memory{AudioLang: "en"}}
	v.open(twoEnglish, nil)
	v.pickSound(1)
	if v.mem.SubKind != "" || v.mem.Pick == nil || v.mem.Pick.Audio != 1 {
		t.Errorf("an earlier record after a change of sound: %+v, pick %+v", *v.mem, v.mem.Pick)
	}
	v.open(twoEnglish, []Subtitle{full, forced})
	v.expect("an earlier record, the sound changed in an episode without subtitles; the next one", 1, "e1")
}
