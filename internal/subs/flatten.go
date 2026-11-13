package subs

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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
// it a border. A sign set one letter to a line is put together again
// (joinLetters), and a song that a program wrote out syllable by syllable
// is taken from the line it was made from (songLines). Words that are in a
// line but made wholly see-through are not in the caption.
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
		sources []scriptLine // lines kept as notes that a song's lines were made from
		count   int          // lines read so far, for the script's order
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
		// notes and are not shown, with one exception (see songLines).
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "format":
			if f, ok := readFields(value); ok {
				fields = f
			}
		case "dialogue":
			if l, ok := fields.read(value, count); ok {
				lines = append(lines, l)
				count++
			}
		case "comment":
			if l, ok := fields.read(value, count); ok && l.effect == "karaoke" {
				sources = append(sources, l)
				count++
			}
		}
	}
	return collapse(joinLetters(songLines(lines, sources)))
}

// scriptLine is one line of a script, cleaned.
type scriptLine struct {
	start, end int64
	text       string
	role       Role
	order      int    // its place in the script
	style      string // the name of its style
	effect     string // what its effect field says, in small letters
	pinned     bool   // it is put at a place of its own in the picture
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
		style: field(f.style), effect: strings.ToLower(field(f.effect)), pinned: pinned,
		role: roleOf(field(f.style), field(f.effect), karaoke, pinned)}, true
}

// songLines deals with songs whose lines were written by a program. The
// maker times a line to the music, syllable by syllable; the program (the
// one that comes with the usual editor for these scripts) then writes a
// line for each syllable, at its own place and with its own movement,
// marks those "fx", and turns the maker's line into a note marked
// "karaoke". Read plainly, the syllables are a heap of crumbs all shown at
// once, and nothing tells them from the song's translation, which is a
// song line too.
//
// So where a note marked "karaoke" has "fx" lines in its style at its
// time, the note is the caption, and those lines are left out. "fx" lines
// with no such note are kept: they are all there is of that song. A note
// with no such lines stays a note.
//
// "At its time" takes in the lines that end as the note begins or begin as
// it ends: a syllable is often brought in and taken out by lines of their
// own, before and after the song's line.
func songLines(lines, sources []scriptLine) []scriptLine {
	if len(sources) == 0 {
		return lines
	}
	made, from := map[string][]scriptLine{}, map[string][]scriptLine{}
	for _, l := range lines {
		if l.effect == "fx" {
			made[l.style] = append(made[l.style], l)
		}
	}
	if len(made) == 0 {
		return lines
	}
	for _, s := range sources {
		from[s.style] = append(from[s.style], s)
	}
	madeAt, fromAt := map[string][][2]int64{}, map[string][][2]int64{}
	for style, list := range made {
		madeAt[style] = stretches(list)
	}
	for style, list := range from {
		fromAt[style] = stretches(list)
	}
	out := make([]scriptLine, 0, len(lines))
	for _, l := range lines {
		if l.effect != "fx" || !within(fromAt[l.style], l.start, l.end) {
			out = append(out, l)
		}
	}
	for _, s := range sources {
		if within(madeAt[s.style], s.start, s.end) {
			s.role = Song
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].order < out[j].order })
	return out
}

// stretches returns the times a list of lines covers between them, as
// stretches that do not touch, in order.
func stretches(list []scriptLine) [][2]int64 {
	sort.Slice(list, func(i, j int) bool { return list[i].start < list[j].start })
	var out [][2]int64
	for _, l := range list {
		if n := len(out); n > 0 && l.start <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], l.end)
		} else {
			out = append(out, [2]int64{l.start, l.end})
		}
	}
	return out
}

// within says whether any of the stretches shares time with start to end,
// or touches it (as nearly as lines with the same words have to be to be
// shown as one).
func within(stretches [][2]int64, start, end int64) bool {
	i := sort.Search(len(stretches), func(i int) bool { return stretches[i][1]+joinMs >= start })
	return i < len(stretches) && stretches[i][0]-joinMs <= end
}

// joinLetters deals with signs that are set one letter to a line, each
// letter at its own place, so that they can be coloured or moved one by
// one. Read plainly that is a pile of one-letter captions, all shown at
// once. Lines of a single letter that follow each other in the script, are
// shown from and to the same moments, are each put at a place of their
// own and have the same style are taken for one sign and made one line.
//
// (Where the sign has a space, the script has no line, so the words of a
// sign of several come out run together. A sign whose letters do not
// follow each other in the script is not put together.)
func joinLetters(lines []scriptLine) []scriptLine {
	letter := func(l scriptLine) bool { return l.pinned && utf8.RuneCountInString(l.text) == 1 }
	out := lines[:0]
	for i := 0; i < len(lines); {
		l := lines[i]
		j := i + 1
		if letter(l) {
			for ; j < len(lines) && letter(lines[j]) && lines[j].start == l.start && lines[j].end == l.end && lines[j].style == l.style; j++ {
				l.text += lines[j].text
			}
		}
		out = append(out, l)
		i = j
	}
	return out
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
// words that are shown, with "\n" between lines. It also says whether the
// line had karaoke timing, and whether it was pinned to a place in the
// picture.
func plainText(raw string) (text string, karaoke, pinned bool) {
	var b strings.Builder
	drawing := false // between {\p1} and {\p0} the "text" is pen movements
	hidden := false  // after {\alpha&HFF&} the text is there and cannot be seen
	for i := 0; i < len(raw); {
		c := raw[i]
		switch {
		case c == '{':
			end := strings.IndexByte(raw[i:], '}')
			if end < 0 {
				// A brace that is never closed is just a brace, and what
				// follows it is text like any other.
				if !drawing && !hidden {
					b.WriteByte(c)
				}
				i++
				continue
			}
			// Instructions each begin with a backslash. A block without
			// one is a note by the maker, and is not shown either.
			changing := false // inside "\t(...)", which says what the text turns into over a time
			for _, tag := range strings.Split(raw[i+1:i+end], `\`)[1:] {
				tag = strings.TrimSpace(tag)
				later := changing
				if strings.HasPrefix(tag, "t(") {
					changing = true
				}
				if strings.Contains(tag, ")") {
					changing = false
				}
				switch {
				case strings.HasPrefix(tag, "pos(") || strings.HasPrefix(tag, "move("):
					pinned = true
				case strings.HasPrefix(tag, "alpha"):
					// "\alpha&HFF&" makes the text wholly see-through,
					// and any other value, or none, makes it seen. Text
					// that turns see-through or seen over a time is seen
					// for some of it, and counts as seen.
					if through := seeThrough(tag[len("alpha"):]); !later {
						hidden = through
					} else if !through {
						hidden = false
					}
				case strings.HasPrefix(tag, "r"):
					hidden = false // "\r": everything back to what the style says
				case karaokeTag.MatchString(tag):
					karaoke = true
				case strings.HasPrefix(tag, "p") && !strings.HasPrefix(tag, "pos") && !strings.HasPrefix(tag, "pbo"):
					// "\p" with a number above 0 begins a drawing; with 0,
					// less, or no number it ends one. The number is read as
					// the programs that draw these scripts read it: as far
					// as it is one, whatever follows it ("\p1.5", "\p1)").
					// "\pos" and "\pbo" are other things.
					drawing = leadingNumber(tag[1:]) > 0
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
			if !drawing && !hidden {
				b.WriteByte(raw[i+1]) // a brace that is meant to be seen
			}
			i += 2
		default:
			if !drawing && !hidden {
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

// leadingNumber reads the whole number a text begins with, after any
// spaces, and returns 0 if it begins with none.
func leadingNumber(s string) int {
	s = strings.TrimLeft(s, " \t")
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	n := 0
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9' && n < 1000000; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if negative {
		return -n
	}
	return n
}

// seeThrough reads what follows "\alpha": how see-through the text is to
// be, from 00 (not at all) to FF (wholly), written "&HFF&". It says
// whether that is wholly.
func seeThrough(value string) bool {
	n, err := strconv.ParseUint(strings.Trim(value, "&Hh) \t"), 16, 32)
	return err == nil && n&0xFF == 0xFF
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
