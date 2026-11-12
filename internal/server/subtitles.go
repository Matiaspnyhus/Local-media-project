package server

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeText turns a subtitle file's bytes into UTF-8 text. Subtitle files
// come in whatever encoding their maker's computer used: UTF-8 (with or
// without a marker at the start), UTF-16 (likewise), or the old Western
// European one.
func decodeText(raw []byte) []byte {
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		raw = raw[3:]
	} else if bigEnd, mark, ok := inTwoBytes(raw); ok {
		raw = raw[mark:]
		u := make([]uint16, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			if bigEnd {
				u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
			} else {
				u = append(u, uint16(raw[i+1])<<8|uint16(raw[i]))
			}
		}
		return []byte(string(utf16.Decode(u)))
	}
	if utf8.Valid(raw) {
		return raw
	}
	return fromWindows1252(raw)
}

// inTwoBytes reports whether raw is text saved two bytes a letter (UTF-16),
// which of the two bytes comes first, and how long the mark at its start is
// that says so: 0 when it has none.
//
// Without the mark it is told by its zero bytes. A subtitle file is full
// of digits, punctuation and line breaks whatever language it is in. Two
// bytes a letter, each of those has a zero for one of its two bytes,
// always on the same side. Text in any encoding of one byte a letter, or
// in UTF-8, has no zero bytes at all.
func inTwoBytes(raw []byte) (bigEnd bool, mark int, ok bool) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		return false, 2, true
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return true, 2, true
	}
	start := raw[:min(len(raw), 4096)&^1] // the start of the file is enough to tell by
	var first, second int                 // zero bytes that are the first, and the second, of their pair
	for i, c := range start {
		if c != 0 {
			continue
		}
		if i%2 == 0 {
			first++
		} else {
			second++
		}
	}
	// A quarter of the letters at least, and nearly all on one side. (Four
	// bytes a letter has its zeros on both sides, and is not read here.)
	pairs := len(start) / 2
	switch {
	case second > 0 && second*4 >= pairs && first*4 <= second:
		return false, 0, true
	case first > 0 && first*4 >= pairs && second*4 <= first:
		return true, 0, true
	}
	return false, 0, false
}

// fromWindows1252 reads bytes as the old Western European encoding
// (Windows-1252), in which every byte is one character.
func fromWindows1252(raw []byte) []byte {
	var b strings.Builder
	for _, c := range raw {
		if c >= 0x80 && c <= 0x9F {
			b.WriteRune(cp1252[c-0x80])
		} else {
			b.WriteRune(rune(c))
		}
	}
	return []byte(b.String())
}

// readable returns whole lines of text as UTF-8, each line judged by
// itself: a line that is UTF-8 already stays as it is, and any other is
// read as the old Western European encoding. It is for text that arrives
// piece by piece (see runSubtitleJob), where the whole cannot be judged at
// once as decodeText does. Judging by the line also saves a subtitle in
// which only some lines are in the old encoding.
func readable(lines []byte) []byte {
	if utf8.Valid(lines) {
		return lines
	}
	out := make([]byte, 0, len(lines)+len(lines)/8)
	for len(lines) > 0 {
		line := lines
		if i := bytes.IndexByte(lines, '\n'); i >= 0 {
			line = lines[:i+1]
		}
		lines = lines[len(line):]
		if utf8.Valid(line) {
			out = append(out, line...)
		} else {
			out = append(out, fromWindows1252(line)...)
		}
	}
	return out
}

var cp1252 = [32]rune{0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
	0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178}

var (
	srtTiming = regexp.MustCompile(`^\s*(\d{1,3}):(\d{2}):(\d{2})[,.](\d{1,3})\s*-->\s*(\d{1,3}):(\d{2}):(\d{2})[,.](\d{1,3})`)
	srtTag    = regexp.MustCompile(`(?i)</?font[^>]*>|\{\\[^}]*\}`)
	onlyDigit = regexp.MustCompile(`^\s*\d+\s*$`)
)

// srtToVTT converts the commonest subtitle format (SubRip, .srt) to the one
// browsers show (WebVTT). The two differ in little more than punctuation.
func srtToVTT(raw []byte) []byte {
	text := strings.ReplaceAll(string(decodeText(raw)), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var out strings.Builder
	out.WriteString("WEBVTT\n\n")
	lines := strings.Split(text, "\n")
	inCue := false
	for i, line := range lines {
		if m := srtTiming.FindStringSubmatch(line); m != nil {
			if inCue {
				out.WriteString("\n")
			}
			pad := func(ms string) string { return (ms + "000")[:3] }
			out.WriteString(two(m[1]) + ":" + m[2] + ":" + m[3] + "." + pad(m[4]) + " --> " + two(m[5]) + ":" + m[6] + ":" + m[7] + "." + pad(m[8]) + "\n")
			inCue = true
			continue
		}
		if !inCue {
			continue
		}
		if strings.TrimSpace(line) == "" {
			out.WriteString("\n")
			inCue = false
			continue
		}
		// A line of digits right before a timing line is the next cue's
		// number, not text.
		if onlyDigit.MatchString(line) && i+1 < len(lines) && srtTiming.MatchString(lines[i+1]) {
			continue
		}
		line = srtTag.ReplaceAllString(line, "")
		// "-->" may not appear in a cue's text.
		out.WriteString(strings.ReplaceAll(line, "-->", "->") + "\n")
	}
	return []byte(out.String())
}

func two(h string) string {
	if len(h) < 2 {
		return "0" + h
	}
	return h
}

// maxSubtitleShiftMs is the most a subtitle may be asked to be moved. The
// shift exists for one case (see laterVTT), where it is a few seconds.
const maxSubtitleShiftMs = 60000

// laterVTT passes WebVTT text through with every caption moved later by a
// fixed time. It is used for a clip whose player clock runs ahead of the
// film's own (hls.Grid.LeadMs): its captions have to run ahead with it.
// The text may arrive in pieces of any size, so it is handled line by line.
type laterVTT struct {
	http.ResponseWriter
	ms   int64
	line []byte // a line that has not ended yet
}

func (l *laterVTT) Write(p []byte) (int, error) {
	l.line = append(l.line, p...)
	for {
		i := bytes.IndexByte(l.line, '\n')
		if i < 0 {
			return len(p), nil
		}
		if _, err := l.ResponseWriter.Write(laterCue(l.line[:i+1], l.ms)); err != nil {
			return 0, err
		}
		l.line = l.line[i+1:]
	}
}

func (l *laterVTT) Flush() {
	if f, ok := l.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finish sends a last line that did not end with a line break.
func (l *laterVTT) finish() {
	if len(l.line) > 0 {
		l.ResponseWriter.Write(laterCue(l.line, l.ms))
		l.line = nil
	}
}

var vttTiming = regexp.MustCompile(`^(\s*)((?:\d+:)?\d{2}:\d{2}\.\d{3})(\s+-->\s+)((?:\d+:)?\d{2}:\d{2}\.\d{3})`)

// laterCue returns line with its two times moved later by ms if it is the
// timing line of a caption ("00:01.000 --> 00:04.000"), and as it is
// otherwise.
func laterCue(line []byte, ms int64) []byte {
	m := vttTiming.FindSubmatchIndex(line)
	if m == nil {
		return line
	}
	from, ok1 := vttMs(line[m[4]:m[5]])
	to, ok2 := vttMs(line[m[8]:m[9]])
	if !ok1 || !ok2 {
		return line
	}
	out := append([]byte{}, line[m[2]:m[3]]...)
	out = append(out, vttTime(from+ms)...)
	out = append(out, line[m[6]:m[7]]...)
	out = append(out, vttTime(to+ms)...)
	return append(out, line[m[9]:]...)
}

// vttMs reads a WebVTT time ("01:02:03.456" or "02:03.456") as milliseconds.
func vttMs(b []byte) (int64, bool) {
	parts := strings.Split(string(b), ":")
	sec, frac, _ := strings.Cut(parts[len(parts)-1], ".")
	parts[len(parts)-1] = sec
	var total int64
	for _, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0, false
		}
		total = total*60 + v
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, false
	}
	return total*1000 + f, true
}

func vttTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
