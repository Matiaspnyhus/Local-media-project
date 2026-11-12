package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// The program is easy to start twice: it starts with Windows and sits among
// the hidden icons, and the Start-menu entry starts it again. The second
// copy must notice the first and stop before it touches anything. It used
// to empty the folder the running copy keeps its converted chunks in
// first, which stalled whatever was being watched.
func TestASecondCopyStopsBeforeItTouchesAnything(t *testing.T) {
	if probe.FindFFmpeg() == "" {
		// Without a converter there is no chunk folder to protect.
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg was not found, and LM_REQUIRE_FFMPEG says it must be")
		}
		t.Skip("ffmpeg is not installed here")
	}
	data := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	opts := options{dataDir: data, port: port, noBrowser: true, noTray: true, noUpdate: true}

	first := &app{opts: opts, quit: make(chan struct{}), ready: make(chan struct{})}
	stopped := make(chan error, 1)
	go func() { stopped <- first.run() }()
	select {
	case <-first.ready:
	case err := <-stopped:
		t.Fatalf("the first copy did not start: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the first copy did not start in 30 seconds")
	}
	defer func() {
		first.stop()
		<-stopped
	}()

	// The first copy is in the middle of a film: its chunks are on disk.
	chunk := filepath.Join(data, "cache", "chunks", "a-film-being-watched", "run-1", "seg-0.m4s")
	if err := os.MkdirAll(filepath.Dir(chunk), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chunk, []byte("a chunk"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, data)

	second := &app{opts: opts, quit: make(chan struct{}), ready: make(chan struct{})}
	err = second.run()
	if err == nil || !strings.Contains(err.Error(), "already running") || !strings.Contains(err.Error(), fmt.Sprint(port)) {
		t.Fatalf("the second copy said %v; want the message that Local Media is already running on port %d", err, port)
	}
	if _, err := os.Stat(chunk); err != nil {
		t.Errorf("the second copy deleted the running copy's chunks: %v", err)
	}
	if after := snapshot(t, data); after != before {
		t.Errorf("the second copy changed the data folder.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("the first copy stopped answering: %v", err)
	}
	res.Body.Close()
}

// snapshot lists every file in a folder with its size, apart from the
// database's own working files and the log, which the running copy writes
// to as it goes.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if strings.HasPrefix(rel, "logs") || strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") {
			return nil
		}
		if info.IsDir() {
			fmt.Fprintf(&b, "%s/\n", filepath.ToSlash(rel))
		} else {
			fmt.Fprintf(&b, "%s %d\n", filepath.ToSlash(rel), info.Size())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
