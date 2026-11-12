package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
)

// script writes a small program for the test to use in place of ffprobe or
// ffmpeg. It skips the test on Windows, which has no /bin/sh.
func script(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this test stands a shell script in for a helper program")
	}
	p := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// longest is the longest chunk of a grid, in milliseconds.
func longest(g hls.Grid) int64 {
	var most int64
	for i, at := range g.Cuts {
		end := g.EndMs
		if i+1 < len(g.Cuts) {
			end = g.Cuts[i+1]
		}
		most = max(most, end-at)
	}
	return most
}

// A list of cuts that makes one chunk of a whole film is worse than none:
// the film would not start until the converter had worked through all of
// it. Such a list must not be used, whether it was just worked out or was
// stored by an earlier run, and must not be stored.
func TestAListOfCutsWithAbsurdlyLongChunksIsNotUsedOrKept(t *testing.T) {
	e := filmWith(t, []string{`0,'video','h264',1,0,0,0,'',''`, `1,'audio','ac3',1,0,0,0,'eng',''`}, nil)
	const size, mtime, duration = 1, 5, 720_005 // a 12-minute film
	path := filepath.Join(e.media, "Films", "Track Film (2019)", "Track Film (2019).mkv")
	kept := func() (grid string) {
		e.s.db.QueryRow(`SELECT grid FROM cuts WHERE file_id=?`, e.fileID).Scan(&grid)
		return grid
	}

	// 1. An earlier run found no key frames and stored one 12-minute chunk.
	bad := `{"start":0,"end":720005,"cuts":[0]}`
	if _, err := e.s.db.Exec(`INSERT INTO cuts(file_id,stamp,grid) VALUES(?,?,?)`, e.fileID, fmt.Sprintf("%d-%d-%d", size, mtime, duration), bad); err != nil {
		t.Fatal(err)
	}
	g := e.s.grid(context.Background(), e.fileID, path, size, mtime, duration)
	if !g.Valid() || longest(g) > 60_000 {
		t.Fatalf("the stored list was used: %d chunks, the longest %d s", len(g.Cuts), longest(g)/1000)
	}

	// 2. The key-frame search itself comes back with nothing (this stand-in
	// for ffprobe prints nothing and reports success, as the real one did
	// for whole kinds of file). The result must be neither used nor stored.
	e.s.db.Exec(`DELETE FROM cuts`)
	e.s.ffprobe = script(t, "exit 0")
	g = e.s.grid(context.Background(), e.fileID, path, size, mtime, duration)
	if !g.Valid() || longest(g) > 60_000 {
		t.Fatalf("a search that found nothing was believed: %d chunks, the longest %d s", len(g.Cuts), longest(g)/1000)
	}
	if k := kept(); k != "" {
		var stored hls.Grid
		if err := json.Unmarshal([]byte(k), &stored); err != nil || longest(stored) > 60_000 {
			t.Fatalf("a list with a %d s chunk was stored: %s", longest(stored)/1000, k)
		}
	}

	// A sensible list is still used as it is.
	good := hls.Grid{StartMs: 0, EndMs: duration, Cuts: []int64{0}}
	for at := int64(7000); at < duration-1000; at += 7000 {
		good.Cuts = append(good.Cuts, at)
	}
	text, _ := json.Marshal(good)
	e.s.db.Exec(`DELETE FROM cuts`)
	e.s.db.Exec(`INSERT INTO cuts(file_id,stamp,grid) VALUES(?,?,?)`, e.fileID, fmt.Sprintf("%d-%d-%d", size, mtime, duration), string(text))
	if g = e.s.grid(context.Background(), e.fileID, path, size, mtime, duration); len(g.Cuts) != len(good.Cuts) || g.Cuts[1] != 7000 {
		t.Fatalf("a sensible stored list was not used: %d chunks", len(g.Cuts))
	}
}
