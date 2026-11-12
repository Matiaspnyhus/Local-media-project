package hls

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// The little of the MP4 format this package needs: enough to tell the
// stream's header from its fragments, and to read when a fragment starts.

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
}

// findVideo reads the header (the body of a moov box) for the picture track.
func findVideo(moov []byte) videoTrack {
	v := videoTrack{id: 1, scale: 0}
	children(moov, func(typ string, trak []byte) {
		if typ != "trak" || v.scale != 0 {
			return
		}
		var id, scale uint32
		var handler string
		children(trak, func(typ string, b []byte) {
			switch typ {
			case "tkhd":
				if len(b) >= 24 && b[0] == 1 {
					id = binary.BigEndian.Uint32(b[20:24])
				} else if len(b) >= 16 {
					id = binary.BigEndian.Uint32(b[12:16])
				}
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
			v = videoTrack{id: id, scale: scale}
		}
	})
	if v.scale == 0 {
		v.scale = 1000
	}
	return v
}

// fragmentStart returns when the picture in a fragment (the body of a moof
// box) starts, in milliseconds on the converter's clock.
func fragmentStart(moof []byte, v videoTrack) (ms float64, ok bool) {
	children(moof, func(typ string, traf []byte) {
		if typ != "traf" || ok {
			return
		}
		var id uint32
		var t uint64
		var have bool
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
			}
		})
		if have && id == v.id {
			ms, ok = float64(int64(t))*1000/float64(v.scale), true
		}
	})
	return
}
