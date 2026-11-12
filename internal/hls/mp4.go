package hls

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// The little of the MP4 format this package needs: enough to tell the
// stream's header from its fragments, and to read when the picture in a
// fragment starts.

// boxHeader reads one box header. raw is the header exactly as read, and
// payload how many bytes of the box follow it.
func boxHeader(r *bufio.Reader) (typ string, raw []byte, payload int64, err error) {
	raw = make([]byte, 8, 16)
	if _, err = io.ReadFull(r, raw); err != nil {
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		return
	}
	size := int64(binary.BigEndian.Uint32(raw[:4]))
	typ = string(raw[4:8])
	if size == 1 {
		raw = raw[:16]
		if _, err = io.ReadFull(r, raw[8:]); err != nil {
			return
		}
		size = int64(binary.BigEndian.Uint64(raw[8:]))
	}
	if size < int64(len(raw)) {
		err = errors.New("the converter's output is not in the expected form")
		return
	}
	return typ, raw, size - int64(len(raw)), nil
}

// children calls fn for each box directly inside body.
func children(body []byte, fn func(typ string, body []byte)) {
	for len(body) >= 8 {
		size := int(binary.BigEndian.Uint32(body[:4]))
		if size < 8 || size > len(body) {
			return
		}
		fn(string(body[4:8]), body[8:size])
		body = body[size:]
	}
}

// videoTrack is the picture track's number and its clock's ticks per second.
type videoTrack struct {
	id    uint32
	scale uint32
	// edit is the tick of the track's clock that the header says is shown
	// first (its "edit list"). A frame is stored with the time it is decoded
	// at and how long after that it is shown; taking edit away from their
	// sum gives the time it is shown at.
	edit int64
}

// findVideo reads the header (the body of a moov box) for the picture track.
func findVideo(moov []byte) videoTrack {
	v := videoTrack{id: 1, scale: 0}
	children(moov, func(typ string, trak []byte) {
		if typ != "trak" || v.scale != 0 {
			return
		}
		var id, scale uint32
		var edit int64
		var handler string
		children(trak, func(typ string, b []byte) {
			switch typ {
			case "tkhd":
				if len(b) >= 24 && b[0] == 1 {
					id = binary.BigEndian.Uint32(b[20:24])
				} else if len(b) >= 16 {
					id = binary.BigEndian.Uint32(b[12:16])
				}
			case "edts":
				children(b, func(typ string, b []byte) {
					if typ == "elst" {
						edit = editStart(b)
					}
				})
			case "mdia":
				children(b, func(typ string, b []byte) {
					switch typ {
					case "mdhd":
						if len(b) >= 24 && b[0] == 1 {
							scale = binary.BigEndian.Uint32(b[20:24])
						} else if len(b) >= 16 {
							scale = binary.BigEndian.Uint32(b[12:16])
						}
					case "hdlr":
						if len(b) >= 12 {
							handler = string(b[8:12])
						}
					}
				})
			}
		})
		if handler == "vide" && scale > 0 {
			v = videoTrack{id: id, scale: scale, edit: edit}
		}
	})
	if v.scale == 0 {
		v.scale = 1000
	}
	return v
}

// editStart reads an edit list (the body of an elst box) for the tick of
// the track's clock that is shown first.
func editStart(b []byte) int64 {
	if len(b) < 8 {
		return 0
	}
	// Each entry is a length and a starting tick, in 32 or 64 bits, and a
	// speed. A starting tick of -1 marks a pause, not a place in the track.
	size := 12
	if b[0] == 1 {
		size = 20
	}
	n := binary.BigEndian.Uint32(b[4:8])
	for e := b[8:]; n > 0 && len(e) >= size; n, e = n-1, e[size:] {
		t := int64(int32(binary.BigEndian.Uint32(e[4:8])))
		if b[0] == 1 {
			t = int64(binary.BigEndian.Uint64(e[8:16]))
		}
		if t >= 0 {
			return t
		}
	}
	return 0
}

// fragmentStart returns when the key frame that opens a fragment (the body
// of a moof box) is shown, in milliseconds on the converter's clock.
//
// That is not the time the fragment is stamped with, which is when the key
// frame is decoded: frames are stored in the order they are decoded, and how
// long after that a key frame is shown differs from one key frame to the
// next in films whose key frames are followed by frames shown before them,
// and in films with an uneven frame rate. The cuts are times a key frame is
// shown at, so that is the time a fragment is filed by.
func fragmentStart(moof []byte, v videoTrack) (ms float64, ok bool) {
	children(moof, func(typ string, traf []byte) {
		if typ != "traf" || ok {
			return
		}
		var id uint32
		var t uint64
		var have, run bool
		var later int64
		children(traf, func(typ string, b []byte) {
			switch typ {
			case "tfhd":
				if len(b) >= 8 {
					id = binary.BigEndian.Uint32(b[4:8])
				}
			case "tfdt":
				if len(b) >= 12 && b[0] == 1 {
					t, have = binary.BigEndian.Uint64(b[4:12]), true
				} else if len(b) >= 8 {
					t, have = uint64(binary.BigEndian.Uint32(b[4:8])), true
				}
			case "trun":
				if !run {
					later, run = firstShownAfter(b), true
				}
			}
		})
		if have && id == v.id {
			ms, ok = float64(int64(t)+later-v.edit)*1000/float64(v.scale), true
		}
	})
	return
}

// firstShownAfter reads a list of frames (the body of a trun box) for how
// many ticks after it is decoded the first frame is shown.
func firstShownAfter(b []byte) int64 {
	if len(b) < 8 || binary.BigEndian.Uint32(b[4:8]) == 0 {
		return 0
	}
	flags := binary.BigEndian.Uint32(b[0:4]) & 0xffffff
	if flags&0x800 == 0 {
		return 0 // every frame is shown when it is decoded
	}
	// The value wanted is the last of up to four given for the first frame,
	// which follow up to two given for the list as a whole. Flags say which
	// are present.
	at := 8
	for _, present := range []uint32{0x1, 0x4, 0x100, 0x200, 0x400} {
		if flags&present != 0 {
			at += 4
		}
	}
	if len(b) < at+4 {
		return 0
	}
	if b[0] == 0 {
		return int64(binary.BigEndian.Uint32(b[at : at+4]))
	}
	return int64(int32(binary.BigEndian.Uint32(b[at : at+4])))
}
