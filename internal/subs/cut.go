package subs

import (
	"encoding/binary"
	"io"
	"os"
)

// stopsEarly says whether a film's file ends before the film does: a copy
// or a download that was broken off, or one that is still arriving.
//
// ffmpeg cannot be relied on to notice. It reads such a file as far as it
// goes and ends as if all were well, with a remark about it for some files
// and none for others. But the two kinds of file most films are kept in
// say of themselves how long they are, at their beginning, and that is
// compared here with how long the file really is.
//
// A file that does not say, or is of another kind, is not judged here.
func stopsEarly(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false // ffprobe will say what is wrong with it
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	return saysMoreThan(f, st.Size())
}

// saysMoreThan says whether a file claims to hold more than length bytes.
func saysMoreThan(r io.ReaderAt, length int64) bool {
	var head [8]byte
	if n, _ := r.ReadAt(head[:], 0); n < len(head) {
		return false
	}
	if binary.BigEndian.Uint32(head[:4]) == idEBML {
		// MKV and WebM: after a short heading, one element holds the whole
		// film and says its size (unless the film was written by a program
		// that could not go back to fill it in, as when recording).
		id, size, data, err := ebmlHeader(r, 0)
		if err != nil || id != idEBML || size == unknownSize {
			return false
		}
		id, size, data, err = ebmlHeader(r, data+size)
		return err == nil && id == idSegment && size != unknownSize && size > length-data
	}
	// MP4 and QuickTime: a row of boxes, each beginning with its size and
	// a name of four letters.
	switch string(head[4:8]) {
	case "ftyp", "styp", "moov", "mdat", "free", "skip", "wide", "pnot":
	default:
		return false
	}
	// (A film cut into pieces for sending has two boxes for every few
	// seconds; a million is far more than any has.)
	for off, n := int64(0), 0; off < length && n < 1<<20; n++ {
		var b [16]byte
		got, _ := r.ReadAt(b[:], off)
		if got < 8 {
			return false // a few stray bytes at the end
		}
		for _, c := range b[4:8] {
			if c < 0x20 || c > 0x7E {
				return false // not a box: this is not the kind of file it seemed
			}
		}
		size := int64(binary.BigEndian.Uint32(b[:4]))
		switch {
		case size == 0:
			return false // the last box, which runs to wherever the file ends
		case size == 1:
			// A box too large for four bytes gives its size in the eight
			// that follow its name.
			if size = int64(binary.BigEndian.Uint64(b[8:16])); got < 16 || size < 16 {
				return false
			}
		case size < 8:
			return false
		}
		if size > length-off {
			return true
		}
		off += size
	}
	return false
}
