package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// A program that is ended by force (the installer does that, and so does a
// power cut) cannot tidy up after itself. What it leaves in the data
// folder's cache, the chunks of the films that were open and a subtitle
// half written, must be gone after the next start: also on a PC where the
// converter is no longer found, which used to be the one thing that
// emptied the chunk folder. Everything else in the data folder stays.
func TestAStartRemovesWhatAForcedEndLeftBehind(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ffmpeg func(t *testing.T) string
	}{
		{"no converter is found", func(*testing.T) string { return "" }},
		{"the converter is found", func(t *testing.T) string {
			ffmpeg := probe.FindFFmpeg()
			if ffmpeg == "" {
				if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
					t.Fatal("ffmpeg was not found, and LM_REQUIRE_FFMPEG says it must be")
				}
				t.Skip("ffmpeg is not installed here")
			}
			return ffmpeg
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ffmpeg := tc.ffmpeg(t)
			data := t.TempDir()
			write := func(rel string) {
				t.Helper()
				p := filepath.Join(data, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("left behind"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// What a forced end leaves.
			write("cache/chunks/0123456789abcdef0123456789abcdef/1/init.mp4")
			write("cache/chunks/0123456789abcdef0123456789abcdef/1/seg-0.m4s")
			write("cache/chunks/0123456789abcdef0123456789abcdef/1/seg-1.m4s")
			write("cache/subtitles/making-2011847355.tmp")
			write("cache/subtitles/making-7.tmp")
			// What must stay: kept subtitles, and anything that merely looks
			// a little like a leftover or lies somewhere else.
			keep := []string{
				"cache/subtitles/0a1b2c3d-2.vtt",
				"cache/subtitles/making-of-a-film.vtt",
				"cache/subtitles/notes.tmp",
				"cache/subtitles/making-notes.tmp",
				"cache/subtitles/making-.tmp",
				"cache/subtitles/making-12.tmp.vtt",
				"cache/subtitles/making-a-folder.tmp/inside.txt",
				"cache/making-1.tmp",
				"cache/other/chunks/seg-0.m4s",
				"making-1.tmp",
				"chunks/seg-0.m4s",
				"artwork/poster.jpg",
				"backups/localmedia-schema6-20261112-101010.db",
			}
			for _, rel := range keep {
				write(rel)
			}

			d, err := db.Open(data)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			s, err := New(Config{DB: d, DataDir: data, Version: "test", Port: 8177, Assets: fstest.MapFS{}, FFmpeg: ffmpeg})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()

			var left []string
			filepath.Walk(filepath.Join(data, "cache"), func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					rel, _ := filepath.Rel(data, p)
					left = append(left, filepath.ToSlash(rel))
				}
				return nil
			})
			sort.Strings(left)
			var want []string
			for _, rel := range keep {
				if strings.HasPrefix(rel, "cache/") {
					want = append(want, rel)
				}
			}
			sort.Strings(want)
			if strings.Join(left, "\n") != strings.Join(want, "\n") {
				t.Errorf("after a start the cache holds:\n  %s\nwant only what is meant to be kept:\n  %s", strings.Join(left, "\n  "), strings.Join(want, "\n  "))
			}
			for _, rel := range keep {
				if _, err := os.Stat(filepath.Join(data, filepath.FromSlash(rel))); err != nil {
					t.Errorf("the start removed %s, which is not a leftover", rel)
				}
			}
		})
	}
}

// The tidying is told the data folder and nothing else, and must not be
// led out of it by a link, wherever in the path the link is.
func TestTidyingNeverLeavesTheDataFolder(t *testing.T) {
	outside := func(t *testing.T) (dir string, check func()) {
		dir = t.TempDir()
		var files []string
		for _, rel := range []string{"holiday.mkv", "chunks/holiday.mkv", "subtitles/making-1.tmp"} {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("not ours"), 0o600); err != nil {
				t.Fatal(err)
			}
			files = append(files, p)
		}
		return dir, func() {
			t.Helper()
			for _, p := range files {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("tidying the cache deleted a file outside the data folder: %v", err)
				}
			}
		}
	}
	link := func(t *testing.T, to, at string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, at); err != nil {
			t.Skipf("links cannot be made here: %v", err)
		}
	}
	t.Run("the chunk folder is a link", func(t *testing.T) {
		data := t.TempDir()
		elsewhere, check := outside(t)
		link(t, elsewhere, filepath.Join(data, "cache", "chunks"))
		removeLeftovers(data)
		check()
	})
	t.Run("the subtitle folder is a link", func(t *testing.T) {
		data := t.TempDir()
		elsewhere, check := outside(t)
		link(t, filepath.Join(elsewhere, "subtitles"), filepath.Join(data, "cache", "subtitles"))
		removeLeftovers(data)
		check()
	})
	t.Run("the cache folder is a link", func(t *testing.T) {
		data := t.TempDir()
		elsewhere, check := outside(t)
		link(t, elsewhere, filepath.Join(data, "cache"))
		removeLeftovers(data)
		check()
	})
}
