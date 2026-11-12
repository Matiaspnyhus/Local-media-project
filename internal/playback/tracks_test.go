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
}
