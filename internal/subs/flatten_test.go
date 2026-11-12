package subs

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

// A script with nothing but lines, for tests that are about the lines.
// (Scripts turn up like this: the headings are not needed to read them.)
func lines(l ...string) []byte {
	return []byte(strings.Join(l, "\n") + "\n")
}

// said is a line of speech from 1 to 2 seconds with the given text.
func said(text string) string {
	return "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,," + text
}

func cue(start, end int64, text string, role Role) Cue {
	return Cue{StartMs: start, EndMs: end, Text: text, Role: role}
}

// The script the test films carry: the spoken line keeps its words and its
// comma and loses its instruction; the sign is a sign; the drawing and the
// maker's note are gone.
func TestFlattenKeepsTheWordsAndNothingElse(t *testing.T) {
	want := []Cue{cue(1000, 2500, "Hello, world", Speech), cue(3000, 4000, "Bakery", Sign)}
	if got := Flatten([]byte(script)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	wantVTT := "WEBVTT\n\n00:00:01.000 --> 00:00:02.500\nHello, world\n\n00:00:03.000 --> 00:00:04.000\nBakery\n"
	if got := string(WebVTT(Flatten([]byte(script)))); got != wantVTT {
		t.Errorf("as WebVTT:\n%q\nwant\n%q", got, wantVTT)
	}
}

func TestFlattenTakesOutEveryInstruction(t *testing.T) {
	for text, want := range map[string]string{
		`{\an8}At the top`:                                      "At the top",
		`{\pos(320,40)}Pinned`:                                  "Pinned",
		`{\fad(200,200)}Fading`:                                 "Fading",
		`{\b1}Bold{\b0} and {\c&HFF00FF&}coloured`:              "Bold and coloured",
		`{\t(0,500,\fs40\frz30)}Growing`:                        "Growing",
		`{\an8\bord2\blur0.6\fscx110}Several {\i1}at{\i0} once`: "Several at once",
		`{\clip(m 0 0 l 100 0 100 100)}Cut to a shape`:          "Cut to a shape",
		`{a note by the maker}Shown`:                            "Shown",
		`{\k20}Ka{\k35}ra{\kf40}o{\ko10}ke`:                     "Karaoke",
		`{\pbo10}Lowered, not drawn`:                            "Lowered, not drawn",
		// Line breaks, of both sorts, and the space lines are not broken at.
		`One\NTwo`:         "One\nTwo",
		`One\nTwo`:         "One\nTwo",
		`One\N\N  \NTwo\N`: "One\nTwo",
		`{\an8}One \N Two`: "One\nTwo",
		`Half\hspace`:      "Half space",
		// Braces that are not instructions.
		`Look {here`:       "Look {here",
		`Curly \{braces\}`: "Curly {braces}",
		`A } alone`:        "A } alone",
		// Commas are part of the text, however many.
		`Yes, no, maybe, {\i1}so{\i0}`: "Yes, no, maybe, so",
		"Tab\there":                    "Tab here",
	} {
		got := Flatten(lines(said(text)))
		if len(got) != 1 || got[0].Text != want || got[0].StartMs != 1000 || got[0].EndMs != 2000 {
			t.Errorf("%q became %+v, want %q", text, got, want)
		}
	}
}

// ffmpeg's own conversion shows a drawing's pen movements as a caption.
// Here a drawing is nothing: whatever stands between the instruction that
// begins one and the one that ends it is left out.
func TestFlattenLeavesDrawingsOut(t *testing.T) {
	for text, want := range map[string]string{
		`{\p1}m 0 0 l 800 0 800 450 0 450{\p0}`:                              "",
		`{\an7\pos(0,0)\bord0\p1\c&H000000&}m 0 0 l 1920 0 1920 1080 0 1080`: "",
		`{\p4}m 0 0 b 10 10 20 20 30 30{\p0}`:                                "",
		`{\p1}m 0 0 l 5 5\N l 9 9{\p0}`:                                      "",
		`{\p1}m 0 0 l 5 5{\p0}Label`:                                         "Label",
		`Before {\p1}m 0 0 l 5 5{\p0} after`:                                 "Before after",
		`{\p1}m 0 0 l 5 5{\p0}One{\p1}m 1 1{\p0}\NTwo`:                       "One\nTwo",
		`{\p0}Never a drawing`:                                               "Never a drawing",
	} {
		got := Flatten(lines(said(text)))
		switch {
		case want == "" && len(got) != 0:
			t.Errorf("%q should leave nothing, left %+v", text, got)
		case want != "" && (len(got) != 1 || got[0].Text != want):
			t.Errorf("%q became %+v, want %q", text, got, want)
		}
		if vtt := string(WebVTT(got)); strings.Contains(vtt, "m 0 0") || strings.Contains(vtt, "l 5 5") {
			t.Errorf("%q: pen movements in the captions:\n%s", text, vtt)
		}
	}
}

// A sign that follows the film's picture is written once for every frame,
// each a little further along. It is one caption, not fifty; so is a line
// written twice at once to give it a border. The same words again after a
// pause are said again.
func TestFlattenShowsRepeatedWordsOnce(t *testing.T) {
	var moving []string
	for i := 0; i < 50; i++ {
		// 25 frames a second: each line lasts one frame.
		from, to := 10000+i*40, 10000+(i+1)*40
		moving = append(moving, fmt.Sprintf(`Dialogue: 0,0:00:%02d.%02d,0:00:%02d.%02d,Default,,0,0,0,,{\pos(%d,200)\frz%d}Shop`,
			from/1000, from%1000/10, to/1000, to%1000/10, 100+3*i, i))
	}
	got := Flatten(lines(moving...))
	if want := []Cue{cue(10000, 12000, "Shop", Sign)}; !reflect.DeepEqual(got, want) {
		t.Errorf("a sign written for each of 50 frames: %d captions %+v", len(got), got)
	}
	// With a frame missing here and there (rounding leaves such gaps), and
	// with its frames out of order in the script.
	gappy := append(append([]string{}, moving[30:]...), moving[:29]...)
	if got := Flatten(lines(gappy...)); !reflect.DeepEqual(got, []Cue{cue(10000, 12000, "Shop", Sign)}) {
		t.Errorf("the same with a frame missing and out of order: %+v", got)
	}
	// Two layers of one line, and a song line shown while its translation
	// is: the first is one caption, the second stays two.
	got = Flatten(lines(
		`Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,{\bord4}Outlined`,
		`Dialogue: 1,0:00:01.00,0:00:03.00,Default,,0,0,0,,{\bord0}Outlined`,
		`Dialogue: 0,0:00:01.00,0:00:03.00,Default,,0,0,0,,Not the same words`,
	))
	if want := []Cue{cue(1000, 3000, "Outlined", Speech), cue(1000, 3000, "Not the same words", Speech)}; !reflect.DeepEqual(got, want) {
		t.Errorf("a line in two layers: %+v", got)
	}
	// Said twice, half a second apart: two captions.
	got = Flatten(lines(
		`Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,No!`,
		`Dialogue: 0,0:00:02.50,0:00:03.00,Default,,0,0,0,,No!`,
		`Dialogue: 0,0:00:03.05,0:00:04.00,Default,,0,0,0,,No!`,
	))
	if want := []Cue{cue(1000, 2000, "No!", Speech), cue(2500, 4000, "No!", Speech)}; !reflect.DeepEqual(got, want) {
		t.Errorf("the same words after a pause: %+v", got)
	}
	// Lines that overlap only in part are joined over all of their time.
	got = Flatten(lines(
		`Dialogue: 0,0:00:05.00,0:00:07.00,Default,,0,0,0,,Long`,
		`Dialogue: 0,0:00:01.00,0:00:06.00,Default,,0,0,0,,Long`,
		`Dialogue: 0,0:00:02.00,0:00:03.00,Default,,0,0,0,,Long`,
	))
	if want := []Cue{cue(1000, 7000, "Long", Speech)}; !reflect.DeepEqual(got, want) {
		t.Errorf("overlapping lines with the same words: %+v", got)
	}
}

// A heavily styled script is long: here, two hundred signs that each move
// for a hundred frames, among two thousand spoken lines. Every sign is one
// caption and every spoken line is kept.
func TestFlattenOfALongScript(t *testing.T) {
	var all []string
	for sign := 0; sign < 200; sign++ {
		for frame := 0; frame < 100; frame++ {
			from := sign*10000 + frame*40
			all = append(all, fmt.Sprintf(`Dialogue: 0,%d:%02d:%02d.%02d,%d:%02d:%02d.%02d,Signs,,0,0,0,,{\pos(%d,%d)}Sign number %d`,
				from/3600000, from/60000%60, from/1000%60, from%1000/10, (from+40)/3600000, (from+40)/60000%60, (from+40)/1000%60, (from+40)%1000/10, frame, sign, sign))
		}
	}
	for line := 0; line < 2000; line++ {
		from := line * 1000
		all = append(all, fmt.Sprintf(`Dialogue: 0,%d:%02d:%02d.00,%d:%02d:%02d.90,Default,,0,0,0,,Spoken line %d`,
			from/3600000, from/60000%60, from/1000%60, from/3600000, from/60000%60, from/1000%60, line))
	}
	got := Flatten(lines(all...))
	signs, speech := 0, 0
	for i, c := range got {
		switch {
		case c.Role == Sign && c.EndMs-c.StartMs == 4000:
			signs++
		case c.Role == Speech && c.EndMs-c.StartMs == 900:
			speech++
		default:
			t.Fatalf("caption %d: %+v", i, c)
		}
		if i > 0 && c.StartMs < got[i-1].StartMs {
			t.Fatalf("caption %d begins at %d, after one that begins at %d", i, c.StartMs, got[i-1].StartMs)
		}
	}
	if signs != 200 || speech != 2000 {
		t.Errorf("%d signs and %d spoken lines from 200 and 2000", signs, speech)
	}
}

// Captions come out in the order they appear, whatever order the script
// has them in. Of those that appear together, speech is first (a player
// puts the first lowest), then signs, then songs, and within a sort the
// script's order stands.
func TestFlattenPutsOverlappingLinesInOrder(t *testing.T) {
	got := Flatten(lines(
		`Dialogue: 0,0:00:05.00,0:00:06.00,Default,,0,0,0,,Last`,
		`Dialogue: 0,0:00:01.00,0:00:04.00,Song,,0,0,0,,La la`,
		`Dialogue: 0,0:00:01.00,0:00:04.00,Sign,,0,0,0,,Exit`,
		`Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Second speaker`,
		`Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,A first speaker`,
		`Dialogue: 0,0:00:00.50,0:00:09.00,Sign,,0,0,0,,Already there`,
		`Dialogue: 0,0:00:01.50,0:00:02.00,Default,,0,0,0,,Interrupting`,
	))
	want := []Cue{
		cue(500, 9000, "Already there", Sign),
		cue(1000, 2000, "Second speaker", Speech),
		cue(1000, 2000, "A first speaker", Speech),
		cue(1000, 4000, "Exit", Sign),
		cue(1000, 4000, "La la", Song),
		cue(1500, 2000, "Interrupting", Speech),
		cue(5000, 6000, "Last", Speech),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order:\n got %+v\nwant %+v", got, want)
	}
}

// What a line is for is judged from its style's name and from the line
// itself. The names are the makers' own; these are the usual ones.
func TestFlattenTellsSignsAndSongsFromSpeech(t *testing.T) {
	for style, want := range map[string]Role{
		"Default": Speech, "Main": Speech, "Italics": Speech, "Alt": Speech, "Top": Speech, "Flashback": Speech, "Subtitle": Speech, "Overlap": Speech,
		// Characters, whose names happen to hold a short word.
		"Ed": Speech, "Edward": Speech, "EDWARD": Speech, "Opera": Speech, "Ted": Speech, "Pop": Speech, "Editor": Speech,
		"Sign": Sign, "Signs": Sign, "sign-top": Sign, "TS": Sign, "Title": Sign, "EpTitle": Sign, "TL Notes": Sign, "Credits": Sign, "Typesetting": Sign,
		"Song Title": Sign, "ED Credits": Sign, "*Sign": Sign,
		"OP": Song, "ED": Song, "OP2": Song, "ED-English": Song, "OP_Romaji": Song, "OPRomaji": Song, "EDEnglish": Song, "Song": Song,
		"Karaoke": Song, "Lyrics": Song, "Insert Song": Song, "Opening": Song, "kfx": Song, "Kara": Song, "ED Kanji": Song,
	} {
		got := Flatten(lines("Dialogue: 0,0:00:01.00,0:00:02.00," + style + ",,0,0,0,,Words"))
		if len(got) != 1 || got[0].Role != want {
			t.Errorf("a line in the style %q is taken for %v, want %v", style, got, want)
		}
	}
	// A character called Song or Ed speaking is speech: the speaker's name
	// is not gone by.
	for _, speaker := range []string{"Song", "Ed", "Sign", "OP"} {
		if got := Flatten(lines("Dialogue: 0,0:00:01.00,0:00:02.00,Default," + speaker + ",0,0,0,,Words")); len(got) != 1 || got[0].Role != Speech {
			t.Errorf("a line spoken by %q is taken for %v", speaker, got)
		}
	}
	// From the line itself, in a style that says nothing.
	for text, want := range map[string]Role{
		`{\pos(100,100)}Pinned to a place`:      Sign,
		`{\move(0,0,100,100)}Moving`:            Sign,
		`{\an8}Only moved to the top`:           Speech,
		`{\k20}With {\k30}karaoke {\k10}timing`: Song,
		`{\pos(10,10)\k20}Pinned {\k30}karaoke`: Song,
		`{\kf50}Sweeping`:                       Song,
		`{\K50}Old sweeping`:                    Song,
		`{\ko50}Outlined`:                       Song,
	} {
		if got := Flatten(lines(said(text))); len(got) != 1 || got[0].Role != want {
			t.Errorf("%q is taken for %v, want %v", text, got, want)
		}
	}
	// And from the effect field, where a karaoke program leaves its mark.
	for effect, want := range map[string]Role{"fx": Song, "Karaoke": Song, "": Speech, "Scroll up;10;20;30": Speech} {
		if got := Flatten(lines("Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0," + effect + ",Words")); len(got) != 1 || got[0].Role != want {
			t.Errorf("a line with effect %q is taken for %v, want %v", effect, got, want)
		}
	}
	if Speech.String() != "speech" || Sign.String() != "sign" || Song.String() != "song" {
		t.Error("the roles' names")
	}
}

// What real files hold.
func TestFlattenCopesWithWhatFilesHold(t *testing.T) {
	want := []Cue{cue(1000, 2500, "Hello, world", Speech), cue(3000, 4000, "Bakery", Sign)}
	crlf := strings.ReplaceAll(script, "\n", "\r\n")
	utf16le := func(s string, big bool) []byte {
		out := []byte{0xFF, 0xFE}
		if big {
			out = []byte{0xFE, 0xFF}
		}
		for _, u := range utf16.Encode([]rune(s)) {
			if big {
				out = append(out, byte(u>>8), byte(u))
			} else {
				out = append(out, byte(u), byte(u>>8))
			}
		}
		return out
	}
	for name, raw := range map[string][]byte{
		"a marker at the start (UTF-8)":  append([]byte{0xEF, 0xBB, 0xBF}, script...),
		"Windows line ends":              []byte(crlf),
		"old Mac line ends":              []byte(strings.ReplaceAll(script, "\n", "\r")),
		"a marker and Windows line ends": append([]byte{0xEF, 0xBB, 0xBF}, crlf...),
		"UTF-16, little end first":       utf16le(crlf, false),
		"UTF-16, big end first":          utf16le(script, true),
		"no final line end":              []byte(strings.TrimRight(script, "\n")),
		"indented lines":                 []byte(strings.ReplaceAll(script, "\nDialogue:", "\n  Dialogue:")),
	} {
		if got := Flatten(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
	}

	// Sections missing: no headings at all, no list of fields, lines only.
	// And in whatever case the words are written.
	got := Flatten(lines(
		"; made by hand",
		`dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,No headings, {\i1}no{\i0} list of fields`,
		`DIALOGUE:0,0:00:03.00,0:00:04.00,Default,,0,0,0,,Close up`,
		`Comment: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,not shown`,
	))
	if want := []Cue{cue(1000, 2000, "No headings, no list of fields", Speech), cue(3000, 4000, "Close up", Speech)}; !reflect.DeepEqual(got, want) {
		t.Errorf("without headings: %+v", got)
	}

	// A script that lists its fields in an order of its own: the text is
	// what is left after the others, commas and all.
	got = Flatten(lines(
		"[Events]",
		"Format: Start, End, Style, Text",
		`Dialogue: 0:00:01.00,0:00:02.00,Sign,Short, but whole, {\b1}line`,
		"Format: Name, Fontname, Fontsize", // a styles line gone astray changes nothing
		`Dialogue: 0:00:03.00,0:00:04.00,Default,Still read`,
	))
	if want := []Cue{cue(1000, 2000, "Short, but whole, line", Sign), cue(3000, 4000, "Still read", Speech)}; !reflect.DeepEqual(got, want) {
		t.Errorf("with its own order of fields: %+v", got)
	}

	// Times past an hour, and far past it.
	got = Flatten(lines(
		`Dialogue: 0,1:02:03.45,1:02:05.00,Default,,0,0,0,,An hour in`,
		`Dialogue: 0,12:00:00.00,12:00:01.50,Default,,0,0,0,,Half a day in`,
		`Dialogue: 0,0:59:59.99,1:00:00.01,Default,,0,0,0,,Across the hour`,
	))
	want = []Cue{cue(3599990, 3600010, "Across the hour", Speech), cue(3723450, 3725000, "An hour in", Speech), cue(43200000, 43201500, "Half a day in", Speech)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("times past an hour: %+v", got)
	}
	if vtt := string(WebVTT(got)); !strings.Contains(vtt, "00:59:59.990 --> 01:00:00.010\n") || !strings.Contains(vtt, "01:02:03.450 --> 01:02:05.000\n") || !strings.Contains(vtt, "12:00:00.000 --> 12:00:01.500\n") {
		t.Errorf("times past an hour as WebVTT:\n%s", vtt)
	}

	// The old form (SSA, "v4"): other headings, a first field called
	// Marked, and a star before the style's name.
	got = Flatten(lines(
		"[Script Info]",
		"; This is a Sub Station Alpha v4 script.",
		"ScriptType: v4.00",
		"",
		"[V4 Styles]",
		"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, TertiaryColour, BackColour, Bold, Italic, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, AlphaLevel, Encoding",
		"Style: Default,Arial,20,65535,65535,65535,-2147483640,-1,0,1,3,0,2,30,30,30,0,0",
		"",
		"[Events]",
		"Format: Marked, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text",
		`Dialogue: Marked=0,0:00:01.00,0:00:02.00,*Default,NTP,0000,0000,0000,,An old line, {\a6}with an old instruction`,
		`Dialogue: Marked=0,0:00:03.00,0:00:04.00,*Signs,NTP,0000,0000,0000,,Old sign`,
	))
	if want := []Cue{cue(1000, 2000, "An old line, with an old instruction", Speech), cue(3000, 4000, "Old sign", Sign)}; !reflect.DeepEqual(got, want) {
		t.Errorf("the old form: %+v", got)
	}

	// Text in the old Western European encoding, which is not valid as
	// what is used today: "café", "Ærlig talt", "naïve – ok".
	got = Flatten(lines(
		"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,caf\xE9",
		"Dialogue: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,\xC6rlig talt",
		"Dialogue: 0,0:00:05.00,0:00:06.00,Default,,0,0,0,,na\xEFve \x96 ok",
	))
	if want := []Cue{cue(1000, 2000, "café", Speech), cue(3000, 4000, "Ærlig talt", Speech), cue(5000, 6000, "naïve – ok", Speech)}; !reflect.DeepEqual(got, want) {
		t.Errorf("the old encoding: %+v", got)
	}
	// And the one used today is left alone.
	if got := Flatten(lines(said("Blåbærsyltetøy, 日本語"))); len(got) != 1 || got[0].Text != "Blåbærsyltetøy, 日本語" {
		t.Errorf("today's encoding: %+v", got)
	}

	// Lines that cannot be read are passed over, and the rest is read.
	got = Flatten(lines(
		`Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0`, // no text field
		`Dialogue: 0,soon,later,Default,,0,0,0,,No times`,
		`Dialogue: 0,0:00:05.00,0:00:04.00,Default,,0,0,0,,Ends before it begins`,
		`Dialogue: 0,0:00:05.00,0:00:05.00,Default,,0,0,0,,No time at all`,
		`Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\an8}`, // nothing to show
		`Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,  \N `,
		`Dialogue`,
		`:`,
		`[Fonts]`,
		`fontname: arial_0.ttf`,
		`Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,inside a font, by chance`,
		`[Events]`,
		`Dialogue: 0,0:00:07.00,0:00:08.00,Default,,0,0,0,,Still here`,
	))
	if want := []Cue{cue(7000, 8000, "Still here", Speech)}; !reflect.DeepEqual(got, want) {
		t.Errorf("after lines that cannot be read: %+v", got)
	}
	if got := Flatten(nil); len(got) != 0 {
		t.Errorf("nothing at all: %+v", got)
	}
	if got := string(WebVTT(nil)); got != "WEBVTT\n" {
		t.Errorf("no captions as WebVTT: %q", got)
	}
}

// WebVTT has instructions of its own, begun by "<" and "&", and an arrow
// that separates a caption's times. None may come out of a caption's
// words.
func TestWebVTTIsSafeForAnyWords(t *testing.T) {
	got := string(WebVTT([]Cue{
		cue(1000, 2000, "A < B & C > D", Speech),
		cue(2000, 3000, "00:00:01.000 --> 00:00:09.000", Speech),
		cue(3000, 4000, "<v Mallory>voice\n\n\n  second line  ", Speech),
		cue(-500, 700, "Began before zero", Speech),
		cue(-900, -100, "Over before zero", Speech),
		cue(5000, 5000, "No time", Speech),
		cue(6000, 7000, " \n ", Speech),
	}))
	want := "WEBVTT\n" +
		"\n00:00:01.000 --> 00:00:02.000\nA &lt; B &amp; C &gt; D\n" +
		"\n00:00:02.000 --> 00:00:03.000\n00:00:01.000 --&gt; 00:00:09.000\n" +
		"\n00:00:03.000 --> 00:00:04.000\n&lt;v Mallory&gt;voice\nsecond line\n" +
		"\n00:00:00.000 --> 00:00:00.700\nBegan before zero\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// The track as each converter writes it out of a film (one ends its lines
// the Windows way, the other does not) flattens to the same captions. And
// what this is for: ffmpeg's own conversion of the same file.
func TestFlattenOfAnExtractedTrack(t *testing.T) {
	tl := tools(t)
	dir, _, _ := extracted(t, tl, madeFilm(t, tl, "five"))
	raw, err := os.ReadFile(filepath.Join(dir, "s0.ass"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Cue{cue(1000, 2500, "Hello, world", Speech), cue(3000, 4000, "Bakery", Sign)}
	if got := Flatten(raw); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	theirs, err := exec.Command(tl.FFmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-i", filepath.Join(dir, "s0.ass"), "-f", "webvtt", "pipe:1").Output()
	if err != nil {
		t.Fatalf("ffmpeg's own conversion: %v", err)
	}
	// Not a failure if a later ffmpeg stops doing it; then this note says so.
	t.Logf("ffmpeg's own conversion shows the drawing as a caption: %v; ends its lines the Windows way when taking the track out: %v",
		bytes.Contains(theirs, []byte("m 0 0 l 100 0")), bytes.Contains(raw, []byte("\r\n")))
}
