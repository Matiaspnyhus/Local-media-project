package subs

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeText turns a subtitle file's bytes into UTF-8 text. Subtitle files
// come in whatever encoding their maker's computer used: UTF-8 (with or
// without a marker at the start), UTF-16, or the old Western European one.
//
// This is the server's function of the same name, copied so that this
// package does not need the server.
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
	return []byte(modern(raw))
}

// modern reads text that is either in today's encoding (UTF-8) or in the
// old Western European one (Windows-1252), in which every byte is one
// character.
func modern(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	var b strings.Builder
	for _, c := range raw {
		if c >= 0x80 && c <= 0x9F {
			b.WriteRune(cp1252[c-0x80])
		} else {
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

var cp1252 = [32]rune{0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
	0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178}

// clip makes text a film says about itself fit to keep and to show: on one
// line, with single spaces, and of a length worth keeping. A file can
// claim a title of any size with anything in it.
func clip(s string) string {
	const most = 200
	s = strings.ToValidUTF8(s, "�")
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(s) <= most {
		return s
	}
	return string([]rune(s)[:most])
}

// The shape of a language's code as films give it: two or three letters
// ("en", "nor"), sometimes with more after hyphens ("pt-br", "zh-hant").
var languageShape = regexp.MustCompile(`^[a-z]{2,8}(-[a-z0-9]{1,8})*$`)

// language tidies what a film calls a track's language. Anything that does
// not have the shape of a language's code is dropped, and so is "und",
// which is how films say they do not know. What is kept is letters, digits
// and hyphens only, so nothing a later step does with it can go wrong.
func language(said string) string {
	l := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(said)), "_", "-")
	if l == "und" || len(l) > 35 || !languageShape.MatchString(l) {
		return ""
	}
	return l
}
