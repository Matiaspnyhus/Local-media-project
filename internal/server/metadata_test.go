package server

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/metadata"
)

// fakeTMDB answers like TMDB for one film, from the recordings kept with
// the metadata package.
func fakeTMDB(t *testing.T, e *env) {
	t.Helper()
	rec := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "metadata", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/img/"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(rec("poster.jpg"))
		case r.URL.Query().Get("api_key") != "goodkey":
			http.Error(w, `{"success":false}`, 401)
		case r.URL.Path == "/3/authentication":
			w.Write(rec("authentication.json"))
		case r.URL.Path == "/3/search/movie" && strings.Contains(strings.ToLower(r.URL.Query().Get("query")), "matrix"):
			w.Write(rec("search-movie-matrix.json"))
		default:
			w.Write(rec("search-movie-none.json"))
		}
	}))
	t.Cleanup(srv.Close)
	e.s.newTMDB = func(key string) *metadata.Client { return metadata.New(key).WithBases(srv.URL+"/3", srv.URL+"/img") }
}

func TestOnlineLookupsNeedAKeyAndCanBeSwitchedOff(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(filepath.Join(e.media, "The.Matrix.1999.1080p.mkv"), []byte("matrix"), 0o644)
	c := e.setup()
	fakeTMDB(t, e)

	var st struct {
		TmdbKeySaved bool
		Online       bool
		Lookups      struct{ Pending, Found, NotFound int }
	}
	decode(t, e.do("GET", "/api/settings", remote, c, nil), &st)
	if st.TmdbKeySaved || !st.Online || st.Lookups.Pending != 3 {
		t.Fatalf("fresh settings: %+v", st)
	}
	// Without a key nothing is looked up, and nothing leaves the PC.
	e.s.EnrichNow()
	decode(t, e.do("GET", "/api/settings", remote, c, nil), &st)
	if st.Lookups.Found+st.Lookups.NotFound != 0 {
		t.Fatal("lookups ran without a key")
	}

	if w := e.do("PUT", "/api/settings/tmdb", remote, c, map[string]string{"key": "wrongkey"}); w.Code != 400 || !strings.Contains(w.Body.String(), "did not accept") {
		t.Fatalf("a wrong key: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/settings/tmdb", remote, c, map[string]string{"key": "has spaces in it"}); w.Code != 400 {
		t.Fatalf("a malformed key: %d", w.Code)
	}
	if w := e.do("PUT", "/api/settings/tmdb", remote, c, map[string]string{"key": " goodkey\n"}); w.Code != 200 {
		t.Fatalf("a good key: %d %s", w.Code, w.Body)
	}
	e.s.background.Wait()
	e.s.EnrichNow()
	w := e.do("GET", "/api/settings", remote, c, nil)
	if strings.Contains(w.Body.String(), "goodkey") {
		t.Fatal("the settings call sends the key back")
	}
	decode(t, w, &st)
	if !st.TmdbKeySaved || st.Lookups.Found != 1 || st.Lookups.NotFound != 2 || st.Lookups.Pending != 0 {
		t.Fatalf("after lookups: %+v", st)
	}

	var out struct{ Items []itemView }
	decode(t, e.do("GET", "/api/movies", remote, c, nil), &out)
	var film itemView
	for _, it := range out.Items {
		if it.Title == "The Matrix" {
			film = it
		}
	}
	if film.ID == "" || !film.Poster || film.Year != 1999 || len(film.Overview) < 20 {
		t.Fatalf("the film after lookup: %+v", film)
	}
	// The poster needs sign-in like everything else.
	if w := e.do("GET", "/api/items/"+film.ID+"/poster", remote, nil, nil); w.Code != 401 {
		t.Errorf("poster without sign-in: %d", w.Code)
	}
	if w := e.do("GET", "/api/items/"+film.ID+"/poster", remote, c, nil); w.Code != 200 || w.Body.Len() < 1000 || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/") {
		t.Errorf("poster: %d %d bytes %s", w.Code, w.Body.Len(), w.Header().Get("Content-Type"))
	}
	if w := e.do("GET", "/api/items/"+e.firstItem(c, "clip")+"/poster", remote, c, nil); w.Code != 404 {
		t.Errorf("a film without a poster: %d", w.Code)
	}

	// The bundle lists what was not found, and never holds the key.
	b := e.do("GET", "/api/support-bundle", remote, c, nil)
	zr, _ := zip.NewReader(bytes.NewReader(b.Body.Bytes()), int64(b.Body.Len()))
	all := ""
	for _, f := range zr.File {
		rc, _ := f.Open()
		x, _ := io.ReadAll(rc)
		rc.Close()
		all += f.Name + "\n" + string(x)
	}
	if !strings.Contains(all, "not-found-online.json") || !strings.Contains(all, "movie: clip") || strings.Contains(all, "goodkey") {
		t.Errorf("bundle: has list %v, has key %v", strings.Contains(all, "movie: clip"), strings.Contains(all, "goodkey"))
	}

	// Switched off: a new film is not looked up.
	if w := e.do("PUT", "/api/settings/online", remote, c, map[string]bool{"on": false}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	os.WriteFile(filepath.Join(e.media, "The Matrix Reloaded (2003).mkv"), []byte("reloaded"), 0o644)
	e.do("POST", "/api/libraries/scan", remote, c, nil)
	e.s.background.Wait()
	e.s.EnrichNow()
	decode(t, e.do("GET", "/api/settings", remote, c, nil), &st)
	if st.Online || st.Lookups.Pending != 1 {
		t.Fatalf("with lookups off: %+v", st)
	}
	// Removing the key works too.
	e.do("PUT", "/api/settings/tmdb", remote, c, map[string]string{"key": ""})
	decode(t, e.do("GET", "/api/settings", remote, c, nil), &st)
	if st.TmdbKeySaved {
		t.Fatal("the key was not removed")
	}
}
