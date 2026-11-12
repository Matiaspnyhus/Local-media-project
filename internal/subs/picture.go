package subs

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// PictureFacts is what has to be known about a picture subtitle track
// before it can be laid over a film.
type PictureFacts struct {
	// Width and Height are the size of picture the track was made for.
	// Its captions are drawn, and placed, for a picture that big, so laid
	// over a picture of another size they have to be scaled by the
	// difference.
	//
	// Both are 0 when the file does not say. That is so for a DVD's track
	// taken from a file of the kind DVDs themselves use (".vob", ".mpg"),
	// where the text that describes the track is not in the film, and for
	// a disc's track without a single caption. Such a track was made for
	// the film's own picture, as near as anyone can tell: whoever lays it
	// over the film goes by the film's size.
	Width, Height int
	// FirstMs is when the first caption is shown, in thousandths of a
	// second on the clock the file was written with, or -1 if the track
	// has no caption at all.
	FirstMs int64
}

// errNotWhole means a file ends in the middle of something, or does not
// have the shape of a file that was finished.
var errNotWhole = errors.New("the file is cut short")

// ReadPicture reads those facts from a picture subtitle file: a ".sup"
// (disc pictures, PGS), a ".mks" as Extract writes it (DVD or broadcast
// pictures in a Matroska file), or the ".idx" half of a DVD subtitle pair
// (VobSub), which is text and says what the ".sub" beside it holds.
//
// It is not for films: a film in a Matroska file (".mkv") is refused. It
// reads the beginning of a file, as far as the first caption, and gives up
// on a file that has a great deal in it before one.
func ReadPicture(path string) (PictureFacts, error) {
	f, err := os.Open(path)
	if err != nil {
		return PictureFacts{FirstMs: -1}, err
	}
	defer f.Close()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sup":
		return readSUP(f, false)
	case ".mks":
		st, err := f.Stat()
		if err != nil {
			return PictureFacts{FirstMs: -1}, err
		}
		return readMKS(f, st.Size(), false)
	case ".idx":
		// The whole of an .idx is a few hundred short lines.
		b, err := io.ReadAll(io.LimitReader(f, 8<<20))
		if err != nil {
			return PictureFacts{FirstMs: -1}, err
		}
		return readIDX(b), nil
	}
	return PictureFacts{FirstMs: -1}, fmt.Errorf("%q is not a kind of picture subtitle file this program reads", filepath.Ext(path))
}

// readSUP reads a ".sup" file. It is a row of pieces, each with thirteen
// bytes in front: the letters "PG", when the piece is shown and when it is
// decoded (in 90,000ths of a second), its kind and its length. The piece
// that begins a caption says what size of picture the caption is for.
//
// With whole set, the file is read to its end and every piece must be
// complete: a file that ends inside one was cut short.
func readSUP(r io.Reader, whole bool) (PictureFacts, error) {
	facts := PictureFacts{FirstMs: -1}
	br := bufio.NewReaderSize(r, 64<<10)
	var head [13]byte
	for n := 0; ; n++ {
		if _, err := io.ReadFull(br, head[:]); err != nil {
			if err == io.EOF {
				return facts, nil // the file ends between two pieces, as it should
			}
			if err == io.ErrUnexpectedEOF {
				err = errNotWhole
			}
			return facts, err
		}
		if head[0] != 'P' || head[1] != 'G' {
			return facts, errors.New("this is not a disc subtitle file: a piece does not begin with the letters PG")
		}
		kind, size := head[10], int(binary.BigEndian.Uint16(head[11:13]))
		if n == 0 {
			facts.FirstMs = (int64(binary.BigEndian.Uint32(head[2:6])) + 45) / 90
		}
		if kind == 0x16 && facts.Width == 0 && size >= 4 {
			var dims [4]byte
			if _, err := io.ReadFull(br, dims[:]); err != nil {
				return facts, errNotWhole
			}
			facts.Width, facts.Height = int(binary.BigEndian.Uint16(dims[0:2])), int(binary.BigEndian.Uint16(dims[2:4]))
			size -= 4
		}
		if got, err := br.Discard(size); err != nil || got != size {
			return facts, errNotWhole
		}
		// For the facts alone, the first caption's first few pieces are
		// enough; a file that has not said its size by then will not.
		if !whole && (facts.Width > 0 || n > 64) {
			return facts, nil
		}
	}
}

var idxSize = regexp.MustCompile(`(?mi)^[ \t]*size:[ \t]*(\d{1,5})[ \t]*x[ \t]*(\d{1,5})`)
var idxTime = regexp.MustCompile(`(?mi)^[ \t]*timestamp:[ \t]*(\d{1,3}):(\d{2}):(\d{2}):(\d{3})`)

// readIDX reads the text that describes a DVD subtitle track: the ".idx"
// file of a pair, or the same text as a Matroska file keeps it for such a
// track. One line gives the size of picture ("size: 720x576"), and each
// caption has a line with its time ("timestamp: 00:01:02:345, filepos:
// ...").
func readIDX(text []byte) PictureFacts {
	facts := PictureFacts{FirstMs: -1}
	if m := idxSize.FindSubmatch(text); m != nil {
		facts.Width, _ = strconv.Atoi(string(m[1]))
		facts.Height, _ = strconv.Atoi(string(m[2]))
	}
	if m := idxTime.FindSubmatch(text); m != nil {
		var part [4]int64
		for i := range part {
			part[i], _ = strconv.ParseInt(string(m[i+1]), 10, 64)
		}
		facts.FirstMs = ((part[0]*60+part[1])*60+part[2])*1000 + part[3]
	}
	return facts
}

// Element IDs of the Matroska format, as they appear in a file.
const (
	idEBML         = 0x1A45DFA3
	idSegment      = 0x18538067
	idInfo         = 0x1549A966
	idTimeScale    = 0x2AD7B1
	idTracks       = 0x1654AE6B
	idTrackEntry   = 0xAE
	idCodecID      = 0x86
	idCodecPrivate = 0x63A2
	idCluster      = 0x1F43B675
	idClusterTime  = 0xE7
	idSimpleBlock  = 0xA3
	idBlockGroup   = 0xA0
	idBlock        = 0xA1
	idBlockLength  = 0x9B
)

const unknownSize = -1

var errNotMatroska = errors.New("this is not a Matroska file")

// errNoCaptions means a file was given up on: it has more in it than any
// subtitle track has before its first caption.
var errNoCaptions = errors.New("the file holds a great deal and no caption was found in it")

// maxLooked is how many pieces of a Matroska file are looked at for its
// facts before it is given up on. A subtitle track has a few before its
// first caption.
const maxLooked = 100000

// The size broadcast subtitles are made for when they do not say: that of
// a standard-definition TV picture.
const dvbWidth, dvbHeight = 720, 576

// readMKS reads a Matroska file that holds one picture subtitle track, as
// Extract writes them. The file is a tree of elements. Near the top it
// says how long a tick of its clock is and describes its track: for DVD
// pictures that description holds the same text an ".idx" file would, with
// the size in it. The captions follow in groups ("clusters"), each caption
// stamped with its time. Broadcast pictures say their size inside the
// captions themselves, if they say it at all.
//
// With whole set, every element down to the last must be complete, and the
// file must say how long it is: ffmpeg fills that in as the last thing it
// does, so a file without it was never finished.
func readMKS(r io.ReaderAt, length int64, whole bool) (PictureFacts, error) {
	facts := PictureFacts{FirstMs: -1}
	id, size, data, err := ebmlHeader(r, 0)
	if err != nil || id != idEBML || size == unknownSize {
		return facts, errNotMatroska
	}
	id, size, data, err = ebmlHeader(r, data+size)
	if err != nil || id != idSegment {
		return facts, errNotMatroska
	}
	end := length
	switch {
	case size != unknownSize && data+size == length:
	case whole:
		return facts, errNotWhole
	case size != unknownSize && data+size < length:
		end = data + size
	}
	about := mksAbout{scale: 1000000}
	captions := 0
	// looked counts the pieces looked at. A file that must be whole is gone
	// through to its end however much it holds: that is what was asked.
	looked := 0
	tooMany := func() bool {
		looked++
		return !whole && looked > maxLooked
	}
	var saidW, saidH int // the size a broadcast caption gave, if one did
	// enough says the captions need no more looking at: the first has given
	// its time, and the first few have had their chance to give a size.
	enough := func() bool {
		return captions >= 8 || captions > 0 && (saidW > 0 || about.codec != "" && about.codec != "S_DVBSUB")
	}
	caption := func(clusterTime, at, n int64) {
		block := make([]byte, min(n, 64<<10))
		if _, err := r.ReadAt(block, at); err != nil {
			return
		}
		rel, payload, ok := blockParts(block)
		if !ok {
			return
		}
		if captions == 0 {
			facts.FirstMs = (clusterTime + rel) * about.scale / 1000000
		}
		captions++
		if w, h, ok := dvbSize(payload); ok && saidW == 0 {
			saidW, saidH = w, h
		}
	}
top:
	for off := data; off < end; {
		if tooMany() {
			return facts, errNoCaptions
		}
		id, size, body, err := ebmlHeader(r, off)
		if err != nil || size == unknownSize || body+size > end {
			if whole {
				return facts, errNotWhole
			}
			break
		}
		switch id {
		case idInfo, idTracks:
			if size > 4<<20 {
				return facts, errors.New("the Matroska file's description of itself is implausibly large")
			}
			b := make([]byte, size)
			if _, err := r.ReadAt(b, body); err != nil {
				return facts, errNotWhole
			}
			about.read(b)
		case idCluster:
			if enough() {
				if !whole {
					break top
				}
				break // only its place in the file matters now
			}
			var clusterTime int64
			inOrder := true
			err := ebmlWalk(r, body, body+size, func(id uint32, at, n int64) bool {
				if tooMany() {
					return false
				}
				switch id {
				case idClusterTime:
					var b [8]byte
					if n <= 8 {
						if _, err := r.ReadAt(b[:n], at); err == nil {
							clusterTime = int64(ebmlUint(b[:n]))
						}
					}
				case idSimpleBlock:
					caption(clusterTime, at, n)
				case idBlockGroup:
					// A caption that says how long it stays is wrapped in a
					// group, with the length beside it.
					if ebmlWalk(r, at, at+n, func(id uint32, at, n int64) bool {
						if id == idBlock {
							caption(clusterTime, at, n)
							return false
						}
						return !tooMany()
					}) != nil {
						inOrder = false
					}
				}
				return !enough()
			})
			if (err != nil || !inOrder) && whole {
				return facts, errNotWhole
			}
			if !whole && looked > maxLooked {
				return facts, errNoCaptions
			}
		}
		off = body + size
	}
	switch about.codec {
	case "S_VOBSUB":
		described := readIDX(about.private)
		facts.Width, facts.Height = described.Width, described.Height
	case "S_DVBSUB":
		facts.Width, facts.Height = dvbWidth, dvbHeight
		if saidW > 0 {
			facts.Width, facts.Height = saidW, saidH
		}
	case "":
		return facts, errors.New("the Matroska file describes no track")
	}
	return facts, nil
}

// mksAbout is what a Matroska file says about itself near its top.
type mksAbout struct {
	scale   int64  // how many billionths of a second one tick of its clock is
	codec   string // the format of its track (the first, if it has several)
	private []byte // what that format keeps about the track as a whole
}

// read takes in one of the two elements that say these things.
func (a *mksAbout) read(element []byte) {
	ebmlChildren(element, func(id uint32, v []byte) {
		switch id {
		case idTimeScale:
			if n := int64(ebmlUint(v)); n > 0 && n <= 1000000000 {
				a.scale = n
			}
		case idTrackEntry:
			if a.codec != "" {
				return // a second track: these files have one
			}
			ebmlChildren(v, func(id uint32, v []byte) {
				switch id {
				case idCodecID:
					a.codec = string(v)
				case idCodecPrivate:
					a.private = v
				}
			})
		}
	})
}

// blockParts takes a caption's block apart: which track it is for (not
// needed here), its time counted from its cluster's, a byte of marks, and
// the caption itself.
func blockParts(b []byte) (rel int64, payload []byte, ok bool) {
	if len(b) < 4 {
		return 0, nil, false
	}
	n := 1 // the track number says in its first byte how many bytes it takes
	for mask := byte(0x80); n <= 8 && b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 || len(b) < n+3 {
		return 0, nil, false
	}
	return int64(int16(binary.BigEndian.Uint16(b[n : n+2]))), b[n+3:], true
}

// dvbSize looks through one caption of a broadcast subtitle track for the
// piece that says what size of picture the track is for. A caption is a row
// of pieces, each beginning with the byte 0x0F, its kind, a page number and
// its length; kind 0x14 holds the width and height, each less one.
func dvbSize(p []byte) (w, h int, ok bool) {
	if len(p) >= 2 && p[0] == 0x20 && p[1] == 0x00 {
		p = p[2:] // two bytes that some files keep in front, and some do not
	}
	for len(p) >= 6 && p[0] == 0x0F {
		n := int(binary.BigEndian.Uint16(p[4:6]))
		if len(p) < 6+n {
			return 0, 0, false
		}
		if p[1] == 0x14 && n >= 5 {
			return int(binary.BigEndian.Uint16(p[7:9])) + 1, int(binary.BigEndian.Uint16(p[9:11])) + 1, true
		}
		p = p[6+n:]
	}
	return 0, 0, false
}

// ebmlHeader reads one element header at off: its ID, the size of its
// content (unknownSize for "until the end"), and where the content starts.
func ebmlHeader(r io.ReaderAt, off int64) (id uint32, size, data int64, err error) {
	var b [12]byte
	n, err := r.ReadAt(b[:], off)
	if n < 2 {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, 0, 0, err
	}
	// The ID: its first byte says how many bytes it has.
	idLen := 1
	for mask := byte(0x80); idLen <= 4 && b[0]&mask == 0; mask >>= 1 {
		idLen++
	}
	if idLen > 4 || n < idLen+1 {
		return 0, 0, 0, errNotMatroska
	}
	for i := 0; i < idLen; i++ {
		id = id<<8 | uint32(b[i])
	}
	// The size: the same scheme, up to 8 bytes, with the length marker
	// removed from the value.
	s := b[idLen:n]
	sizeLen := 1
	for mask := byte(0x80); sizeLen <= 8 && s[0]&mask == 0; mask >>= 1 {
		sizeLen++
	}
	if sizeLen > 8 || len(s) < sizeLen {
		return 0, 0, 0, errNotMatroska
	}
	v := uint64(s[0]) & (0xFF >> sizeLen)
	ones := v == uint64(0xFF>>sizeLen)
	for i := 1; i < sizeLen; i++ {
		v = v<<8 | uint64(s[i])
		ones = ones && s[i] == 0xFF
	}
	size = int64(v)
	if ones || size < 0 {
		size = unknownSize
	}
	return id, size, off + int64(idLen+sizeLen), nil
}

// ebmlWalk calls fn for each element between from and to, with where its
// content is and how long, until fn returns false. It reads only the
// headers, so a large element costs nothing to pass over.
func ebmlWalk(r io.ReaderAt, from, to int64, fn func(id uint32, at, n int64) bool) error {
	for off := from; off < to; {
		id, size, data, err := ebmlHeader(r, off)
		if err != nil || size == unknownSize || data+size > to {
			return errNotWhole
		}
		if !fn(id, data, size) {
			return nil
		}
		off = data + size
	}
	return nil
}

// ebmlChildren calls fn for each element inside b. It says whether b was
// made of whole elements from its first byte to its last.
func ebmlChildren(b []byte, fn func(id uint32, body []byte)) bool {
	r := byteReaderAt(b)
	for off := int64(0); off < int64(len(b)); {
		id, size, data, err := ebmlHeader(r, off)
		if err != nil || size == unknownSize || data+size > int64(len(b)) {
			return false
		}
		fn(id, b[data:data+size])
		off = data + size
	}
	return true
}

type byteReaderAt []byte

func (b byteReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func ebmlUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}
