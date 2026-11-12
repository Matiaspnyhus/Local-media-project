package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// showEnv is a library with one show of two seasons and a special.
func showEnv(t *testing.T) (*env, *http.Cookie) {
	e := newEnv(t)
	for _, rel := range []string{
		"TV/Good Show/Season 01/Good Show - S01E01.mkv", "TV/Good Show/Season 01/Good Show - S01E02.mkv",
		"TV/Good Show/Season 01/Good Show - S01E03.mkv", "TV/Good Show/Season 02/Good Show - S02E01.mkv",
		"TV/Good Show/Specials/Good Show - S00E01.mkv", "TV/Other Show/Season 01/Other Show - S01E01.mkv",
		"Films/Amélie (2001)/Amélie (2001).mkv",
	} {
		p := filepath.Join(e.media, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(rel), 0o644)
	}
	return e, e.setup()
}

type homeOut struct {
	Continue []itemView `json:"continue"`
	NextUp   []itemView `json:"nextUp"`
	Counts   map[string]int
}

func (e *env) episode(c *http.Cookie, show string, season, episode int) itemView {
	e.t.Helper()
	var shows struct{ Shows []showView }
	decode(e.t, e.do("GET", "/api/shows", remote, c, nil), &shows)
	for _, sh := range shows.Shows {
		if sh.Title != show {
			continue
		}
		var out struct {
			Seasons []struct {
				Number   int
				Episodes []itemView
			}
		}
		decode(e.t, e.do("GET", "/api/shows/"+sh.ID, remote, c, nil), &out)
		for _, se := range out.Seasons {
			for _, ep := range se.Episodes {
				if ep.Season == season && ep.Episode == episode {
					return ep
				}
			}
		}
	}
	e.t.Fatalf("%s S%dE%d not found", show, season, episode)
	return itemView{}
}

func (e *env) watch(c *http.Cookie, id string, position, duration int64) {
	e.t.Helper()
	if w := e.do("PUT", "/api/items/"+id+"/progress", remote, c, map[string]int64{"positionMs": position, "durationMs": duration}); w.Code != 200 {
		e.t.Fatal(w.Body)
	}
	time.Sleep(1100 * time.Millisecond) // progress times are whole seconds
}

func TestShowsSeasonsAndNextUp(t *testing.T) {
	e, c := showEnv(t)
	var shows struct{ Shows []showView }
	decode(t, e.do("GET", "/api/shows", remote, c, nil), &shows)
	if len(shows.Shows) != 2 || shows.Shows[0].Title != "Good Show" || shows.Shows[0].Episodes != 5 {
		t.Fatalf("%+v", shows.Shows)
	}
	var show struct {
		Seasons []struct {
			Number   int
			Title    string
			Episodes []itemView
		}
	}
	decode(t, e.do("GET", "/api/shows/"+shows.Shows[0].ID, remote, c, nil), &show)
	var order []string
	for _, s := range show.Seasons {
		order = append(order, fmt.Sprintf("%s:%d", s.Title, len(s.Episodes)))
	}
	if strings.Join(order, " ") != "Season 1:3 Season 2:1 Specials:1" {
		t.Fatalf("seasons %v (specials go last)", order)
	}

	var home homeOut
	decode(t, e.do("GET", "/api/home", remote, c, nil), &home)
	if len(home.NextUp) != 0 || len(home.Continue) != 0 || home.Counts["shows"] != 2 || home.Counts["movies"] != 3 {
		t.Fatalf("a fresh library: %+v", home)
	}

	// Half-way through episode 1: it is "continue", and nothing is "next".
	e1 := e.episode(c, "Good Show", 1, 1)
	e.watch(c, e1.ID, 300_000, 1_000_000)
	decode(t, e.do("GET", "/api/home", remote, c, nil), &home)
	if len(home.Continue) != 1 || home.Continue[0].ID != e1.ID || len(home.NextUp) != 0 {
		t.Fatalf("mid-episode: continue %d, next %d", len(home.Continue), len(home.NextUp))
	}
	if home.Continue[0].ShowTitle != "Good Show" || home.Continue[0].Season != 1 {
		t.Errorf("%+v", home.Continue[0])
	}

	// Finished episode 1: episode 2 is next.
	e.watch(c, e1.ID, 990_000, 1_000_000)
	decode(t, e.do("GET", "/api/home", remote, c, nil), &home)
	if len(home.Continue) != 0 || len(home.NextUp) != 1 || home.NextUp[0].Episode != 2 {
		t.Fatalf("after episode 1: continue %d, next %+v", len(home.Continue), home.NextUp)
	}

	// Finished the last episode of season 1: season 2 episode 1 is next,
	// even though episode 2 was skipped.
	e.watch(c, e.episode(c, "Good Show", 1, 3).ID, 990_000, 1_000_000)
	decode(t, e.do("GET", "/api/home", remote, c, nil), &home)
	if len(home.NextUp) != 1 || home.NextUp[0].Season != 2 || home.NextUp[0].Episode != 1 {
		t.Fatalf("after the season finale: %+v", home.NextUp)
	}

	// Finished the last episode there is: nothing is next; specials never are.
	e.watch(c, e.episode(c, "Good Show", 2, 1).ID, 990_000, 1_000_000)
	decode(t, e.do("GET", "/api/home", remote, c, nil), &home)
	if len(home.NextUp) != 0 {
		t.Fatalf("after the last episode: %+v", home.NextUp)
	}
	decode(t, e.do("GET", "/api/shows", remote, c, nil), &shows)
	if shows.Shows[0].Watched != 3 {
		t.Errorf("watched count %d, want 3", shows.Shows[0].Watched)
	}
}

func TestSearchFindsFilmsAndShowsAndCannotBeInjected(t *testing.T) {
	e, c := showEnv(t)
	type out struct {
		Items []itemView
		Shows []showView
	}
	search := func(q string) out {
		var o out
		w := e.do("GET", "/api/search?q="+strings.ReplaceAll(q, " ", "+"), remote, c, nil)
		if w.Code != 200 {
			t.Fatalf("search %q: %d %s", q, w.Code, w.Body)
		}
		decode(t, w, &o)
		return o
	}
	if o := search("amelie"); len(o.Items) != 1 || o.Items[0].Year != 2001 {
		t.Errorf("accent-free search: %+v", o.Items)
	}
	if o := search("goo"); len(o.Shows) != 1 || o.Shows[0].Title != "Good Show" {
		t.Errorf("prefix search: %+v", o.Shows)
	}
	if o := search("show"); len(o.Shows) != 2 {
		t.Errorf("two shows share a word: %+v", o.Shows)
	}
	for _, q := range []string{`"`, `title:x`, `a OR b`, `NEAR(a b)`, `*`, `a"b`, `-`, `(`, strings.Repeat("x ", 300)} {
		search(q) // must not error
	}
	if o := search(""); len(o.Items) != 0 || len(o.Shows) != 0 {
		t.Error("an empty search returned something")
	}
}

func TestPlayableMarksFollowTheDeviceReport(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	film := e.firstItem(c, "Some Movie")
	// As ffprobe would have recorded an MKV with H.264 and Dolby sound.
	var fileID string
	e.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, film).Scan(&fileID)
	e.s.db.Exec(`UPDATE files SET probed=1, container='matroska', duration_ms=5400000 WHERE id=?`, fileID)
	e.s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,bit_depth,is_default) VALUES(?,0,'video','h264',8,1),(?,1,'audio','ac3',0,1)`, fileID, fileID)

	get := func() itemView {
		var out struct{ Items []itemView }
		decode(t, e.do("GET", "/api/movies", remote, c, nil), &out)
		for _, it := range out.Items {
			if it.ID == film {
				return it
			}
		}
		t.Fatal("film not listed")
		return itemView{}
	}
	if it := get(); it.Playable != "unknown" || it.DurationMs != 5400000 {
		t.Fatalf("before the device reports: %+v", it)
	}
	chrome := map[string]any{
		"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true},
	}
	if w := e.do("POST", "/api/capabilities", remote, c, chrome); w.Code != 200 {
		t.Fatal(w.Body)
	}
	it := get()
	if it.Playable != "no" || it.Method != "none" || len(it.Reasons) == 0 || !strings.Contains(it.Reasons[0], "Dolby Digital") {
		t.Fatalf("in a browser without Dolby: %+v", it)
	}
	// The play call carries the same answer, and says the file has sound.
	var p struct {
		Plan struct {
			Playable string
			HasAudio *bool
			Reasons  []string
		}
	}
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.Playable != "no" || p.Plan.HasAudio == nil || !*p.Plan.HasAudio {
		t.Fatalf("play plan %+v", p.Plan)
	}
	// Another device, signed in separately, has its own answer.
	safari := sessionCookie(e.do("POST", "/api/login", "192.168.1.60:1", nil, map[string]string{"name": "owner", "password": "a good password"}))
	e.do("POST", "/api/capabilities", remote, safari, map[string]any{
		"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true, "ac3": true}})
	var out struct{ Items []itemView }
	decode(t, e.do("GET", "/api/movies", remote, safari, nil), &out)
	for _, x := range out.Items {
		if x.ID == film && (x.Playable != "no" || !strings.Contains(x.Reasons[0], "MKV")) {
			t.Fatalf("in Safari the problem is the package, not the sound: %+v", x)
		}
	}
	if it := get(); !strings.Contains(it.Reasons[0], "Dolby") {
		t.Fatal("one device's report changed another's answer")
	}

	// Firefox claims MKV but cannot stream it; the server corrects that.
	r := map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true}, "video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true, "ac3": true}}
	req := e.do("POST", "/api/capabilities", remote, c, r)
	_ = req
	e.s.capsMu.Lock()
	applyOverrides(e.s.caps[func() string {
		var id string
		e.s.db.QueryRow(`SELECT id FROM sessions ORDER BY rowid LIMIT 1`).Scan(&id)
		return id
	}()], "Mozilla/5.0 Firefox/145.0")
	e.s.capsMu.Unlock()
	if it := get(); it.Playable != "no" || !strings.Contains(it.Reasons[0], "MKV") {
		t.Fatalf("Firefox override: %+v", it)
	}
	// A silent file is known to be silent.
	e.s.db.Exec(`DELETE FROM streams WHERE file_id=? AND kind='audio'`, fileID)
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if p.Plan.HasAudio == nil || *p.Plan.HasAudio {
		t.Fatalf("a file with no sound track reports sound: %+v", p.Plan)
	}
}

// Whatever is put where an ID belongs, the answer is a refusal: never a
// file, never a crash.
func TestNastyIDsAreRefusedEverywhere(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	secret := filepath.Join(filepath.Dir(e.media), "secret.txt")
	os.WriteFile(secret, []byte("private"), 0o644)
	defer os.Remove(secret)
	nasty := []string{
		"..", "../secret.txt", "..%2Fsecret.txt", "%2e%2e%2fsecret.txt", "....//secret.txt", "..%5Csecret.txt",
		"%00", "a%00b", strings.Repeat("a", 5000), "0123456789abcdef0123456789abcdeg", "0123456789ABCDEF0123456789ABCDEF",
		"'%20OR%201=1--", "%22%3E%3Cscript%3E", "local:..%2Fsecret.txt", "C:%5CWindows%5Cwin.ini", "/etc/passwd", "%2Fetc%2Fpasswd",
	}
	for _, rt := range e.s.Routes() {
		if !strings.Contains(rt.Pattern, "{") {
			continue
		}
		for _, bad := range nasty {
			target := strings.NewReplacer("{id}", bad, "{fileID}", bad).Replace(rt.Pattern)
			if rt.Policy == SignedLink {
				target += "?e=99999999999&s=0123456789abcdef0123456789abcdef&g=x"
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s %s crashed: %v", rt.Method, target, r)
					}
				}()
				w := e.do(rt.Method, target, remote, c, map[string]int64{"positionMs": 1, "durationMs": 2})
				if w.Code < 400 || w.Code >= 500 || strings.Contains(w.Body.String(), "private") {
					t.Errorf("%s %s answered %d %.80q", rt.Method, target, w.Code, w.Body)
				}
			}()
		}
	}
}
