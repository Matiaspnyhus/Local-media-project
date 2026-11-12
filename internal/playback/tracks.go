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

// Memory is what the viewer last chose for a show (or a film), which wins
// over Prefs for the rest of that show. A language alone does not say which
// track it was, so the kind of track is kept too, and the pick itself.
type Memory struct {
	AudioLang string
	SubLang   string
	// SubsOn says full subtitles were showing. Forced ones (only signs and
	// foreign lines) do not count: the app shows those by itself.
	SubsOn bool
	// Commentary says the sound picked was a commentary or description
	// track, which is otherwise never chosen.
	Commentary bool
	// SubKind is the kind of subtitle picked: "full", "described" (with
	// sounds described, for the hard of hearing), "forced" or "off". It is
	// "" on a record that holds only the languages and SubsOn, as an
	// earlier version kept them.
	SubKind string
	// Pick is the very tracks that were picked, nil when not recorded.
	Pick *Pick
}

// Pick names the tracks a viewer picked in one film or episode. Played
// again, that film gets exactly these. In another episode of the show they
// only settle which track to take when several have the remembered
// language and kind (two English sound tracks, say): the one in the same
// place.
type Pick struct {
	Audio    int    // the sound track, counted from 0; -1 for none
	Subtitle string // the subtitle's Key; "" for none
}

// Subtitle is one subtitle a viewer could switch on.
type Subtitle struct {
	Key             string // what the page calls it, such as "e2"; see Pick
	Language        string
	Forced          bool
	HearingImpaired bool
	Default         bool
	Title           string
	// Kind says what the subtitle is made of: plain text, styled text, or
	// pictures. "" counts as text. It settles how the subtitle can be
	// shown (see DecideFor).
	Kind SubtitleKind
}

// Choice is which tracks to start with: indexes into the lists given, -1
// for "none".
type Choice struct {
	Audio    int
	Subtitle int
}

// Choose picks the sound and subtitle tracks to start with.
//
// Sound: what was picked for this show before wins: the same language and
// kind of track, and the same place among several such. Without that, a
// commentary or description track is never picked while there is anything
// else, and among the rest the viewer's language wins, then the track the
// file marks as main.
//
// Subtitles: what was picked before wins here too, including "off", which
// then also means no forced ones. Without that, forced subtitles (for
// signs and lines in another language) are shown unless subtitles are off,
// and full subtitles are shown when the viewer always wants them or, on
// "auto", when the sound is not in the viewer's language.
func Choose(audio []Track, subs []Subtitle, prefs Prefs, mem *Memory) Choice {
	c := Choice{Audio: pickAudio(audio, prefs, mem), Subtitle: -1}
	mode := prefs.Subs
	if mode == "" {
		mode = "auto"
	}
	if mem != nil {
		switch mem.SubKind {
		case "off":
			return c
		case "forced":
			c.Subtitle = rememberedSubtitle(subs, mem, true)
			return c
		}
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
	}
	if full {
		if mem != nil {
			c.Subtitle = rememberedSubtitle(subs, mem, false)
		}
		if c.Subtitle < 0 {
			c.Subtitle = bestSubtitle(subs, want, false)
		}
		if c.Subtitle < 0 && want != "" && (mode == "always" || mem != nil) {
			c.Subtitle = bestSubtitle(subs, "", false) // any language beats none
		}
		if c.Subtitle >= 0 {
			return c
		}
		// No full subtitles here at all: the forced ones, if any, are
		// better than nothing.
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

// Remember turns what a viewer picked into what is kept of it. audioIndex
// counts sound tracks from 0 (-1 for none) and subKey is a subtitle's Key
// ("" for off). ok is false when the file has no such tracks.
func Remember(audio []Track, subs []Subtitle, audioIndex int, subKey string) (m Memory, ok bool) {
	if audioIndex < -1 || audioIndex >= len(audio) {
		return Memory{}, false
	}
	m = Memory{SubKind: "off", Pick: &Pick{Audio: audioIndex, Subtitle: subKey}}
	if audioIndex >= 0 {
		m.AudioLang, m.Commentary = Lang(audio[audioIndex].Language), isDescription(audio[audioIndex])
	}
	if subKey == "" {
		return m, true
	}
	for _, s := range subs {
		if s.Key != subKey {
			continue
		}
		m.SubLang = Lang(s.Language)
		switch {
		case s.Forced:
			m.SubKind = "forced"
		case s.HearingImpaired:
			m.SubKind, m.SubsOn = "described", true
		default:
			m.SubKind, m.SubsOn = "full", true
		}
		return m, true
	}
	return Memory{}, false
}

func isDescription(t Track) bool {
	title := strings.ToLower(t.Title)
	return t.Commentary || t.HearingImpaired || strings.Contains(title, "commentary") || strings.Contains(title, "descri")
}

func pickAudio(audio []Track, prefs Prefs, mem *Memory) int {
	if len(audio) == 0 {
		return -1
	}
	if mem != nil {
		if i := rememberedAudio(audio, mem); i >= 0 {
			return i
		}
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
	if lang := Lang(prefs.AudioLang); lang != "" {
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
		if best >= 0 {
			return best
		}
	}
	for _, i := range plain {
		if audio[i].Default {
			return i
		}
	}
	return plain[0]
}

// rememberedAudio finds the sound track that matches what was picked
// before: the same language and kind (ordinary sound, or commentary). When
// several match, the one in the same place as the pick wins, then the one
// the file marks as main. It returns -1 when nothing matches or nothing
// about the sound was kept.
func rememberedAudio(audio []Track, mem *Memory) int {
	lang, picked := Lang(mem.AudioLang), -1
	if mem.Pick != nil {
		picked = mem.Pick.Audio
	}
	if lang == "" && picked < 0 {
		return -1 // no language was kept, and no track either
	}
	fits := func(i int) bool {
		return i >= 0 && i < len(audio) && Lang(audio[i].Language) == lang && isDescription(audio[i]) == mem.Commentary
	}
	if fits(picked) {
		return picked
	}
	best := -1
	for i := range audio {
		if !fits(i) {
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

// rememberedSubtitle finds the subtitle that matches what was picked
// before: the same language (one without a language matches only another
// without), forced or not as asked. When several match, the same kind
// wins (with sounds described, or plain), then the same place as the pick.
func rememberedSubtitle(subs []Subtitle, mem *Memory, forced bool) int {
	lang := Lang(mem.SubLang)
	best, bestScore := -1, -1
	for i, s := range subs {
		if s.Forced != forced || Lang(s.Language) != lang {
			continue
		}
		score := 0
		if s.HearingImpaired == (mem.SubKind == "described") {
			score += 2
		}
		if mem.Pick != nil && mem.Pick.Subtitle != "" && s.Key == mem.Pick.Subtitle {
			score++
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	return best
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
