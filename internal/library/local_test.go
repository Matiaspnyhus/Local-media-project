package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInfoFilesAndLocalPicturesWin(t *testing.T) {
	media := t.TempDir()
	film := filepath.Join(media, "Films", "odd.name.2019.mkv")
	write(t, film, "film")
	write(t, filepath.Join(media, "Films", "odd.name.2019.nfo"), `<?xml version="1.0"?><movie><title>The Proper Title</title><year>2018</year><plot>What it is about.</plot><uniqueid type="imdb">tt1</uniqueid><uniqueid type="tmdb" default="true">603</uniqueid></movie>`)
	write(t, filepath.Join(media, "Films", "Boxed (2001)", "Boxed (2001).mkv"), "boxed")
	write(t, filepath.Join(media, "Films", "Boxed (2001)", "movie.nfo"), `<movie><title>Boxed!</title><plot>In a folder of its own.</plot></movie>`)
	write(t, filepath.Join(media, "Films", "Boxed (2001)", "poster.jpg"), "jpeg bytes")
	write(t, filepath.Join(media, "Films", "Linked (2002)", "Linked (2002).mkv"), "linked")
	write(t, filepath.Join(media, "Films", "Linked (2002)", "Linked (2002).nfo"), `https://www.themoviedb.org/movie/603`)
	write(t, filepath.Join(media, "TV", "Some Show", "tvshow.nfo"), `<tvshow><title>Some Show: The Series</title><premiered>2015-03-01</premiered><plot>A show.</plot><tmdbid>1396</tmdbid></tvshow>`)
	write(t, filepath.Join(media, "TV", "Some Show", "folder.jpg"), "jpeg bytes")
	write(t, filepath.Join(media, "TV", "Some Show", "Season 01", "Some Show - S01E01.mkv"), "e1")
	write(t, filepath.Join(media, "TV", "Some Show", "Season 01", "Some Show - S01E01.nfo"), `<episodedetails><title>The Beginning</title><plot>It begins.</plot><season>1</season><episode>1</episode></episodedetails>`)
	write(t, filepath.Join(media, "TV", "Some Show", "Season 01", "Some Show - S01E02.mkv"), "e2")
	outside := filepath.Join(t.TempDir(), "secret.nfo")
	write(t, outside, `<movie><title>Stolen</title></movie>`)
	write(t, filepath.Join(media, "Films", "Trick (2003)", "Trick (2003).mkv"), "trick")
	os.Symlink(outside, filepath.Join(media, "Films", "Trick (2003)", "movie.nfo"))

	d := open(t)
	lib, _ := Add(d, media)
	if _, err := Scan(d, lib); err != nil {
		t.Fatal(err)
	}
	row := func(where string) (title, overview, source, poster string, year, tmdb int) {
		t.Helper()
		if err := d.QueryRow(`SELECT title, overview, name_source, poster, year, tmdb_id FROM items WHERE `+where).Scan(&title, &overview, &source, &poster, &year, &tmdb); err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		return
	}
	title, overview, source, _, year, tmdb := row(`kind='movie' AND match_key LIKE 'oddname/%'`)
	if title != "The Proper Title" || year != 2018 || overview != "What it is about." || source != "nfo" || tmdb != 603 {
		t.Errorf("film with its own info file: %q %d %q %s tmdb=%d", title, year, overview, source, tmdb)
	}
	title, _, _, poster, year, _ := row(`kind='movie' AND match_key LIKE 'boxed/%'`)
	if title != "Boxed!" || year != 2001 || poster != "local:Films/Boxed (2001)/poster.jpg" {
		t.Errorf("film in its own folder: %q %d poster %q", title, year, poster)
	}
	if title, _, source, _, _, _ := row(`kind='movie' AND match_key LIKE 'linked/%'`); title != "Linked" || source != "file" {
		t.Errorf("an info file holding only a web address changed the film: %q %s", title, source)
	}
	if title, _, source, _, _, _ := row(`kind='movie' AND match_key LIKE 'trick/%'`); title != "Trick" || source != "file" {
		t.Errorf("an info file linked from outside the library was read: %q %s", title, source)
	}
	title, overview, _, poster, year, tmdb = row(`kind='show'`)
	if title != "Some Show: The Series" || year != 2015 || overview != "A show." || tmdb != 1396 || poster != "local:TV/Some Show/folder.jpg" {
		t.Errorf("show: %q %d %q tmdb=%d poster=%q", title, year, overview, tmdb, poster)
	}
	var sortKey string
	var ep int
	d.QueryRow(`SELECT title, sort_title, episode_number FROM items WHERE kind='episode' AND name_source='nfo'`).Scan(&title, &sortKey, &ep)
	if title != "The Beginning" || sortKey != "0001" || ep != 1 {
		t.Errorf("episode: %q sort %q number %d", title, sortKey, ep)
	}
	// Search finds the proper title; a rescan changes nothing and does not
	// turn the renamed show into a second one.
	if n := count(t, d, `SELECT count(*) FROM item_fts WHERE item_fts MATCH '"proper"*'`); n != 1 {
		t.Errorf("search for the info file's title found %d", n)
	}
	write(t, filepath.Join(media, "TV", "Some Show", "Season 01", "Some Show - S01E03.mkv"), "e3")
	if res, err := Scan(d, lib); err != nil || res.Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if n := count(t, d, `SELECT count(*) FROM items WHERE kind='show'`); n != 1 {
		t.Fatalf("%d shows after a rescan", n)
	}
}
