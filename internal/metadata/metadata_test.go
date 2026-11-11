package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/library"
)

// fake serves answers recorded from the real TMDB (testdata/), so tests
// need no network and no key.
type fake struct {
	*httptest.Server
	requests atomic.Int32
	seen     []string
	badKey   bool
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	file := func(w http.ResponseWriter, name string) {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			http.Error(w, "no recording", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.seen = append(f.seen, r.URL.Path+"?"+r.URL.Query().Get("query"))
		isImage := strings.HasPrefix(r.URL.Path, "/img/") // the image site needs no key
		if !isImage && (r.URL.Query().Get("api_key") != "shortkey" && r.Header.Get("Authorization") != "Bearer eyJlongtoken" || f.badKey) {
			http.Error(w, `{"status_code":7,"success":false}`, 401)
			return
		}
		q := strings.ToLower(r.URL.Query().Get("query"))
		switch {
		case r.URL.Path == "/3/authentication":
			file(w, "authentication.json")
		case r.URL.Path == "/3/search/movie" && strings.Contains(q, "matrix"):
			file(w, "search-movie-matrix.json")
		case r.URL.Path == "/3/search/movie" && (strings.Contains(q, "amelie") || strings.Contains(q, "amélie")):
			file(w, "search-movie-amelie.json")
		case r.URL.Path == "/3/search/movie":
			file(w, "search-movie-none.json")
		case r.URL.Path == "/3/search/tv" && strings.Contains(q, "breaking"):
			file(w, "search-tv-breaking-bad.json")
		case r.URL.Path == "/3/search/tv" && strings.Contains(q, "office"):
			file(w, "search-tv-office.json")
		case r.URL.Path == "/3/search/tv":
			file(w, "search-movie-none.json")
		case r.URL.Path == "/3/tv/1396/season/1":
			file(w, "tv-1396-season-1.json")
		case r.URL.Path == "/3/movie/603":
			file(w, "movie-603.json")
		case strings.HasPrefix(r.URL.Path, "/img/w342/"):
			b, _ := os.ReadFile(filepath.Join("testdata", "poster.jpg"))
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(b)
		default:
			http.Error(w, `{"status_code":34}`, 404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) client(key string) *Client { return New(key).WithBases(f.URL+"/3", f.URL+"/img") }

func TestBothKindsOfKeyWorkAndABadOneIsRefused(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	if err := f.client("shortkey").Check(ctx); err != nil {
		t.Errorf("short key: %v", err)
	}
	if err := f.client("eyJlongtoken").Check(ctx); err != nil {
		t.Errorf("long token: %v", err)
	}
	if err := f.client("wrong").Check(ctx); err != ErrBadKey {
		t.Errorf("wrong key: %v", err)
	}
	if err := f.client("").Check(ctx); err != ErrBadKey {
		t.Errorf("no key: %v", err)
	}
	// A dead server must not leak the key through an error message.
	c := New("shortkey").WithBases("http://127.0.0.1:1/3", "http://127.0.0.1:1/img")
	if err := c.Check(ctx); err == nil || strings.Contains(err.Error(), "shortkey") {
		t.Errorf("error %v", err)
	}
}

func TestPickPrefersNothingOverTheWrongFilm(t *testing.T) {
	hits := []Result{{ID: 1, Title: "The Office", Year: 2005}, {ID: 2, Title: "The Office", Year: 2001}, {ID: 3, Title: "The Office PL", Year: 2021}}
	cases := []struct {
		title string
		year  int
		want  int
	}{
		{"The Office", 0, 1},    // no year: the best-known
		{"the office", 2001, 2}, // the year decides
		{"The Office", 2002, 2}, // a year off by one is still that one
		{"The Office", 1990, 0}, // right title, wrong year, several candidates: nothing
		{"The Office US", 0, 0}, // not the same title
		{"Office", 0, 0},
	}
	for _, c := range cases {
		got, ok := Pick(c.title, c.year, hits)
		if (c.want == 0) == ok || got.ID != c.want {
			t.Errorf("%q (%d): picked %d, want %d", c.title, c.year, got.ID, c.want)
		}
	}
	if got, ok := Pick("Amelie", 2001, []Result{{ID: 9, Title: "Le Fabuleux Destin d'Amélie Poulain", OriginalTitle: "Amélie", Year: 2001}}); !ok || got.ID != 9 {
		t.Error("the original title did not match")
	}
	if got, ok := Pick("Fast and Furious 6", 2013, []Result{{ID: 5, Title: "Fast & Furious 6", Year: 2013}}); !ok || got.ID != 5 {
		t.Error("'and' and '&' are the same word")
	}
	if _, ok := Pick("Anything", 0, nil); ok {
		t.Error("picked from nothing")
	}
}

func TestHintID(t *testing.T) {
	for rel, want := range map[string]int{
		"Films/The Matrix (1999) {tmdb-603}/The Matrix.mkv": 603,
		"Films/The Matrix (1999) [tmdbid-603]/x.mkv":        603,
		"Films/The Matrix (1999)/The Matrix.mkv":            0,
		"Films/{tmdb-abc}/x.mkv":                            0,
	} {
		if got := HintID(rel); got != want {
			t.Errorf("%s: %d, want %d", rel, got, want)
		}
	}
}

func TestPosterPathIsChecked(t *testing.T) {
	f := newFake(t)
	c := f.client("shortkey")
	for _, bad := range []string{"", "x.jpg", "/../../etc/passwd", "/a/b.jpg", "/x.jpg?y=1", "//evil.example/x.jpg", "/x.svg"} {
		if _, err := c.Poster(context.Background(), bad); err == nil {
			t.Errorf("poster path %q was fetched", bad)
		}
	}
	if b, err := c.Poster(context.Background(), "/dXNAPwY7VrqMAo51EKhhCJfaGb5.jpg"); err != nil || len(b) < 1000 {
		t.Errorf("good poster: %d bytes, %v", len(b), err)
	}
}

func write(t *testing.T, p string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(p), 0o644)
}

func TestEnrichALibrary(t *testing.T) {
	f := newFake(t)
	media := t.TempDir()
	write(t, filepath.Join(media, "Films", "the.matrix.1999.1080p.bluray.x264-grp.mkv"))
	write(t, filepath.Join(media, "Films", "Amelie (2001)", "Amelie (2001).mkv"))
	write(t, filepath.Join(media, "Films", "My Holiday Video.mp4"))
	write(t, filepath.Join(media, "TV", "Breaking Bad", "Season 01", "Breaking Bad - S01E01.mkv"))
	write(t, filepath.Join(media, "TV", "Breaking Bad", "Season 01", "Breaking Bad - S01E02.mkv"))
	data := t.TempDir()
	d, err := db.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	lib, _ := library.Add(d, media)
	if _, err := library.Scan(d, lib); err != nil {
		t.Fatal(err)
	}
	art := filepath.Join(data, "artwork")
	sum, err := Enrich(context.Background(), d, f.client("shortkey"), art)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Found != 3 || sum.NotFound != 1 {
		t.Fatalf("%+v, want 3 found (two films, one show) and 1 not found (the home video)", sum)
	}
	var title, overview, poster string
	var year, tmdb int
	d.QueryRow(`SELECT title, year, overview, tmdb_id, poster FROM items WHERE match_key LIKE 'thematrix/%'`).Scan(&title, &year, &overview, &tmdb, &poster)
	if title != "The Matrix" || year != 1999 || tmdb != 603 || len(overview) < 20 || poster == "" {
		t.Errorf("film: %q %d tmdb=%d poster=%q overview=%d chars", title, year, tmdb, poster, len(overview))
	}
	if st, err := os.Stat(filepath.Join(art, poster)); err != nil || st.Size() < 1000 {
		t.Errorf("poster file: %v", err)
	}
	d.QueryRow(`SELECT title FROM items WHERE match_key LIKE 'amelie/%'`).Scan(&title)
	if title != "Amélie" {
		t.Errorf("accented title %q", title)
	}
	var state int
	d.QueryRow(`SELECT title, lookup_state FROM items WHERE match_key LIKE 'myholidayvideo/%'`).Scan(&title, &state)
	if title != "My Holiday Video" || state != 2 {
		t.Errorf("a home video keeps its own name: %q state %d", title, state)
	}
	d.QueryRow(`SELECT title FROM items WHERE kind='episode' AND episode_number=1`).Scan(&title)
	if title != "Pilot" {
		t.Errorf("episode 1 is called %q, want Pilot", title)
	}
	// Search finds the film under the name in the file as well.
	var n int
	d.QueryRow(`SELECT count(*) FROM item_fts WHERE item_fts MATCH '"amelie"*'`).Scan(&n)
	if n != 1 {
		t.Errorf("search for the unaccented title found %d", n)
	}

	// Nothing is asked twice: neither what was found nor what was not.
	before := f.requests.Load()
	if sum, _ = Enrich(context.Background(), d, f.client("shortkey"), art); sum.Found+sum.NotFound != 0 || f.requests.Load() != before {
		t.Errorf("second round did %+v with %d requests", sum, f.requests.Load()-before)
	}
	// Only titles went out: no paths, no file names, no release tags.
	for _, s := range f.seen {
		if strings.Contains(s, "mkv") || strings.Contains(s, "1080p") || strings.Contains(s, "Films") || strings.Contains(strings.ToLower(s), "grp") {
			t.Errorf("more than a title was sent: %s", s)
		}
	}

	// A rescan still recognises the files although their titles changed,
	// and a new episode of a known show gets its name.
	write(t, filepath.Join(media, "TV", "Breaking Bad", "Season 01", "Breaking Bad - S01E03.mkv"))
	res, err := library.Scan(d, lib)
	if err != nil || res.Added != 1 {
		t.Fatalf("rescan: %+v %v", res, err)
	}
	d.QueryRow(`SELECT count(*) FROM items WHERE kind='show'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d shows after a rescan, want 1", n)
	}
	Enrich(context.Background(), d, f.client("shortkey"), art)
	d.QueryRow(`SELECT title FROM items WHERE kind='episode' AND episode_number=3`).Scan(&title)
	if title == "Episode 3" || title == "" {
		t.Errorf("the new episode was not named: %q", title)
	}
}

func TestEnrichStopsOnABadKeyAndChangesNothing(t *testing.T) {
	f := newFake(t)
	f.badKey = true
	media := t.TempDir()
	write(t, filepath.Join(media, "The Matrix (1999).mkv"))
	data := t.TempDir()
	d, _ := db.Open(data)
	defer d.Close()
	lib, _ := library.Add(d, media)
	library.Scan(d, lib)
	if _, err := Enrich(context.Background(), d, f.client("shortkey"), filepath.Join(data, "artwork")); err != ErrBadKey {
		t.Fatalf("err %v", err)
	}
	var state int
	d.QueryRow(`SELECT lookup_state FROM items WHERE kind='movie'`).Scan(&state)
	if state != 0 {
		t.Fatal("a refused key marked the film as looked up")
	}
}

func TestHintOverridesSearch(t *testing.T) {
	f := newFake(t)
	media := t.TempDir()
	write(t, filepath.Join(media, "Some Odd Name (1999) {tmdb-603}", "film.mkv"))
	data := t.TempDir()
	d, _ := db.Open(data)
	defer d.Close()
	lib, _ := library.Add(d, media)
	library.Scan(d, lib)
	Enrich(context.Background(), d, f.client("eyJlongtoken"), filepath.Join(data, "artwork"))
	var title string
	d.QueryRow(`SELECT title FROM items WHERE kind='movie'`).Scan(&title)
	if title != "The Matrix" {
		t.Fatalf("title %q", title)
	}
}

// TestLive talks to the real TMDB. It runs only when asked:
//
//	LM_LIVE_TMDB=1 go test ./internal/metadata -run TestLive
//
// In the project's build environment the credential is added to the
// request on the way out, so the key used here is a stand-in.
func TestLive(t *testing.T) {
	if os.Getenv("LM_LIVE_TMDB") == "" {
		t.Skip("set LM_LIVE_TMDB=1 to talk to the real TMDB")
	}
	key := os.Getenv("LM_TMDB_KEY")
	if key == "" {
		key = "eyJ-added-on-the-way-out"
	}
	c := New(key)
	ctx := context.Background()
	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	hits, err := c.SearchMovie(ctx, "The Matrix", 1999)
	if err != nil {
		t.Fatal(err)
	}
	hit, ok := Pick("the matrix", 1999, hits)
	if !ok || hit.ID != 603 {
		t.Fatalf("%+v", hit)
	}
	if img, err := c.Poster(ctx, hit.PosterPath); err != nil || len(img) < 5000 {
		t.Fatalf("poster %d bytes, %v", len(img), err)
	}
	shows, _ := c.SearchTV(ctx, "Breaking Bad", 0)
	show, ok := Pick("Breaking Bad", 0, shows)
	eps, err := c.Season(ctx, show.ID, 1)
	if !ok || err != nil || len(eps) != 7 || eps[0].Name != "Pilot" {
		t.Fatalf("%+v %v %d", show, err, len(eps))
	}
}
