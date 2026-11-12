package subs

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// A styled track (ASS, or its older form SSA) is a script: a list of lines,
// each with a time, a named style and a text that is full of instructions
// in braces: where to put the line, how to fade it, how to move it. Some
// "lines" are not words at all but drawings, given as a row of pen
// movements. Signs that move with the film's picture are written as one
// line for every frame.
//
// A device that cannot be given the styled form gets the words alone.
// ffmpeg can make that conversion, but it prints the pen movements as if
// they were speech ("m 0 0 l 800 0 800 450"), and a sign that takes fifty
// lines to move across the screen fifty times over. So it is done here.

// Role says what a caption is for, as far as that can be told.
type Role int

const (
	// Speech is what people say, and anything not known to be one of the
	// other two.
	Speech Role = iota
	// Sign is the translation of something written in the picture: a shop
	// sign, a letter, a title.
	Sign
	// Song is the words of a song: an opening or ending, or karaoke.
	Song
)

func (r Role) String() string {
	switch r {
	case Sign:
		return "sign"
	case Song:
		return "song"
	}
	return "speech"
}

// Cue is one plain caption.
type Cue struct {
	// StartMs and EndMs are when it is shown, on the clock the script
	// uses.
	StartMs, EndMs int64
	// Text is its words: one or more lines with "\n" between them, and
	// nothing in it but the words.
	Text string
	// Role says what the caption is for. It is a judgement, not a fact: a
	// script has no field for it. It comes from the name of the line's
	// style ("Sign", "OP Romaji", "Song-ED"), which most makers of such
	// scripts choose along those lines but nothing makes them, and from
	// the line itself: a line with karaoke timing is a song, and a line
	// pinned to a spot in the picture is a sign. A script whose styles are
	// all called "Default" and whose signs are not pinned gives Speech for
	// everything.
	Role Role
}

// joinMs is how far apart two captions with the same words may be and
// still be shown as one. It covers the gaps that rounding leaves between
// the frames of a moving sign, and is too short for anyone to have been
// meant to see.
const joinMs = 100

// Flatten turns a styled script into plain captions: the spoken lines and
// the signs and songs as words, with every instruction taken out and
// drawings left out altogether.
//
// Lines with the same words that follow each other without a pause, or are
// shown at once, become one caption. That is what makes one caption of a
// sign written once for every frame, and of a line written twice to give
// it a border.
//
// The captions come in the order they appear. Of those that appear at the
// same moment, speech comes first, which a player puts lowest, and
// otherwise the script's own order is kept.
//
// Everything is kept, signs and songs too: what a sign says is often what
// the scene is about. Each caption says what it was taken for (see
// Cue.Role), so a caller can leave some out.
func Flatten(script []byte) []Cue {
	text := string(decodeText(script))
	var (
		section string
		fields  = defaultFields
		lines   []scriptLine
	)
	for len(text) > 0 {
		// A line ends the Unix way, the Windows way or the old Mac way. (A
		// heavily styled script runs to tens of megabytes, so it is gone
		// through as it lies and not copied about.)
		var line string
		if i := strings.IndexAny(text, "\r\n"); i < 0 {
			line, text = text, ""
		} else {
			line = text[:i]
			if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			text = text[i+1:]
		}
		line = strings.TrimSpace(line)
		if line == "" || line[0] == ';' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			section = strings.ToLower(line)
			continue
		}
		// Fonts and pictures kept inside a script are written as text
		// that means nothing.
		if section == "[fonts]" || section == "[graphics]" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		// Lines are taken wherever they stand: scripts turn up with their
		// headings missing or misspelt. "Comment:" lines are the maker's
		// notes and are not shown.
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "format":
			if f, ok := readFields(value); ok {
				fields = f
			}
		case "dialogue":
			if l, ok := fields.read(value, len(lines)); ok {
				lines = append(lines, l)
			}
		}
	}
	return collapse(lines)
}

// scriptLine is one line of a script, cleaned.
type scriptLine struct {
	start, end int64
	text       string
	role       Role
	order      int // its place in the script
}

// lineFields says where in a line each thing is. A script says so itself,
// in a line that begins "Format:"; the text is always what is left after
// the other fields, commas and all.
type lineFields struct {
	count                     int // how many fields, the text included
	start, end, style, effect int // -1 for a field the script does not have
}

// The usual order. Old scripts (SSA, "v4") have a field called Marked
// where newer ones (ASS, "v4+") have Layer; everything else is in the same
// place.
var defaultFields = lineFields{count: 10, start: 1, end: 2, style: 3, effect: 8}

func readFields(format string) (lineFields, bool) {
	names := strings.Split(format, ",")
	f := lineFields{count: len(names), start: -1, end: -1, style: -1, effect: -1}
	text := false
	for i, n := range names {
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "start":
			f.start = i
		case "end":
			f.end = i
		case "style":
			f.style = i
		case "effect":
			f.effect = i
		case "text":
			text = true
		}
	}
	// The styles have a "Format:" line of their own, which is not this.
	return f, text && f.start >= 0 && f.end >= 0
}

func (f lineFields) read(value string, order int) (scriptLine, bool) {
	parts := strings.SplitN(value, ",", f.count)
	if len(parts) < f.count {
		return scriptLine{}, false
	}
	start, ok1 := assTime(parts[f.start])
	end, ok2 := assTime(parts[f.end])
	if !ok1 || !ok2 || end <= start {
		return scriptLine{}, false
	}
	field := func(i int) string {
		if i < 0 || i >= f.count-1 {
			return ""
		}
		return strings.TrimSpace(parts[i])
	}
	text, karaoke, pinned := plainText(parts[f.count-1])
	if text == "" {
		return scriptLine{}, false
	}
	return scriptLine{start: start, end: end, text: text, order: order,
		role: roleOf(field(f.style), field(f.effect), karaoke, pinned)}, true
}

// assTime reads a time as scripts write it: hours, minutes, seconds and
// hundredths, "1:02:03.45". The hours take as many digits as they need.
func assTime(s string) (int64, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, false
	}
	sec, frac, _ := strings.Cut(parts[2], ".")
	parts[2] = sec
	var total int64
	for _, p := range parts {
		v, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil || v < 0 || v > 1000000 {
			return 0, false
		}
		total = total*60 + v
	}
	total *= 1000
	if frac != "" {
		v, err := strconv.ParseInt(frac, 10, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		switch len(frac) {
		case 1, 2:
			total += v * 10 // hundredths, as the programs that draw these scripts read it
		case 3:
			total += v
		default:
			return 0, false
		}
	}
	return total, true
}

// plainText takes the instructions out of a line's text and returns the
// words, with "\n" between lines. It also says whether the line had
// karaoke timing, and whether it was pinned to a place in the picture.
func plainText(raw string) (text string, karaoke, pinned bool) {
	var b strings.Builder
	drawing := false // between {\p1} and {\p0} the "text" is pen movements
	for i := 0; i < len(raw); {
		c := raw[i]
		switch {
		case c == '{':
			end := strings.IndexByte(raw[i:], '}')
			if end < 0 {
				// A brace that is never closed is just a brace.
				if !drawing {
					b.WriteString(raw[i:])
				}
				i = len(raw)
				continue
			}
			// Instructions each begin with a backslash. A block without
			// one is a note by the maker, and is not shown either.
			for _, tag := range strings.Split(raw[i+1:i+end], `\`)[1:] {
				tag = strings.TrimSpace(tag)
				switch {
				case strings.HasPrefix(tag, "pos(") || strings.HasPrefix(tag, "move("):
					pinned = true
				case len(tag) > 1 && tag[0] == 'p' && digits(tag[1:]):
					// \p1 (or any number but 0) begins a drawing, \p0 ends
					// it. \pos and \pbo are other things.
					n, _ := strconv.Atoi(tag[1:])
					drawing = n != 0
				case karaokeTag.MatchString(tag):
					karaoke = true
				}
			}
			i += end + 1
		case c == '\\' && i+1 < len(raw) && (raw[i+1] == 'N' || raw[i+1] == 'n'):
			b.WriteByte('\n')
			i += 2
		case c == '\\' && i+1 < len(raw) && raw[i+1] == 'h':
			b.WriteByte(' ') // a space that lines are not broken at
			i += 2
		case c == '\\' && i+1 < len(raw) && (raw[i+1] == '{' || raw[i+1] == '}'):
			if !drawing {
				b.WriteByte(raw[i+1]) // a brace that is meant to be seen
			}
			i += 2
		default:
			if !drawing {
				b.WriteByte(c)
			}
			i++
		}
	}
	// What is left is tidied: no empty lines, and one space wherever there
	// are several (where an instruction or a drawing stood between two).
	var lines []string
	for _, line := range strings.Split(b.String(), "\n") {
		words := strings.FieldsFunc(line, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
		if len(words) > 0 {
			lines = append(lines, strings.Join(words, " "))
		}
	}
	return strings.Join(lines, "\n"), karaoke, pinned
}

// karaokeTag matches the instructions that time a line's syllables to
// music: \k, \K, \kf, \ko and \kt, each followed by a number.
var karaokeTag = regexp.MustCompile(`^(k[fot]?|K)\s*\d`)

func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// Words that makers of scripts put in the names of styles for lines that
// are not speech. Signs are looked for first: "Song Title" and "ED Credits"
// are signs shown during a song.
//
// Only the style's name is gone by. A line also names its speaker, and some
// makers write "Sign" there, but that field mostly holds the names of the
// film's characters, who may be called anything: Ed, Song, Kara.
var (
	signWords = []string{"sign", "title", "credit", "typeset", "note", "eyecatch", "logo", "staff"}
	songWords = []string{"song", "karaoke", "lyric", "romaji", "kanji", "opening", "ending", "insert", "music"}
	// Short ones count only as a whole word, and "OP" (opening), "ED"
	// (ending) and "TS" (typesetting) only in capitals, with a number if
	// there are several: "OP2".
	signShort = regexp.MustCompile(`^TS$`)
	songShort = regexp.MustCompile(`^((OP|ED|OPED)\d*|(?i:kara|kfx|fx))$`)
	// "OPRomaji", "EDEnglish": the short ones run together with what
	// follows. "EDWARD" is somebody's name.
	songStart = regexp.MustCompile(`^(OP|ED)[A-Z][a-z]`)
)

// roleOf judges what a line is for. See Cue.Role for how far to trust it.
func roleOf(style, effect string, karaoke, pinned bool) Role {
	if e := strings.ToLower(effect); karaoke || e == "fx" || strings.Contains(e, "karaoke") {
		return Song
	}
	if r := roleOfStyle(style); r != Speech {
		return r
	}
	if pinned {
		return Sign
	}
	return Speech
}

func roleOfStyle(name string) Role {
	name = strings.TrimLeft(strings.TrimSpace(name), "*")
	// "Subtitle" has "title" in it, and is speech.
	low := strings.ReplaceAll(strings.ToLower(name), "subtitle", "")
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, w := range signWords {
		if strings.Contains(low, w) {
			return Sign
		}
	}
	for _, w := range words {
		if signShort.MatchString(w) {
			return Sign
		}
	}
	for _, w := range songWords {
		if strings.Contains(low, w) {
			return Song
		}
	}
	for _, w := range words {
		if songShort.MatchString(w) || songStart.MatchString(w) {
			return Song
		}
	}
	return Speech
}

// collapse makes one caption of lines with the same words that touch or
// overlap in time, and puts the captions in order.
func collapse(lines []scriptLine) []Cue {
	// Same words together, each group in order of time.
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].text != lines[j].text {
			return lines[i].text < lines[j].text
		}
		return lines[i].start < lines[j].start
	})
	var merged []scriptLine
	for i := 0; i < len(lines); {
		cur := lines[i]
		j := i + 1
		for ; j < len(lines) && lines[j].text == cur.text && lines[j].start <= cur.end+joinMs; j++ {
			cur.end = max(cur.end, lines[j].end)
			cur.order = min(cur.order, lines[j].order)
		}
		merged = append(merged, cur)
		i = j
	}
	sort.SliceStable(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		switch {
		case a.start != b.start:
			return a.start < b.start
		case a.role != b.role:
			return a.role < b.role
		}
		return a.order < b.order
	})
	cues := make([]Cue, 0, len(merged))
	for _, l := range merged {
		cues = append(cues, Cue{StartMs: l.start, EndMs: l.end, Text: l.text, Role: l.role})
	}
	return cues
}

var vttEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// WebVTT writes captions in the form browsers show. A caption that would
// begin before zero begins at zero; one that would be over by then is left
// out.
func WebVTT(cues []Cue) []byte {
	list := make([]vttCaption, 0, len(cues))
	for _, c := range cues {
		// "<" would begin an instruction of WebVTT's own.
		list = append(list, vttCaption{startMs: c.StartMs, endMs: c.EndMs, lines: ownLines(vttEscape.Replace(c.Text))})
	}
	file, _ := vttFile(list)
	return file
}

// vttCaption is a caption ready to be written: its lines are WebVTT as they
// stand, and none is empty (an empty line would end the caption).
type vttCaption struct {
	startMs, endMs int64
	lines          []string
}

// vttFile writes a WebVTT file. It also returns when the first of its
// captions is shown, or -1 if it has none.
func vttFile(cues []vttCaption) (file []byte, firstMs int64) {
	firstMs = -1
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	for _, c := range cues {
		start := max(c.startMs, 0)
		if c.endMs <= start || len(c.lines) == 0 {
			continue
		}
		if firstMs < 0 || start < firstMs {
			firstMs = start
		}
		b.WriteString("\n" + vttTime(start) + " --> " + vttTime(c.endMs) + "\n" + strings.Join(c.lines, "\n") + "\n")
	}
	return []byte(b.String()), firstMs
}

func vttTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
