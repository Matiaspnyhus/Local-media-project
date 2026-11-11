package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/library"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

// TestBigLibrary builds a library of empty files laid out like a real one
// and holds the plan's speed targets on it. By default it uses 3,000 files
// so it runs with every test; LM_BIG=50000 runs the full size:
//
//	LM_BIG=50000 go test ./internal/server -run TestBigLibrary -v -timeout 30m
func TestBigLibrary(t *testing.T) {
	total := 3000
	if n, err := strconv.Atoi(os.Getenv("LM_BIG")); err == nil && n > 0 {
		total = n
	}
	e := newEnv(t)
	films := total / 25
	shows := (total - films) / 120 // six seasons of twenty
	made := 0
	for i := 0; i < films; i++ {
		dir := filepath.Join(e.media, "Films", fmt.Sprintf("Film Number %d (%d)", i, 1950+i%75))
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("Film Number %d (%d).mkv", i, 1950+i%75)), nil, 0o644)
		made++
	}
	for s := 0; s < shows; s++ {
		for season := 1; season <= 6; season++ {
			dir := filepath.Join(e.media, "TV", fmt.Sprintf("Show Number %d", s), fmt.Sprintf("Season %02d", season))
			os.MkdirAll(dir, 0o755)
			for ep := 1; ep <= 20; ep++ {
				os.WriteFile(filepath.Join(dir, fmt.Sprintf("Show Number %d - S%02dE%02d.mkv", s, season, ep)), nil, 0o644)
				made++
			}
		}
	}
	// Scaled to the plan's targets for 50,000 files: first scan under ten
	// minutes, an unchanged rescan under one, the first screen under 100 ms.
	scale := float64(made) / 50000
	if scale < 0.2 {
		scale = 0.2 // small runs get slack for fixed costs
	}

	start := time.Now()
	c := e.setup() // setup runs the first scan
	first := time.Since(start)
	var libID string
	e.s.db.QueryRow(`SELECT id FROM libraries`).Scan(&libID)
	start = time.Now()
	res, err := library.Scan(e.s.db, libID)
	rescan := time.Since(start)
	if err != nil || res.Added != 0 || res.Total != made+2 {
		t.Fatalf("rescan: %+v %v (made %d)", res, err, made)
	}
	// As if every file had been inspected: an MKV with H.264 and AAC.
	e.s.db.Exec(`UPDATE files SET probed=1, container='matroska', duration_ms=2700000`)
	e.s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,bit_depth,is_default) SELECT id,0,'video','h264',8,1 FROM files`)
	e.s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,is_default) SELECT id,1,'audio','aac',1 FROM files`)
	e.do("POST", "/api/capabilities", remote, c, map[string]any{
		"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}})
	// Someone part-way through twenty shows.
	rows, _ := e.s.db.Query(`SELECT e.id FROM items e WHERE e.kind='episode' AND e.season_number=2 AND e.episode_number=5 LIMIT 20`)
	var watched []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		watched = append(watched, id)
	}
	rows.Close()
	for _, id := range watched {
		e.s.db.Exec(`INSERT INTO progress(user_id,item_id,position_ms,duration_ms,played,updated_at) SELECT id,?,2600000,2700000,1,? FROM users`, id, time.Now().Unix())
	}

	p95 := func(target string, n int) time.Duration {
		var times []time.Duration
		for i := 0; i < n; i++ {
			start := time.Now()
			if w := e.do("GET", target, remote, c, nil); w.Code != 200 {
				t.Fatalf("%s: %d %s", target, w.Code, w.Body)
			}
			times = append(times, time.Since(start))
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		return times[len(times)*95/100]
	}
	var showID string
	e.s.db.QueryRow(`SELECT id FROM items WHERE kind='show' LIMIT 1`).Scan(&showID)
	home, movies, showList := p95("/api/home", 40), p95("/api/movies", 10), p95("/api/shows", 10)
	show, search := p95("/api/shows/"+showID, 20), p95("/api/search?q=number+12", 20)
	t.Logf("%d files: first scan %v, unchanged rescan %v", made, first.Round(time.Millisecond), rescan.Round(time.Millisecond))
	t.Logf("95th percentile: first screen %v, all films %v, all shows %v, one show %v, search %v",
		home.Round(time.Millisecond), movies.Round(time.Millisecond), showList.Round(time.Millisecond), show.Round(time.Millisecond), search.Round(time.Millisecond))

	limit := func(d time.Duration) time.Duration { return time.Duration(float64(d) * scale) }
	if first > limit(10*time.Minute) {
		t.Errorf("first scan took %v, over the target of %v for this size", first, limit(10*time.Minute))
	}
	if rescan > limit(time.Minute) {
		t.Errorf("rescan took %v, over the target of %v for this size", rescan, limit(time.Minute))
	}
	if home > 100*time.Millisecond {
		t.Errorf("the first screen took %v at the 95th percentile, over 100 ms", home)
	}
	if show > 100*time.Millisecond || search > 150*time.Millisecond {
		t.Errorf("a show page took %v and a search %v", show, search)
	}
	var next homeOut
	decode(t, e.do("GET", "/api/home", remote, c, nil), &next)
	if len(next.NextUp) != len(watched) {
		t.Errorf("next up lists %d, want %d", len(next.NextUp), len(watched))
	}
}

// TestInspectionRate measures how fast real files are inspected, which is
// the other part of a first scan. It needs ffprobe and runs only when asked:
//
//	LM_BIG=1 go test ./internal/server -run TestInspectionRate -v
func TestInspectionRate(t *testing.T) {
	ff := probe.Find()
	if ff == "" || os.Getenv("LM_BIG") == "" {
		t.Skip("needs ffprobe and LM_BIG")
	}
	e := newEnv(t)
	src, _ := os.ReadFile(filepath.Join("..", "..", "e2e", "fixtures", "Test Clip.mp4"))
	const n = 400
	for i := 0; i < n; i++ {
		os.WriteFile(filepath.Join(e.media, fmt.Sprintf("Clip Number %d (2001).mp4", i)), src, 0o644)
	}
	e.setup()
	start := time.Now()
	res, err := library.ProbePending(context.Background(), e.s.db, ff)
	took := time.Since(start)
	if err != nil || res.Inspected < n {
		t.Fatalf("%+v %v", res, err)
	}
	per := took / time.Duration(res.Inspected)
	t.Logf("inspected %d files in %v: %v each, so 50,000 would take about %v", res.Inspected, took.Round(time.Millisecond), per.Round(time.Millisecond), (per * 50000).Round(time.Second))
	_ = io.Discard
}
