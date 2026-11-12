package subs

import (
	"errors"
	"io"
	"os"
	"strings"
)

// Text tracks are not converted to WebVTT by ffmpeg. It would do it, but it
// reads every caption as text in today's encoding (UTF-8) and drops, with
// no more than a remark, each one that is not: a film put together from an
// old subtitle file lost its lines with accented letters that way, and
// when too few of a track's captions could be read ffmpeg gave up on the
// whole film, its other tracks included.
//
// So ffmpeg only copies such a track out, caption by caption as the film
// holds it, into a small Matroska file in the working folder, and the
// WebVTT file is written from that here. Nothing is lost on the way, and a
// caption in the old Western European encoding is read as that.
//
// (This goes for the text formats MKV files hold. The one MP4 files have
// is not text but a form of its own that only ffmpeg reads; such a track is
// still converted by ffmpeg.)

// caption is one caption of a text track as the film holds it.
type caption struct {
	startMs, endMs int64
	words          []byte
}

// maxTextBytes is the most a text track is read for. A film's whole
// dialogue is a few hundred thousand bytes; this is what the program has
// always allowed one subtitle.
const maxTextBytes = 32 << 20

// errNotText means a track that calls itself text holds more than any text
// track does, or was copied out as something that is not text.
var errNotText = errors.New("this is not a text track")

// textTrack writes the WebVTT file for a text track from the copy of it
// that ffmpeg made. It returns when the first caption is shown (-1 if there
// is none) and the size of what it wrote.
func textTrack(from, to string) (firstMs, size int64, err error) {
	in, err := os.Open(from)
	if err != nil {
		return -1, 0, err
	}
	st, err := in.Stat()
	if err != nil {
		in.Close()
		return -1, 0, err
	}
	codec, list, err := readTextTrack(in, st.Size())
	in.Close()
	if err != nil {
		return -1, 0, err
	}
	lines := srtLines
	switch codec {
	case "S_TEXT/UTF8", "S_TEXT/ASCII":
	case "D_WEBVTT/SUBTITLES", "S_TEXT/WEBVTT":
		lines = vttLines
	default:
		return -1, 0, errNotText
	}
	cues := make([]vttCaption, 0, len(list))
	for _, c := range list {
		cues = append(cues, vttCaption{startMs: c.startMs, endMs: c.endMs, lines: lines(c.words)})
	}
	file, firstMs := vttFile(cues)
	// Written and made sure to be on the disk, so that a full disk is
	// found out now.
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return -1, 0, err
	}
	_, err = out.Write(file)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return firstMs, int64(len(file)), err
}

// readTextTrack reads every caption from a Matroska file that holds one
// text track, as ffmpeg writes it: its captions in groups ("clusters"),
// each caption with its time, counted from its cluster's, and how long it
// stays. It also returns the name of the track's format.
//
// The file must be whole, as when a picture track is checked (see
// readMKS).
func readTextTrack(r io.ReaderAt, length int64) (codec string, list []caption, err error) {
	id, size, data, err := ebmlHeader(r, 0)
	if err != nil || id != idEBML || size == unknownSize {
		return "", nil, errNotWhole
	}
	id, size, data, err = ebmlHeader(r, data+size)
	if err != nil || id != idSegment || size == unknownSize || size != length-data {
		return "", nil, errNotWhole
	}
	about := mksAbout{scale: 1000000}
	var total int64
	for off := data; off < length; {
		id, size, body, err := ebmlHeader(r, off)
		if err != nil || size == unknownSize || size > length-body {
			return "", nil, errNotWhole
		}
		switch id {
		case idInfo, idTracks:
			if size > 4<<20 {
				return "", nil, errNotText
			}
			b := make([]byte, size)
			if _, err := r.ReadAt(b, body); err != nil {
				return "", nil, errNotWhole
			}
			about.read(b)
		case idCluster:
			if total += size; total > maxTextBytes {
				return "", nil, errNotText
			}
			b := make([]byte, size)
			if _, err := r.ReadAt(b, body); err != nil {
				return "", nil, errNotWhole
			}
			var clusterTime int64
			whole := true
			inOrder := ebmlChildren(b, func(id uint32, v []byte) {
				var c caption
				switch id {
				case idClusterTime:
					clusterTime = int64(ebmlUint(v))
					return
				case idSimpleBlock:
					// A caption that does not say how long it stays, which
					// ffmpeg does not write. It would never be shown.
					rel, words, ok := blockParts(v)
					if !ok {
						whole = false
						return
					}
					c = caption{startMs: clusterTime + rel, endMs: clusterTime + rel, words: words}
				case idBlockGroup:
					found := false
					if !ebmlChildren(v, func(id uint32, v []byte) {
						switch id {
						case idBlock:
							if rel, words, ok := blockParts(v); ok {
								c.startMs, c.words, found = clusterTime+rel, words, true
							}
						case idBlockLength:
							c.endMs = int64(ebmlUint(v))
						}
					}) || !found {
						whole = false
						return
					}
					c.endMs += c.startMs
				default:
					return
				}
				c.startMs, c.endMs = c.startMs*about.scale/1000000, c.endMs*about.scale/1000000
				list = append(list, c)
			})
			if !whole || !inOrder {
				return "", nil, errNotWhole
			}
		}
		off = body + size
	}
	return about.codec, list, nil
}

// srtLines turns the words of one caption of a SubRip or plain text track
// into the lines of a WebVTT caption.
//
// Such text is mostly just text. It may mark words as italic, bold or
// underlined the way web pages do, which WebVTT does too; anything else
// written that way (a colour, a font) is left out, as are the instructions
// borrowed from styled tracks ("{\an8}" puts a line at the top). What is
// left is written so that no "<" or "&" in it means anything to WebVTT.
func srtLines(words []byte) []string {
	text := modern(words)
	var b strings.Builder
	for i := 0; i < len(text); {
		switch c := text[i]; c {
		case '{':
			if end := strings.IndexByte(text[i:], '}'); end > 0 && strings.HasPrefix(text[i:], `{\`) {
				i += end + 1
				continue
			}
			b.WriteByte(c)
		case '<':
			if name, n := markupTag(text[i:]); n > 0 {
				switch name {
				case "i", "b", "u", "/i", "/b", "/u":
					b.WriteString("<" + name + ">")
				case "br", "br/":
					b.WriteByte('\n')
				}
				i += n
				continue
			}
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		default:
			b.WriteByte(c)
		}
		i++
	}
	return ownLines(b.String())
}

// markupTag looks at text that begins with "<" and says whether it begins
// with a tag as web pages write them ("<i>", "</font>", "<font
// color=red>"). It returns the tag's name in small letters, with the stroke
// in front if it has one, and how much of the text the tag takes up; that
// is 0 if this "<" is just a "<", as in "1 < 2" or "<3".
func markupTag(text string) (name string, length int) {
	end := strings.IndexAny(text[1:], "<>\r\n")
	if end < 0 || text[1+end] != '>' || end > 200 {
		return "", 0
	}
	inside := text[1 : 1+end]
	name, _, _ = strings.Cut(inside, " ")
	letters := strings.TrimPrefix(strings.TrimSuffix(name, "/"), "/")
	if letters == "" || !isLetter(letters[0]) {
		return "", 0
	}
	for i := 0; i < len(letters); i++ {
		if !isLetter(letters[i]) && (letters[i] < '0' || letters[i] > '9') {
			return "", 0
		}
	}
	return strings.ToLower(name), end + 2
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// vttLines turns one caption of a WebVTT track, as ffmpeg copies it out of
// an MKV file, into the lines of a WebVTT caption. The caption begins with
// two lines that are not words (a name for it, and where on the screen to
// put it), and the rest is written as WebVTT already.
func vttLines(words []byte) []string {
	text := modern(words)
	for skip := 0; skip < 2; skip++ {
		_, rest, found := strings.Cut(text, "\n")
		if !found {
			break
		}
		text = rest
	}
	// The one thing that may not stand in a caption is the arrow that
	// separates a caption's two times.
	return ownLines(strings.ReplaceAll(text, "-->", "--&gt;"))
}

// ownLines splits text into its lines and leaves out the empty ones: in
// WebVTT an empty line ends the caption.
func ownLines(text string) []string {
	var lines []string
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
