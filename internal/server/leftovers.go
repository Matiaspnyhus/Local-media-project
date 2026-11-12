package server

import (
	"os"
	"path/filepath"
	"strings"
)

// removeLeftovers deletes what an earlier run of the program left in the
// data folder's cache because it was ended by force (the installer does
// that, and so does a power cut or a crash) and so never tidied up:
//
//   - cache/chunks, the converted pieces of the films that were open. They
//     can be hundreds of megabytes, and nothing can ever ask for them again:
//     their addresses died with the run that made them.
//   - cache/subtitles/making-<digits>.tmp, a subtitle that was being
//     written down when the program ended.
//
// It runs at every start, whether or not a converter is found: the chunk
// folder used to be emptied only by the converter's own start, so on a PC
// where ffmpeg had gone missing it stayed for good.
//
// Only these two exact things are removed, and only inside the data
// folder: it is opened as a root, which refuses to follow a link out of
// it. A chunk folder that is itself a link loses the link, never what the
// link points at. Nothing here is worth stopping the start for, so every
// failure is passed over.
func removeLeftovers(dataDir string) {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return
	}
	defer root.Close()
	root.RemoveAll(filepath.Join("cache", "chunks"))

	subtitles := filepath.Join("cache", "subtitles")
	dir, err := root.Open(subtitles)
	if err != nil {
		return
	}
	entries, _ := dir.ReadDir(-1)
	dir.Close()
	for _, e := range entries {
		if e.Type().IsRegular() && isSubtitleLeftover(e.Name()) {
			root.Remove(filepath.Join(subtitles, e.Name()))
		}
	}
}

// isSubtitleLeftover reports whether a file name is one that keepSubtitle
// gives the file it writes before renaming it: "making-", a number, ".tmp".
func isSubtitleLeftover(name string) bool {
	number, ok := strings.CutPrefix(name, "making-")
	if !ok {
		return false
	}
	if number, ok = strings.CutSuffix(number, ".tmp"); !ok || number == "" {
		return false
	}
	return strings.Trim(number, "0123456789") == ""
}
