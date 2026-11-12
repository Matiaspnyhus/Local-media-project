package playback

import "strings"

// Prefs is what a viewer wants by default.
type Prefs struct {
	AudioLang string `json:"audioLang"` // "" = whatever the file marks as its main sound
	SubLang   string `json:"subLang"`   // "" = same as AudioLang
	// Subs: "auto" shows subtitles when the sound is in another language
	// than the viewer's, and forced ones (signs, foreign dialogue) always;
	// "always" shows them whenever there are any; "off" never.
	Subs string `json:"subs"`
}

// Memory is what the viewer last chose for a show, which wins over Prefs
// for the rest of that show.
type Memory struct {
	AudioLang string
	SubLang   string
	SubsOn    bool
}

// Subtitle is one subtitle a viewer could switch on.
type Subtitle struct {
	Language        string
	Forced          bool
	HearingImpaired bool
	Default         bool
	Title           string
}

// Choice is which tracks to start with: indexes into the lists given, -1
// for "none".
type Choice struct {
	Audio    int
	Subtitle int
}

// Choose picks the sound and subtitle tracks to start with.
//
// Sound: a commentary or description track is never picked while there is
// anything else. Among the rest, the remembered language for this show
// wins, then the viewer's language, then the track the file marks as main.
//
// Subtitles: forced ones (for signs and lines in another language) are
// shown unless subtitles are off. Full subtitles are shown when the viewer
// always wants them, when they were on for this show, or, on "auto", when
// the sound is not in the viewer's language.
func Choose(audio []Track, subs []Subtitle, prefs Prefs, mem *Memory) Choice {
	c := Choice{Audio: pickAudio(audio, prefs, mem), Subtitle: -1}
	mode := prefs.Subs
	if mode == "" {
		mode = "auto"
	}
	if mode == "off" && mem == nil {
		return c
	}
	want := Lang(prefs.SubLang)
	if want == "" {
		want = Lang(prefs.AudioLang)
	}
	audioLang := ""
	if c.Audio >= 0 {
		audioLang = Lang(audio[c.Audio].Language)
	}
	full := mode == "always"
	if mode == "auto" && want != "" && audioLang != "" && audioLang != want {
		full = true
	}
	if mem != nil {
		full = mem.SubsOn
		if mem.SubsOn && mem.SubLang != "" {
			want = Lang(mem.SubLang)
		}
	}
	if full {
		c.Subtitle = bestSubtitle(subs, want, false)
		if c.Subtitle < 0 && mem == nil && want != "" && mode == "always" {
			c.Subtitle = bestSubtitle(subs, "", false) // any language beats none
		}
		return c
	}
	if mode == "off" {
		return c
	}
	// Forced subtitles: in the viewer's language if known, else the sound's.
	forcedLang := want
	if forcedLang == "" {
		forcedLang = audioLang
	}
	c.Subtitle = bestSubtitle(subs, forcedLang, true)
	return c
}

func isDescription(t Track) bool {
	title := strings.ToLower(t.Title)
	return t.Commentary || t.HearingImpaired || strings.Contains(title, "commentary") || strings.Contains(title, "descri")
}

func pickAudio(audio []Track, prefs Prefs, mem *Memory) int {
	if len(audio) == 0 {
		return -1
	}
	var plain []int
	for i, t := range audio {
		if !isDescription(t) {
			plain = append(plain, i)
		}
	}
	if len(plain) == 0 {
		// Only commentary or description tracks: one of them is the sound.
		return BrowserAudio(audio)
	}
	inLang := func(lang string) int {
		if lang = Lang(lang); lang == "" {
			return -1
		}
		best := -1
		for _, i := range plain {
			if Lang(audio[i].Language) != lang {
				continue
			}
			if audio[i].Default {
				return i
			}
			if best < 0 {
				best = i
			}
		}
		return best
	}
	if mem != nil {
		if i := inLang(mem.AudioLang); i >= 0 {
			return i
		}
	}
	if i := inLang(prefs.AudioLang); i >= 0 {
		return i
	}
	for _, i := range plain {
		if audio[i].Default {
			return i
		}
	}
	return plain[0]
}

// bestSubtitle finds a subtitle in lang ("" = any). forced selects forced
// tracks only; otherwise forced tracks are skipped, and one that is not for
// the hard of hearing is preferred.
func bestSubtitle(subs []Subtitle, lang string, forced bool) int {
	best := -1
	for i, s := range subs {
		if s.Forced != forced {
			continue
		}
		if lang != "" && Lang(s.Language) != lang {
			continue
		}
		if forced && lang == "" {
			continue // a forced track in an unknown language helps nobody
		}
		if !s.HearingImpaired {
			return i
		}
		if best < 0 {
			best = i
		}
	}
	return best
}

// threeToTwo maps the three-letter language codes video files use to the
// two-letter ones people and subtitle file names use.
var threeToTwo = map[string]string{
	"eng": "en", "nor": "no", "nob": "no", "nno": "no", "swe": "sv", "dan": "da", "fin": "fi", "isl": "is",
	"deu": "de", "ger": "de", "fra": "fr", "fre": "fr", "spa": "es", "ita": "it", "por": "pt", "nld": "nl", "dut": "nl",
	"jpn": "ja", "kor": "ko", "zho": "zh", "chi": "zh", "rus": "ru", "pol": "pl", "tur": "tr", "ara": "ar", "hin": "hi",
	"ces": "cs", "cze": "cs", "hun": "hu", "ell": "el", "gre": "el", "heb": "he", "tha": "th", "ukr": "uk", "ron": "ro", "rum": "ro",
	"nb": "no", "nn": "no",
}

// Lang reduces a language code to one comparable form: "eng", "en", "EN"
// and "en-US" are all "en". Unknown codes are kept, lower-cased.
func Lang(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	if code == "und" {
		return ""
	}
	if two, ok := threeToTwo[code]; ok {
		return two
	}
	return code
}
