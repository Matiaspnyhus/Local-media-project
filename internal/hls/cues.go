package hls

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sort"
)

// Most films in a home library are MKV files, and an MKV file carries its
// own list of key frames (its "Cues"), which players use to jump around.
// Reading that list directly takes one small read, where asking ffprobe to
// visit the film every few seconds takes a disk seek per visit: seconds, on
// a hard disk, the first time each film is played.

var errNoCues = errors.New("no usable key-frame list in this file")

// Element IDs of the MKV format, as they appear in a file.
const (
	idEBML        = 0x1A45DFA3
	idSegment     = 0x18538067
	idSeekHead    = 0x114D9B74
	idSeek        = 0x4DBB
	idSeekID      = 0x53AB
	idSeekPos     = 0x53AC
	idInfo        = 0x1549A966
	idTimeScale   = 0x2AD7B1
	idTracks      = 0x1654AE6B
	idTrackEntry  = 0xAE
	idTrackNumber = 0xD7
	idTrackType   = 0x83
	idCluster     = 0x1F43B675
	idCues        = 0x1C53BB6B
	idCuePoint    = 0xBB
	idCueTime     = 0xB3
	idCuePosition = 0xB7
	idCueTrack    = 0xF7
)

const unknownSize = -1

// ebmlHeader reads one element header at off: its ID, the size of its
// content (unknownSize for "until the end"), and where the content starts.
func ebmlHeader(r io.ReaderAt, off int64) (id uint32, size, data int64, err error) {
	var b [12]byte
	n, err := r.ReadAt(b[:], off)
	if n < 2 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return
	}
	// The ID: its first byte says how many bytes it has.
	idLen := 1
	for mask := byte(0x80); idLen <= 4 && b[0]&mask == 0; mask >>= 1 {
		idLen++
	}
	if idLen > 4 || n < idLen+1 {
		return 0, 0, 0, errNoCues
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
		return 0, 0, 0, errNoCues
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

// ebmlChildren calls fn for each element inside b.
func ebmlChildren(b []byte, fn func(id uint32, body []byte)) {
	r := byteReaderAt(b)
	for off := int64(0); off < int64(len(b)); {
		id, size, data, err := ebmlHeader(r, off)
		if err != nil || size == unknownSize || data+size > int64(len(b)) {
			return
		}
		fn(id, b[data:data+size])
		off = data + size
	}
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

// matroskaKeyFrames returns the times, in milliseconds, of the picture's
// key frames as the file's own index lists them.
func matroskaKeyFrames(path string) ([]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	id, size, data, err := ebmlHeader(f, 0)
	if err != nil || id != idEBML || size == unknownSize {
		return nil, errNoCues
	}
	// The Segment holds everything else.
	off := data + size
	id, size, seg, err := ebmlHeader(f, off)
	if err != nil || id != idSegment {
		return nil, errNoCues
	}
	end := st.Size()
	if size != unknownSize && seg+size < end {
		end = seg + size
	}
	read := func(at, n int64) []byte {
		if n <= 0 || n > 64<<20 || at+n > st.Size() {
			return nil
		}
		b := make([]byte, n)
		if _, err := f.ReadAt(b, at); err != nil {
			return nil
		}
		return b
	}
	var info, tracks, cues []byte
	where := map[uint32]int64{} // from the file's table of contents
	// Walk the start of the file: the table of contents, the description
	// and the track list come first; the first Cluster is where the film
	// itself begins and the walk ends.
	off = seg
walk:
	for i := 0; i < 64 && off < end; i++ {
		id, size, data, err := ebmlHeader(f, off)
		if err != nil || size == unknownSize {
			break
		}
		switch id {
		case idCluster:
			break walk
		case idSeekHead:
			ebmlChildren(read(data, size), func(id uint32, seek []byte) {
				if id != idSeek {
					return
				}
				var what uint32
				pos := int64(-1)
				ebmlChildren(seek, func(id uint32, b []byte) {
					switch id {
					case idSeekID:
						what = uint32(ebmlUint(b))
					case idSeekPos:
						pos = int64(ebmlUint(b))
					}
				})
				if pos >= 0 {
					where[what] = seg + pos
				}
			})
		case idInfo:
			info = read(data, size)
		case idTracks:
			tracks = read(data, size)
		case idCues:
			cues = read(data, size)
		}
		off = data + size
	}
	fetch := func(want uint32, have []byte) []byte {
		if have != nil {
			return have
		}
		at, ok := where[want]
		if !ok {
			return nil
		}
		id, size, data, err := ebmlHeader(f, at)
		if err != nil || id != want || size == unknownSize {
			return nil
		}
		return read(data, size)
	}
	info, tracks, cues = fetch(idInfo, info), fetch(idTracks, tracks), fetch(idCues, cues)
	if tracks == nil || cues == nil {
		return nil, errNoCues
	}
	scale := uint64(1000000) // nanoseconds per tick, unless the file says otherwise
	ebmlChildren(info, func(id uint32, b []byte) {
		if id == idTimeScale && ebmlUint(b) > 0 {
			scale = ebmlUint(b)
		}
	})
	video := uint64(0)
	ebmlChildren(tracks, func(id uint32, entry []byte) {
		if id != idTrackEntry || video != 0 {
			return
		}
		var number, kind uint64
		ebmlChildren(entry, func(id uint32, b []byte) {
			switch id {
			case idTrackNumber:
				number = ebmlUint(b)
			case idTrackType:
				kind = ebmlUint(b)
			}
		})
		if kind == 1 {
			video = number
		}
	})
	if video == 0 {
		return nil, errNoCues
	}
	var keys []int64
	ebmlChildren(cues, func(id uint32, point []byte) {
		if id != idCuePoint {
			return
		}
		t, mine, timed := uint64(0), false, false
		ebmlChildren(point, func(id uint32, b []byte) {
			switch id {
			case idCueTime:
				t, timed = ebmlUint(b), true
			case idCuePosition:
				ebmlChildren(b, func(id uint32, b []byte) {
					if id == idCueTrack && ebmlUint(b) == video {
						mine = true
					}
				})
			}
		})
		if mine && timed {
			// Ticks to milliseconds, rounded to the nearest.
			keys = append(keys, int64((t*scale+500000)/1000000))
		}
	})
	if len(keys) < 2 {
		return nil, errNoCues
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys, nil
}

// isMatroska reports whether the file starts the way MKV and WebM files do.
func isMatroska(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [4]byte
	_, err = io.ReadFull(f, b[:])
	return err == nil && binary.BigEndian.Uint32(b[:]) == idEBML
}
