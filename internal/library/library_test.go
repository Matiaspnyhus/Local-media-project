package library

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/probe"
)

func write(t *testing.T, p, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func count(t *testing.T, d *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func snapshot(root string) []string {
	var out []string
	filepath.Walk(root, func(p string, i os.FileInfo, _ error) error {
		if i != nil {
			out = append(out, p+"|"+i.ModTime().String())
		}
		return nil
	})
	return out
}

func TestScanBuildsFilmsShowsAndSeasons(t *testing.T) {
	media := t.TempDir()
	write(t, filepath.Join(media, "Films", "Some Movie (2019)", "Some Movie (2019).mkv"), "film-a")
	write(t, filepath.Join(media, "Films", "Some Movie (2019)", "Some Movie (2019).en.srt"), "sub")
	write(t, filepath.Join(media, "Films", "Some Movie (2019)", "Some Movie (2019).no.forced.srt"), "sub")
	write(t, filepath.Join(media, "Films", "Some Movie (2019)", "Some Movie (2019)-trailer.mp4"), "trailer")
	write(t, filepath.Join(media, "Films", "Some Movie (2019)", "Extras", "Making Of.mkv"), "extra")
	write(t, filepath.Join(media, "Films", "The.Other.Film.2001.1080p.BluRay.x264-GRP.mkv"), "film-b")
	write(t, filepath.Join(media, "TV", "Good Show", "Season 01", "Good Show - S01E01 - Pilot.mkv"), "ep-1")
	write(t, filepath.Join(media, "TV", "Good Show", "Season 01", "Good Show - S01E02.mkv"), "ep-2")
	write(t, filepath.Join(media, "TV", "Good Show", "Season 02", "good.show.s02e01.720p.hdtv.mkv"), "ep-3")
	write(t, filepath.Join(media, "TV", "Good Show", "Specials", "Good Show - S00E01 - Special.mkv"), "ep-0")
	write(t, filepath.Join(media, "holiday.mp4"), "home")
	write(t, filepath.Join(media, "notes.txt"), "x")
	write(t, filepath.Join(media, ".hidden", "secret.mp4"), "x")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "outside.mp4"), "x")
	os.Symlink(filepath.Join(outside, "outside.mp4"), filepath.Join(media, "link.mp4"))
	before := snapshot(media)

	d := open(t)
	lib, err := Add(d, media)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Scan(d, lib)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 7 || res.Total != 7 {
		t.Fatalf("first scan %+v, want 7 files (trailer, extra, hidden folder, text file and link skipped)", res)
	}
	if n := count(t, d, `SELECT count(*) FROM items WHERE kind='movie'`); n != 3 {
		t.Errorf("%d films, want 3", n)
	}
	if n := count(t, d, `SELECT count(*) FROM items WHERE kind='show'`); n != 1 {
		t.Errorf("%d shows, want 1 (three spellings of one show)", n)
	}
	if n := count(t, d, `SELECT count(*) FROM items WHERE kind='season'`); n != 3 {
		t.Errorf("%d seasons, want 3 (1, 2 and specials)", n)
	}
	if n := count(t, d, `SELECT count(*) FROM items WHERE kind='episode'`); n != 4 {
		t.Errorf("%d episodes, want 4", n)
	}
	var title string
	var year int
	d.QueryRow(`SELECT i.title, i.year FROM items i JOIN versions v ON v.item_id=i.id JOIN files f ON f.version_id=v.id WHERE f.rel_path LIKE '%Other.Film%'`).Scan(&title, &year)
	if title != "The Other Film" || year != 2001 {
		t.Errorf("film %q (%d)", title, year)
	}
	if n := count(t, d, `SELECT count(*) FROM sidecars`); n != 2 {
		t.Errorf("%d subtitle files recorded, want 2", n)
	}
	var lang string
	var forced int
	d.QueryRow(`SELECT language, is_forced FROM sidecars WHERE rel_path LIKE '%forced%'`).Scan(&lang, &forced)
	if lang != "no" || forced != 1 {
		t.Errorf("forced subtitle: language %q forced %d", lang, forced)
	}
	// Search finds films and shows, ignoring accents and case.
	if n := count(t, d, `SELECT count(*) FROM item_fts WHERE item_fts MATCH 'good'`); n != 1 {
		t.Errorf("search for the show found %d", n)
	}

	res, _ = Scan(d, lib)
	if res.Added != 0 || res.Moved != 0 || res.Missing != 0 {
		t.Fatalf("an unchanged rescan changed something: %+v", res)
	}
	after := snapshot(media)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatal("scanning changed the media folder")
	}
	if _, err := Add(d, media); err == nil {
		t.Fatal("same folder added twice")
	}
}

func TestMovedOrRenamedFileKeepsItsProgress(t *testing.T) {
	media := t.TempDir()
	old := filepath.Join(media, "downloads", "some.movie.2019.1080p.mkv")
	write(t, old, strings.Repeat("the film ", 40000))
	write(t, filepath.Join(media, "TV", "Good Show", "Season 01", "S01E01.mkv"), "episode one")
	d := open(t)
	lib, _ := Add(d, media)
	if _, err := Scan(d, lib); err != nil {
		t.Fatal(err)
	}
	d.Exec(`INSERT INTO users(id,name,password_hash,role,created_at) VALUES('u','o','x','owner',0)`)
	var filmID string
	d.QueryRow(`SELECT id FROM items WHERE kind='movie'`).Scan(&filmID)
	d.Exec(`INSERT INTO progress(user_id,item_id,position_ms,duration_ms,played,updated_at) VALUES('u',?,1000,5000,1,0)`, filmID)

	// The owner tidies up: the film gets a proper folder and name.
	tidy := filepath.Join(media, "Films", "Some Movie (2019)", "Some Movie (2019).mkv")
	os.MkdirAll(filepath.Dir(tidy), 0o755)
	if err := os.Rename(old, tidy); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(d, lib)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 1 || res.Added != 0 || res.Missing != 0 {
		t.Fatalf("after a rename: %+v, want exactly one move", res)
	}
	if n := count(t, d, `SELECT count(*) FROM items WHERE kind='movie'`); n != 1 {
		t.Fatalf("%d films after a rename, want 1", n)
	}
	if n := count(t, d, `SELECT count(*) FROM progress p JOIN items i ON i.id=p.item_id WHERE i.id=? AND p.played=1`, filmID); n != 1 {
		t.Fatal("the renamed film is no longer marked as watched")
	}

	// A missing file keeps its item; when it returns, nothing is new.
	ep := filepath.Join(media, "TV", "Good Show", "Season 01", "S01E01.mkv")
	away := filepath.Join(t.TempDir(), "away.mkv")
	os.Rename(ep, away)
	res, _ = Scan(d, lib)
	if res.Missing != 1 || count(t, d, `SELECT count(*) FROM items WHERE kind='episode'`) != 1 {
		t.Fatalf("missing episode: %+v", res)
	}
	res, _ = Scan(d, lib)
	if res.Missing != 0 {
		t.Fatalf("a file already known to be missing was counted again: %+v", res)
	}
	os.Rename(away, ep)
	res, _ = Scan(d, lib)
	if res.Added != 0 || res.Missing != 0 {
		t.Fatalf("file came back: %+v", res)
	}

	// A different file under the old name is a new item, not the old one.
	write(t, old, strings.Repeat("another film ", 40000))
	res, _ = Scan(d, lib)
	if res.Added != 1 || res.Moved != 0 {
		t.Fatalf("a different file was mistaken for a move: %+v", res)
	}
}

func TestEpisodeMovedBetweenShowsLeavesNoEmptyShow(t *testing.T) {
	media := t.TempDir()
	wrong := filepath.Join(media, "TV", "Wrong Name", "Season 01", "Wrong Name - S01E01.mkv")
	write(t, wrong, strings.Repeat("ep", 1000))
	d := open(t)
	lib, _ := Add(d, media)
	Scan(d, lib)
	right := filepath.Join(media, "TV", "Right Name", "Season 01", "Right Name - S01E01.mkv")
	os.MkdirAll(filepath.Dir(right), 0o755)
	os.Rename(wrong, right)
	if res, _ := Scan(d, lib); res.Moved != 1 {
		t.Fatalf("%+v", res)
	}
	var shows string
	rows, _ := d.Query(`SELECT title FROM items WHERE kind='show'`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		shows += s + ";"
	}
	rows.Close()
	if shows != "Right Name;" {
		t.Fatalf("shows after the move: %q", shows)
	}
	if n := count(t, d, `SELECT count(*) FROM item_fts WHERE item_fts MATCH 'wrong'`); n != 0 {
		t.Error("the removed show is still found by search")
	}
}

func TestTwoCopiesOfOneFilmAreOneItem(t *testing.T) {
	media := t.TempDir()
	write(t, filepath.Join(media, "Some Movie (2019)", "Some Movie (2019) - 1080p.mkv"), "small copy")
	write(t, filepath.Join(media, "Some Movie (2019)", "Some Movie (2019) - 2160p.mkv"), "big copy!!")
	d := open(t)
	lib, _ := Add(d, media)
	Scan(d, lib)
	if items, versions := count(t, d, `SELECT count(*) FROM items WHERE kind='movie'`), count(t, d, `SELECT count(*) FROM versions`); items != 1 || versions != 2 {
		t.Fatalf("%d items, %d versions; want 1 and 2", items, versions)
	}
}

func TestItemsFromTheFirstVersionAreRelabelledInPlace(t *testing.T) {
	media := t.TempDir()
	write(t, filepath.Join(media, "Good Show", "Season 01", "Good Show - S01E03.mkv"), "episode")
	d := open(t)
	lib, _ := Add(d, media)
	// What M0 wrote: a flat "video" item.
	d.Exec(`INSERT INTO items(id,library_id,kind,title,sort_title,created_at) VALUES('old',?, 'video','Good Show - S01E03','x',0)`, lib)
	d.Exec(`INSERT INTO versions(id,item_id) VALUES('v','old')`)
	d.Exec(`INSERT INTO files(id,version_id,library_id,rel_path,size,mtime) VALUES('f','v',?,'Good Show/Season 01/Good Show - S01E03.mkv',7,0)`, lib)
	if _, err := Scan(d, lib); err != nil {
		t.Fatal(err)
	}
	var kind string
	var season, episode int
	d.QueryRow(`SELECT kind, season_number, episode_number FROM items WHERE id='old'`).Scan(&kind, &season, &episode)
	if kind != "episode" || season != 1 || episode != 3 {
		t.Fatalf("legacy item is now %s S%dE%d", kind, season, episode)
	}
	if n := count(t, d, `SELECT count(*) FROM items WHERE kind='video'`); n != 0 {
		t.Fatal("a legacy item was left behind")
	}
}

func TestProbePendingStoresTracksOnce(t *testing.T) {
	ff := probe.Find()
	if ff == "" {
		t.Skip("ffprobe is not installed here")
	}
	media := t.TempDir()
	for _, name := range []string{"Test Clip.mp4", "Dolby.Sound.Test.mkv"} {
		src, err := os.Open(filepath.Join("..", "..", "e2e", "fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		dst, _ := os.Create(filepath.Join(media, name))
		io.Copy(dst, src)
		src.Close()
		dst.Close()
	}
	write(t, filepath.Join(media, "broken.mkv"), "this is not a video")
	d := open(t)
	lib, _ := Add(d, media)
	Scan(d, lib)
	res, err := ProbePending(context.Background(), d, ff)
	if err != nil || res.Inspected != 2 || res.Failed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	var codec string
	var channels int
	d.QueryRow(`SELECT s.codec, s.channels FROM streams s JOIN files f ON f.id=s.file_id WHERE f.rel_path LIKE 'Dolby%' AND s.kind='audio'`).Scan(&codec, &channels)
	if codec != "ac3" {
		t.Errorf("audio codec %q, want ac3", codec)
	}
	// The label the MP4 gives its picture is kept; an MKV has none.
	labels := map[string]string{}
	for _, name := range []string{"Test Clip.mp4", "Dolby.Sound.Test.mkv"} {
		var label string
		d.QueryRow(`SELECT s.codec_tag FROM streams s JOIN files f ON f.id=s.file_id WHERE f.rel_path=? AND s.kind='video'`, name).Scan(&label)
		labels[name] = label
	}
	if labels["Test Clip.mp4"] != "avc1" || labels["Dolby.Sound.Test.mkv"] != "" {
		t.Errorf("picture labels %q, want avc1 for the MP4 and none for the MKV", labels)
	}
	if n := count(t, d, `SELECT duration_ms FROM files WHERE rel_path='Test Clip.mp4'`); n < 19000 || n > 21000 {
		t.Errorf("duration %d ms", n)
	}
	// Nothing is inspected twice, including the file that failed.
	res, _ = ProbePending(context.Background(), d, ff)
	if res.Inspected != 0 || res.Failed != 0 {
		t.Fatalf("second round inspected again: %+v", res)
	}
}

// A file named .webm is stored as WebM, not as MKV (ffprobe has one name
// for both). And a file an earlier version stored as MKV, which the
// upgrade marked with a size that cannot match, is inspected again.
func TestWebMIsStoredAsWebMAndMarkedFilesAreInspectedAgain(t *testing.T) {
	ff := probe.Find()
	if ff == "" {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffprobe was not found, and LM_REQUIRE_FFMPEG says it must be")
		}
		t.Skip("ffprobe is not installed here")
	}
	media := t.TempDir()
	// Any Matroska file will do: what makes this one WebM is its name.
	src, err := os.ReadFile(filepath.Join("..", "..", "e2e", "fixtures", "Dolby.Sound.Test.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "Web Clip.webm"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	d := open(t)
	lib, _ := Add(d, media)
	Scan(d, lib)
	stored := func() (container string) {
		d.QueryRow(`SELECT container FROM files`).Scan(&container)
		return container
	}
	if res, err := ProbePending(context.Background(), d, ff); err != nil || res.Inspected != 1 || stored() != "webm" {
		t.Fatalf("inspected %+v %v, stored as %q; want webm", res, err, stored())
	}
	// As an earlier version left it. Nothing looks at the file again...
	d.Exec(`UPDATE files SET container='matroska'`)
	if res, _ := ProbePending(context.Background(), d, ff); res.Inspected != 0 || stored() != "matroska" {
		t.Fatalf("an unmarked file was inspected again: %+v", res)
	}
	// ...until the upgrade marks it.
	d.Exec(`UPDATE files SET probed_size=-1`)
	if res, err := ProbePending(context.Background(), d, ff); err != nil || res.Inspected != 1 || stored() != "webm" {
		t.Fatalf("a marked file: inspected %+v %v, stored as %q; want webm", res, err, stored())
	}
	if res, _ := ProbePending(context.Background(), d, ff); res.Inspected != 0 {
		t.Fatalf("it was inspected a third time: %+v", res)
	}
}

func TestListDirs(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "b"), 0o755)
	os.Mkdir(filepath.Join(root, "A"), 0o755)
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	write(t, filepath.Join(root, "file.mp4"), "x")
	l, err := ListDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Dirs) != 2 || l.Dirs[0].Name != "A" || l.Parent == "" {
		t.Fatalf("%+v", l)
	}
	if l, _ := ListDirs(""); len(l.Dirs) == 0 {
		t.Fatal("no starting points")
	}
	if _, err := ListDirs(filepath.Join(root, "nope")); err == nil {
		t.Fatal("listed a folder that does not exist")
	}
}
