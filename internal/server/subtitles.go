package server

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeText turns a subtitle file's bytes into UTF-8 text. Subtitle files
// come in whatever encoding their maker's computer used: UTF-8 (with or
// without a marker at the start), UTF-16, or the old Western European one.
func decodeText(raw []byte) []byte {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		raw = raw[3:]
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}), bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		big := raw[0] == 0xFE
		raw = raw[2:]
		u := make([]uint16, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			if big {
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
	// Windows-1252: every byte is one character.
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
