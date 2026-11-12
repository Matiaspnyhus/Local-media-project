package subs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The words of a caption as SubRip tracks hold them, and what is written
// for them: the three marks WebVTT also knows are kept, other marks are
// left out, and nothing else can mean anything to WebVTT.
func TestWordsOfATextTrack(t *testing.T) {
	for words, want := range map[string][]string{
		"Just words":                              {"Just words"},
		"Two\nlines":                              {"Two", "lines"},
		"Windows\r\nline ends":                    {"Windows", "line ends"},
		"An empty\n\n \nline between":             {"An empty", "line between"},
		"  spaces around  \n":                     {"spaces around"},
		"<i>Slanted</i> and <b>bold</b>":          {"<i>Slanted</i> and <b>bold</b>"},
		"<I>Capitals</I> <U>too</U>":              {"<i>Capitals</i> <u>too</u>"},
		`<font color="#ff0000">Red</font> words`:  {"Red words"},
		"<font face='Arial' size=12>Sized</font>": {"Sized"},
		"One<br>two<BR/>three":                    {"One", "two", "three"},
		`{\an8}At the top`:                        {"At the top"},
		`{\an8}{\i1}Both{\i0}`:                    {"Both"},
		"{not an instruction}":                    {"{not an instruction}"},
		`{\never closed`:                          {`{\never closed`},
		"Tom & Jerry":                             {"Tom &amp; Jerry"},
		"&amp; as some files have it":             {"&amp;amp; as some files have it"},
		"1 < 2 and 3 > 2":                         {"1 &lt; 2 and 3 &gt; 2"},
		"I <3 you":                                {"I &lt;3 you"},
		"a<b":                                     {"a&lt;b"},
		"<< Earlier":                              {"&lt;&lt; Earlier"},
		"<not closed\nbefore the line ends>":      {"&lt;not closed", "before the line ends&gt;"},
		"00:00:01.000 --> 00:00:09.000":           {"00:00:01.000 --&gt; 00:00:09.000"},
		"<v Mallory>a voice of WebVTT's":          {"a voice of WebVTT's"},
		"<c.loud>a class of WebVTT's</c>":         {"&lt;c.loud&gt;a class of WebVTT's"},
		"<00:00:05.000>a time of WebVTT's":        {"&lt;00:00:05.000&gt;a time of WebVTT's"},
		"</>":                                     {"&lt;/&gt;"},
		"<>":                                      {"&lt;&gt;"},
		"Blåbær, 日本語 ♪":                           {"Blåbær, 日本語 ♪"},
		"caf\xe9 \x96 na\xefve":                   {"café – naïve"},
		"":                                        nil,
		" \n\r\n":                                 nil,
		`{\an8}`:                                  nil,
		"<font size=3></font>":                    nil,
	} {
		if got := srtLines([]byte(words)); !reflect.DeepEqual(got, want) {
			t.Errorf("%q became %q, want %q", words, got, want)
		}
	}
	// A caption of a WebVTT track begins with two lines that are not its
	// words, and its words are WebVTT as they stand.
	for words, want := range map[string][]string{
		"\n\nJust words": {"Just words"},
		"cue-7\nline:10% align:left\nNamed, placed": {"Named, placed"},
		"\n\n<v Fred>A voice</v> & <i>more</i>":     {"<v Fred>A voice</v> & <i>more</i>"},
		"\n\nTwo\n\nlines":                          {"Two", "lines"},
		"\n\nAn arrow --> in the words":             {"An arrow --&gt; in the words"},
		"\n\ncaf\xe9":                               {"café"},
		"\n\n":                                      nil,
		"no two lines in front":                     {"no two lines in front"},
		"":                                          nil,
	} {
		if got := vttLines([]byte(words)); !reflect.DeepEqual(got, want) {
			t.Errorf("%q of a WebVTT track became %q, want %q", words, got, want)
		}
	}
}

// What is written for a text track comes from ffmpeg's copy of it, caption
// by caption. Nothing a caption holds can break the file: empty lines, a
// line that looks like the next caption's times, text in the old encoding.
// The first caption is the earliest, wherever it stands; a caption with no
// time to be shown in, or with nothing to show, is not written.
func TestWritingATextTrack(t *testing.T) {
	dir := t.TempDir()
	copyOf := func(codec string, captions ...textCaption) string {
		path := filepath.Join(dir, "t0.mks")
		if err := os.WriteFile(path, textMKS(codec, captions...), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	to := filepath.Join(dir, "t0.vtt")
	first, size, err := textTrack(copyOf("S_TEXT/UTF8",
		textCaption{5000, 1500, "Later in the film,\n\n2\n00:00:09,000 --> 00:00:10,000\nstill the same caption"},
		textCaption{36000000, 2000, "Ten hours in"},
		textCaption{1000, 800, "Bl\xe5b\xe6r <i>first</i>"},
		textCaption{2000, 0, "No time to be shown in"},
		textCaption{3000, 500, " \n "},
		textCaption{3000, 500, "At the same moment"},
	), to)
	want := "WEBVTT\n" +
		"\n00:00:05.000 --> 00:00:06.500\nLater in the film,\n2\n00:00:09,000 --&gt; 00:00:10,000\nstill the same caption\n" +
		"\n10:00:00.000 --> 10:00:02.000\nTen hours in\n" +
		"\n00:00:01.000 --> 00:00:01.800\nBlåbær <i>first</i>\n" +
		"\n00:00:03.000 --> 00:00:03.500\nAt the same moment\n"
	got := string(mustRead(t, to))
	if err != nil || first != 1000 || size != int64(len(want)) || got != want {
		t.Errorf("first caption at %d ms, %d bytes, %v:\n%q\nwant\n%q", first, size, err, got, want)
	}
	// What was written passes for a text track wherever one is checked.
	if facts, size, err := check(formVTT, dir, 0); err != nil || facts.FirstMs != 1000 || size != int64(len(want)) {
		t.Errorf("checked as a file ffmpeg wrote: %+v, %d bytes, %v", facts, size, err)
	}

	// A WebVTT track is copied out with two lines in front of each
	// caption's words.
	first, _, err = textTrack(copyOf("D_WEBVTT/SUBTITLES", textCaption{700, 300, "\n\n<v Fred>Hello</v>"}, textCaption{2000, 1000, "id\nline:0\nSecond"}), to)
	want = "WEBVTT\n\n00:00:00.700 --> 00:00:01.000\n<v Fred>Hello</v>\n\n00:00:02.000 --> 00:00:03.000\nSecond\n"
	if got := string(mustRead(t, to)); err != nil || first != 700 || got != want {
		t.Errorf("a WebVTT track: first caption at %d ms, %v:\n%q\nwant\n%q", first, err, got, want)
	}

	// A track without captions is a file with a heading and nothing else.
	first, size, err = textTrack(copyOf("S_TEXT/UTF8"), to)
	if got := string(mustRead(t, to)); err != nil || first != -1 || size != 7 || got != "WEBVTT\n" {
		t.Errorf("a track without captions: %d, %d bytes, %v: %q", first, size, err, got)
	}

	// A copy that is something other than text is not written out as text.
	if _, _, err := textTrack(copyOf("S_HDMV/PGS", textCaption{1000, 800, "PG..."}), to); !errors.Is(err, errNotText) {
		t.Errorf("a copy of a picture track: %v", err)
	}
	// Nor is one that holds more than any film has words.
	much := textCaption{1000, 800, strings.Repeat("words ", 1<<20)}
	if _, _, err := textTrack(copyOf("S_TEXT/UTF8", much, much, much, much, much, much), to); !errors.Is(err, errNotText) {
		t.Errorf("a copy of %d MB: %v", 6*len(much.words)>>20, err)
	}
	if _, _, err := textTrack(filepath.Join(dir, "never written.mks"), to); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a copy that is not there: %v", err)
	}
}

// ffmpeg's copy must be whole: cut anywhere, with a byte too many, or with
// a caption that says it is longer than the group it is in, it is refused,
// and never taken for a shorter track.
func TestACopyOfATextTrackCutShortIsNoticed(t *testing.T) {
	whole := textMKS("S_TEXT/UTF8", textCaption{1000, 800, "One"}, textCaption{2000, 800, "Two"}, textCaption{3000, 800, "Three"})
	codec, list, err := readTextTrack(bytes.NewReader(whole), int64(len(whole)))
	want := []caption{{1000, 1800, []byte("One")}, {2000, 2800, []byte("Two")}, {3000, 3800, []byte("Three")}}
	if err != nil || codec != "S_TEXT/UTF8" || !reflect.DeepEqual(list, want) {
		t.Fatalf("the whole copy: %q, %+v, %v", codec, list, err)
	}
	for cut := 0; cut < len(whole); cut++ {
		if _, list, err := readTextTrack(bytes.NewReader(whole[:cut]), int64(cut)); !errors.Is(err, errNotWhole) {
			t.Fatalf("cut after %d of %d bytes: %v, %d captions", cut, len(whole), err, len(list))
		}
	}
	if _, _, err := readTextTrack(bytes.NewReader(append(append([]byte{}, whole...), 0)), int64(len(whole))+1); !errors.Is(err, errNotWhole) {
		t.Errorf("with a byte too many: %v", err)
	}
	// The last caption's words are said to be one byte longer than they
	// are, which is one more than its group has room for. (The length is
	// the last of the eight bytes in front of the words' block.)
	at := bytes.LastIndex(whole, []byte{0x81, 0, 0, 0, 'T', 'h', 'r', 'e', 'e'})
	spoiled := append([]byte{}, whole...)
	spoiled[at-1]++
	if _, _, err := readTextTrack(bytes.NewReader(spoiled), int64(len(spoiled))); at < 8 || !errors.Is(err, errNotWhole) {
		t.Errorf("a caption that overruns its group: %v", err)
	}
	// Damaged anywhere, it is read without stumbling, whatever comes of it.
	for at := 0; at < len(whole); at++ {
		spoiled := append([]byte{}, whole...)
		spoiled[at] ^= 0xFF
		readTextTrack(bytes.NewReader(spoiled), int64(len(spoiled)))
	}
}

// The one text format ffmpeg still converts itself (that of MP4 files)
// comes as a WebVTT file, which is whole if it has its heading and its last
// line is ended; its first caption is the earliest in it, not the first.
func TestCheckingAWebVTTFileFFmpegWrote(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		file  string
		whole bool
		first int64
	}{
		"as ffmpeg writes it":       {"WEBVTT\n\n00:01.000 --> 00:02.000\nOne\n\n00:03.000 --> 00:04.000\nTwo\n\n", true, 1000},
		"Windows line ends":         {"WEBVTT\r\n\r\n00:01.000 --> 00:02.000\r\nOne\r\n\r\n", true, 1000},
		"hours in the times":        {"WEBVTT\n\n01:00:01.000 --> 01:00:02.000\nOne\n\n", true, 3601000},
		"the earliest is not first": {"WEBVTT\n\n00:05.000 --> 00:06.000\nLater\n\n00:01.500 --> 00:02.000\nEarlier\n\n", true, 1500},
		"no captions":               {"WEBVTT\n\n", true, -1},
		"nothing at all":            {"", false, -1},
		"no heading":                {"\n00:01.000 --> 00:02.000\nOne\n\n", false, -1},
		"half a heading":            {"WEB", false, -1},
		"cut inside a caption":      {"WEBVTT\n\n00:01.000 --> 00:02.000\nOn", false, -1},
		"cut inside a time":         {"WEBVTT\n\n00:01.000 --> 00:0", false, -1},
		"cut inside the heading":    {"WEBVTT", false, -1},
	} {
		if err := os.WriteFile(filepath.Join(dir, "t3.vtt"), []byte(c.file), 0o600); err != nil {
			t.Fatal(err)
		}
		facts, size, err := check(formVTT, dir, 3)
		if c.whole && (err != nil || facts.FirstMs != c.first || size != int64(len(c.file))) {
			t.Errorf("%s: first caption at %d ms, %d bytes, %v", name, facts.FirstMs, size, err)
		}
		if !c.whole && !errors.Is(err, errNotWhole) {
			t.Errorf("%s: taken for whole: %v", name, err)
		}
	}
}
