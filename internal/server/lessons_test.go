package server

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// These tests hold lessons.go to its rules, each on a clock the test moves.

// lessonEnv is a server with three films, one signed-in browser, and a
// clock that only moves when the test says so.
type lessonEnv struct {
	*env
	clock   *testClock
	pc      *browser
	a, b, c string // three files
}

func newLessonEnv(t *testing.T) *lessonEnv {
	t.Helper()
	e := newEnv(t)
	os.WriteFile(filepath.Join(e.media, "Third Film (2001).mkv"), []byte("third"), 0o644)
	l := &lessonEnv{env: e, clock: e.clock(), pc: e.browser(chromeOnWindows)}
	l.pc.setup()
	rows, err := e.s.db.Query(`SELECT id FROM files ORDER BY rel_path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		files = append(files, id)
	}
	if len(files) != 3 {
		t.Fatalf("%d files in the library, want 3", len(files))
	}
	l.a, l.b, l.c = files[0], files[1], files[2]
	return l
}

// user is the browser's sign-in as a handler sees it.
func (l *lessonEnv) user(b *browser) *User {
	l.t.Helper()
	u := &User{DeviceID: b.device()}
	if err := l.s.db.QueryRow(`SELECT s.id, s.user_id FROM sessions s WHERE s.device_id=? AND s.revoked_at IS NULL ORDER BY s.rowid DESC`, b.device()).Scan(&u.SessionID, &u.ID); err != nil {
		l.t.Fatal(err)
	}
	return u
}

// A kind of picture a device may have trouble with, as a step from package
// playback names it.
var hevc4K = playback.PictureKind{Codec: "hevc", BitDepth: 10, Size: playback.Size4K}

func trouble(k playback.PictureKind) learned {
	return lessonsIn(playback.Step{Lesson: playback.LessonDevice, Device: playback.DeviceLesson{Picture: k}})[0]
}

func (l *lessonEnv) learn(file string, what learned) string {
	l.t.Helper()
	id, err := l.s.learn(l.pc.device(), file, what)
	if err != nil {
		l.t.Fatalf("learn: %v", err)
	}
	return id
}

// row reads a lesson as it is kept, whether or not it still holds.
func (l *lessonEnv) row(id string) (file string, hits int, life time.Duration, left time.Duration) {
	l.t.Helper()
	var f sql.NullString
	var learnedAt, expires int64
	if err := l.s.db.QueryRow(`SELECT file_id, hits, learned_at, expires_at FROM lessons WHERE id=?`, id).Scan(&f, &hits, &learnedAt, &expires); err != nil {
		l.t.Fatalf("lesson %s: %v", id, err)
	}
	return f.String, hits, time.Duration(expires-learnedAt) * time.Second, time.Duration(expires-l.clock.at.Unix()) * time.Second
}

// holds says the lesson is planned with for a file right now.
func (l *lessonEnv) troubled(file string) []playback.PictureKind {
	_, limits := l.s.planFor(l.user(l.pc), true).file(file)
	return limits.Trouble
}

// listed is what GET /api/device shows the browser.
func (l *lessonEnv) listed(b *browser) deviceView {
	l.t.Helper()
	var d deviceView
	w := b.do("GET", "/api/device", nil)
	if w.Code != 200 {
		l.t.Fatalf("GET /api/device: %d %s", w.Code, w.Body)
	}
	decode(l.t, w, &d)
	return d
}

// Rule 2: the first failure counts for that file, a second file for the
// kind. It holds for each of the three kinds of lesson about films.
func TestTheFirstFailureCountsForThatFileAndASecondFileForTheKind(t *testing.T) {
	for name, tc := range map[string]struct {
		what learned
		// seen says the lesson shows in what a film is planned with.
		seen func(c *playback.Caps, l playback.Limits) bool
	}{
		"a picture the device had trouble showing": {trouble(hevc4K),
			func(c *playback.Caps, l playback.Limits) bool {
				return reflect.DeepEqual(l.Trouble, []playback.PictureKind{hevc4K})
			}},
		"a cap on the size of a converted picture": {lessonsIn(playback.Step{Lesson: playback.LessonDevice,
			Device: playback.DeviceLesson{Picture: playback.PictureKind{Codec: "h264", BitDepth: 8, Size: playback.Size1080}, MaxHeight: 720}})[0],
			func(c *playback.Caps, l playback.Limits) bool { return l.MaxHeight == 720 }},
		"subtitles the page could not draw": {lessonsIn(playback.Step{Lesson: playback.LessonDraw, Draw: playback.Drawing{Styled: true}})[0],
			func(c *playback.Caps, l playback.Limits) bool { return !c.Draw.Styled && c.Draw.Picture }},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLessonEnv(t)
			what := tc.what
			// A device whose page says it can draw subtitles.
			if w := l.pc.do("POST", "/api/capabilities", map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true},
				"audio": map[string]bool{"aac": true}, "hls": true, "pageFullscreen": true, "draw": map[string]bool{"styled": true, "picture": true}}); w.Code != 200 {
				t.Fatal(w.Body)
			}
			held := func(file string) bool {
				return tc.seen(l.s.planFor(l.user(l.pc), true).file(file))
			}
			if held(l.a) {
				t.Fatal("before anything is learned, the lesson already shows")
			}

			id := l.learn(l.a, what)
			if file, hits, life, _ := l.row(id); file != l.a || hits != 1 || life != 30*day {
				t.Fatalf("after the first failure the lesson is about file %q, reported %d times, for %v", file, hits, life)
			}
			if !held(l.a) || held(l.b) || held(l.c) {
				t.Errorf("after the first failure the lesson is planned with for the three files: %v %v %v, want only the first", held(l.a), held(l.b), held(l.c))
			}
			// The same file failing again is still one file.
			if again := l.learn(l.a, what); again != id {
				t.Errorf("the same lesson was kept twice: %s and %s", id, again)
			}
			if file, hits, _, _ := l.row(id); file != l.a || hits != 2 {
				t.Errorf("after the same file failed again the lesson is about file %q, reported %d times", file, hits)
			}
			if held(l.b) {
				t.Error("the same file failing twice made it a lesson about every film")
			}
			// A second file: now it is about the kind.
			l.learn(l.b, what)
			if file, hits, _, _ := l.row(id); file != "" || hits != 3 {
				t.Errorf("after a second file failed the lesson is about file %q, reported %d times", file, hits)
			}
			if !held(l.a) || !held(l.b) || !held(l.c) {
				t.Errorf("after a second file failed the lesson is planned with for: %v %v %v, want all", held(l.a), held(l.b), held(l.c))
			}
			// And it stays so.
			l.learn(l.c, what)
			if file, _, _, _ := l.row(id); file != "" {
				t.Errorf("a third file made the lesson about one file again: %q", file)
			}
			if n := l.count(`SELECT count(*) FROM lessons`); n != 1 {
				t.Errorf("%d rows for one lesson", n)
			}
		})
	}
}

// Two kinds of picture are two lessons, each with its own first file.
func TestEachKindOfPictureIsItsOwnLesson(t *testing.T) {
	l := newLessonEnv(t)
	av1 := playback.PictureKind{Codec: "av1", BitDepth: 8, Size: playback.Size1080}
	l.learn(l.a, trouble(hevc4K))
	l.learn(l.b, trouble(av1))
	if a, b, c := l.troubled(l.a), l.troubled(l.b), l.troubled(l.c); len(a) != 1 || a[0] != hevc4K || len(b) != 1 || b[0] != av1 || len(c) != 0 {
		t.Errorf("planned with: %v, %v, %v", a, b, c)
	}
	// 12-bit is the same lesson as 10-bit: all a lesson asks is "more than 8".
	l.learn(l.c, trouble(playback.PictureKind{Codec: "hevc", BitDepth: 12, Size: playback.Size4K}))
	if n := l.count(`SELECT count(*) FROM lessons`); n != 2 {
		t.Errorf("%d lessons, want 2", n)
	}
	if got := l.troubled(l.b); len(got) != 2 {
		t.Errorf("after a second file with 4K HEVC the second film is planned with %v", got)
	}
	// A format name that ffprobe would never give is not kept at all.
	for _, bad := range []string{"", "HEVC", "h264; drop", "a/b", strings.Repeat("x", 40), "Some Film (2020)"} {
		if got := lessonsIn(playback.Step{Lesson: playback.LessonDevice, Device: playback.DeviceLesson{Picture: playback.PictureKind{Codec: bad, BitDepth: 8}}}); len(got) != 0 {
			t.Errorf("a picture called %q would be kept as %+v", bad, got)
		}
	}
}

// Rules 4 and 6: thirty days; then one quiet retry; learned again after
// that, twice as long, up to 120 days; learned again while it holds, as
// long as before from that day.
func TestALessonLastsThirtyDaysAndTwiceAsLongWhenLearnedAgain(t *testing.T) {
	l := newLessonEnv(t)
	id := l.learn(l.a, trouble(hevc4K))
	l.clock.forward(30*day - time.Second)
	if len(l.troubled(l.a)) != 1 || len(l.listed(l.pc).Lessons) != 1 {
		t.Fatal("a second before its thirty days are over, the lesson no longer holds")
	}
	l.clock.forward(time.Second)
	// It has run out: the next such film is tried as it is, and nothing is
	// listed. Its row is still there.
	if len(l.troubled(l.a)) != 0 || len(l.listed(l.pc).Lessons) != 0 {
		t.Fatal("after thirty days the lesson still holds")
	}
	if n := l.count(`SELECT count(*) FROM lessons WHERE id=?`, id); n != 1 {
		t.Fatal("a lesson that ran out was forgotten at once, so learning it again cannot last longer")
	}
	// The retry fails.
	l.clock.forward(5 * day)
	for i, want := range []time.Duration{60 * day, 120 * day, 120 * day} {
		if again := l.learn(l.a, trouble(hevc4K)); again != id {
			t.Fatalf("learned again, the lesson is another row")
		}
		file, hits, life, left := l.row(id)
		if life != want || left != want || file != l.a || hits != i+2 {
			t.Errorf("learned again for the %d. time: lasts %v with %v left, about file %q, reported %d times; want %v", i+2, life, left, file, hits, want)
		}
		if len(l.troubled(l.a)) != 1 {
			t.Errorf("learned again, the lesson does not hold")
		}
		l.clock.forward(want + day)
	}
	// Learned again while it holds: as long as it lasted, from that day.
	l.learn(l.a, trouble(hevc4K))
	l.clock.forward(10 * day)
	l.learn(l.a, trouble(hevc4K))
	if _, _, life, left := l.row(id); life != 120*day || left != 120*day {
		t.Errorf("learned again while it held, the lesson lasts %v with %v left, want 120 days from today", life, left)
	}
	// A second file after the first one's lesson ran out is both: about
	// the kind now, and longer.
	other := l.learn(l.b, trouble(playback.PictureKind{Codec: "av1", BitDepth: 8, Size: playback.Size1080}))
	l.clock.forward(31 * day)
	l.learn(l.c, trouble(playback.PictureKind{Codec: "av1", BitDepth: 8, Size: playback.Size1080}))
	if file, _, life, _ := l.row(other); file != "" || life != 60*day {
		t.Errorf("a second file after the lesson ran out: about file %q for %v, want about the kind for 60 days", file, life)
	}
}

// Rule 5: a lesson about the connection holds for every film at once, for a
// day, and never longer however often it is learned.
func TestALessonAboutTheConnectionLastsADay(t *testing.T) {
	l := newLessonEnv(t)
	slow := lessonsIn(playback.Step{Lesson: playback.LessonNetwork, Network: playback.NetworkLesson{MaxRate: 5_000_000, Hours: 24}})[0]
	rate := func(file string) int {
		_, limits := l.s.planFor(l.user(l.pc), true).file(file)
		return limits.MaxRate
	}
	id := l.learn(l.a, slow)
	if file, _, life, _ := l.row(id); file != "" || life != 24*time.Hour {
		t.Errorf("the lesson is about file %q for %v, want every film for a day", file, life)
	}
	if rate(l.a) != 5_000_000 || rate(l.c) != 5_000_000 {
		t.Errorf("the cap is %d for the film that stalled and %d for another", rate(l.a), rate(l.c))
	}
	l.clock.forward(23 * time.Hour)
	l.learn(l.b, slow) // while it holds: a day from now
	if _, hits, life, left := l.row(id); life != 24*time.Hour || left != 24*time.Hour || hits != 2 {
		t.Errorf("learned again while it held: %v with %v left, reported %d times", life, left, hits)
	}
	l.clock.forward(24 * time.Hour)
	if rate(l.a) != 0 {
		t.Error("the cap outlived its day")
	}
	l.clock.forward(time.Hour)
	l.learn(l.a, slow) // after it ran out: a day again, not two
	if _, _, life, _ := l.row(id); life != 24*time.Hour {
		t.Errorf("learned again after it ran out, the lesson lasts %v", life)
	}
	// A tighter cap is a lesson of its own, and the tighter one counts.
	l.learn(l.a, lessonsIn(playback.Step{Lesson: playback.LessonNetwork, Network: playback.NetworkLesson{MaxRate: 2_500_000, Hours: 24}})[0])
	if rate(l.b) != 2_500_000 {
		t.Errorf("with both caps learned, films are planned with %d", rate(l.b))
	}
}

// The viewer's own word that HDR colours look wrong on a device: thirty
// days for every film, every time it is said, and taken back by saying so.
func TestTheViewersWordAboutHDRColours(t *testing.T) {
	l := newLessonEnv(t)
	noHDR := func(b *browser) bool {
		_, limits := l.s.planFor(l.user(b), true).file(l.a)
		return limits.NoHDR
	}
	var said struct {
		OK     bool
		On     bool
		Lesson string
	}
	w := l.pc.do("POST", "/api/device/hdr-wrong", map[string]bool{"on": true})
	decode(t, w, &said)
	if w.Code != 200 || !said.OK || !said.On || said.Lesson == "" {
		t.Fatalf("saying so: %d %s", w.Code, w.Body)
	}
	seen := l.listed(l.pc)
	if !noHDR(l.pc) || !seen.HDRWrong || len(seen.Lessons) != 1 || seen.Lessons[0].ID != said.Lesson || seen.Lessons[0].Kind != "hdr" {
		t.Errorf("after saying so: planned with %v, listed %+v", noHDR(l.pc), seen)
	}
	if file, _, life, _ := l.row(said.Lesson); file != "" || life != 30*day {
		t.Errorf("the lesson is about file %q for %v", file, life)
	}
	// It is about this device only.
	phone := l.signedIn(safariOnIPhone)
	if noHDR(phone) || l.listed(phone).HDRWrong {
		t.Error("what was said on one device holds on another")
	}
	// Said again after it ran out: thirty days again, not sixty.
	l.clock.forward(31 * day)
	if noHDR(l.pc) || l.listed(l.pc).HDRWrong {
		t.Error("the word outlived its thirty days")
	}
	l.pc.do("POST", "/api/device/hdr-wrong", map[string]bool{"on": true})
	if _, _, life, _ := l.row(said.Lesson); life != 30*day {
		t.Errorf("said again, it lasts %v", life)
	}
	// Taken back.
	w = l.pc.do("POST", "/api/device/hdr-wrong", map[string]bool{"on": false})
	decode(t, w, &said)
	if w.Code != 200 || said.On || noHDR(l.pc) || l.listed(l.pc).HDRWrong || l.count(`SELECT count(*) FROM lessons`) != 0 {
		t.Errorf("after taking it back: %d %s, planned with %v", w.Code, w.Body, noHDR(l.pc))
	}
	// Taking back what was never said is not an error, and nonsense is.
	if w := l.pc.do("POST", "/api/device/hdr-wrong", map[string]bool{"on": false}); w.Code != 200 {
		t.Errorf("taking back nothing: %d", w.Code)
	}
	if w := l.pc.do("POST", "/api/device/hdr-wrong", map[string]string{"on": "yes"}); w.Code != 400 {
		t.Errorf("nonsense: %d", w.Code)
	}
}

// Rule 6: a lesson that ran out is remembered for a year, and then not.
func TestALessonThatRanOutIsRememberedForAYear(t *testing.T) {
	l := newLessonEnv(t)
	recent := l.learn(l.a, trouble(hevc4K))
	l.clock.forward(30*day + 364*day)
	l.learn(l.a, trouble(hevc4K))
	if _, hits, life, _ := l.row(recent); life != 60*day || hits != 2 {
		t.Errorf("learned again 364 days after it ran out: %v, reported %d times; want remembered, so 60 days", life, hits)
	}
	// Another lesson runs out and nobody learns it again for over a year.
	av1 := trouble(playback.PictureKind{Codec: "av1", BitDepth: 8, Size: playback.Size720})
	old := l.learn(l.b, av1)
	l.clock.forward(30*day + 366*day)
	l.learn(l.c, trouble(playback.PictureKind{Codec: "vp9", BitDepth: 8, Size: playback.Size720})) // anything learned sweeps
	if n := l.count(`SELECT count(*) FROM lessons WHERE id=?`, old); n != 0 {
		t.Error("a lesson that ran out over a year ago is still kept")
	}
	again := l.learn(l.c, av1)
	if file, hits, life, _ := l.row(again); again == old || file != l.c || hits != 1 || life != 30*day {
		t.Errorf("learned afresh: about file %q, reported %d times, for %v", file, hits, life)
	}
}

// Rule 7: forgetting removes the lesson altogether. The viewer can forget
// what was learned about the device in their hand and nobody else's.
func TestForgettingALessonStartsAfresh(t *testing.T) {
	l := newLessonEnv(t)
	id := l.learn(l.a, trouble(hevc4K))
	l.learn(l.b, trouble(hevc4K))
	l.clock.forward(31 * day)
	l.learn(l.c, trouble(hevc4K)) // about the kind, for 60 days
	if len(l.troubled(l.c)) != 1 {
		t.Fatal("the lesson does not hold")
	}

	// Another device cannot forget it, whoever is signed in on it; nor can
	// anyone without a sign-in.
	phone := l.signedIn(safariOnIPhone)
	l.member("kid", "member password")
	tablet := l.browser("Mozilla/5.0 (Linux; Android 13; SM-X710) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36")
	tablet.signIn("kid", "member password")
	for who, b := range map[string]*browser{"the owner's phone": phone, "another user's tablet": tablet, "nobody": l.browser(chromeOnWindows)} {
		w := b.do("DELETE", "/api/device/lessons/"+id, nil)
		if want := map[bool]int{true: 401, false: 404}[who == "nobody"]; w.Code != want {
			t.Errorf("%s forgetting the PC's lesson: %d %s, want %d", who, w.Code, w.Body, want)
		}
	}
	if len(l.troubled(l.c)) != 1 {
		t.Fatal("another device forgot this device's lesson")
	}
	// A write needs the page's own header, as everywhere.
	if w := l.ask("DELETE", "/api/device/lessons/"+id, remote, "192.168.1.10:8177", l.pc.session(), nil, ""); w.Code != 403 {
		t.Errorf("forgetting without the page's header: %d", w.Code)
	}

	if w := l.pc.do("DELETE", "/api/device/lessons/"+id, nil); w.Code != 200 {
		t.Fatalf("forgetting: %d %s", w.Code, w.Body)
	}
	if len(l.troubled(l.c)) != 0 || len(l.listed(l.pc).Lessons) != 0 || l.count(`SELECT count(*) FROM lessons`) != 0 {
		t.Error("the lesson is still there after it was forgotten")
	}
	if w := l.pc.do("DELETE", "/api/device/lessons/"+id, nil); w.Code != 404 {
		t.Errorf("forgetting it twice: %d", w.Code)
	}
	// Learned again, it starts from the beginning: one file, thirty days.
	again := l.learn(l.c, trouble(hevc4K))
	if file, hits, life, _ := l.row(again); file != l.c || hits != 1 || life != 30*day || again == id {
		t.Errorf("learned again after forgetting: about file %q, reported %d times, for %v", file, hits, life)
	}
}

// A lesson about one file goes when the file's row does: nothing is left
// pointing at a film that is no longer in the library, and the lesson does
// not turn into one about every film.
func TestALessonAboutOneFileGoesWithTheFile(t *testing.T) {
	l := newLessonEnv(t)
	l.learn(l.a, trouble(hevc4K))
	l.learn(l.a, lessonsIn(playback.Step{Lesson: playback.LessonNetwork, Network: playback.NetworkLesson{MaxRate: 5_000_000, Hours: 24}})[0])
	l.must(`DELETE FROM files WHERE id=?`, l.a)
	held, err := l.s.lessonsOf(l.pc.device())
	if err != nil || len(held) != 1 || held[0].Kind != lessonRate {
		t.Fatalf("after the file was removed the device's lessons are %+v, %v", held, err)
	}
	if got := l.troubled(l.b); len(got) != 0 {
		t.Errorf("the lesson about the removed file now holds for other films: %v", got)
	}
	// A lesson cannot be kept about a file that is not there.
	if _, err := l.s.learn(l.pc.device(), l.a, trouble(hevc4K)); err == nil {
		t.Error("a lesson was kept about a file that is not in the library")
	}
	if _, err := l.s.learn(l.pc.device(), "", trouble(hevc4K)); err == nil {
		t.Error("a lesson about a kind of film was kept without a file")
	}
	if _, err := l.s.learn("", l.b, trouble(hevc4K)); err == nil {
		t.Error("a lesson was kept without a device")
	}
}

// One device cannot fill the table, however many kinds of picture it claims
// to have had trouble with.
func TestADeviceCannotFillTheTable(t *testing.T) {
	l := newLessonEnv(t)
	last := ""
	for i := 0; i < maxLessons+25; i++ {
		l.clock.forward(time.Minute)
		last = l.learn(l.a, trouble(playback.PictureKind{Codec: fmt.Sprintf("codec%d", i), BitDepth: 8, Size: playback.Size1080}))
	}
	if n := l.count(`SELECT count(*) FROM lessons`); n != maxLessons {
		t.Errorf("%d lessons kept for one device, want %d", n, maxLessons)
	}
	if n := l.count(`SELECT count(*) FROM lessons WHERE id=?`, last); n != 1 {
		t.Error("the newest lesson was the one dropped")
	}
	// Another device's lessons are not what makes room.
	phone := l.signedIn(safariOnIPhone)
	if _, err := l.s.learn(phone.device(), l.a, trouble(hevc4K)); err != nil {
		t.Fatal(err)
	}
	l.learn(l.b, trouble(hevc4K))
	if n := l.count(`SELECT count(*) FROM lessons WHERE device_id=?`, phone.device()); n != 1 {
		t.Error("one device's lessons pushed out another's")
	}
}

// Every lesson that holds is worked into what the device's films are
// planned with, and nothing else is: not another device's lessons, and not
// the report the device itself made of what it plays.
func TestLessonsAreWorkedIntoWhatFilmsArePlannedWith(t *testing.T) {
	l := newLessonEnv(t)
	draws := map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true},
		"hls": true, "pageFullscreen": true, "draw": map[string]bool{"styled": true, "picture": true}}
	if w := l.pc.do("POST", "/api/capabilities", draws); w.Code != 200 {
		t.Fatal(w.Body)
	}
	u := l.user(l.pc)
	plain, limits := l.s.planFor(u, false).file(l.a)
	if !plain.Draw.Styled || !plain.Draw.Picture || limits.MaxHeight != 0 || limits.MaxRate != 0 || limits.NoHDR || len(limits.Trouble) != 0 || limits.Lower != 0 {
		t.Fatalf("with nothing learned, films are planned with %+v %+v", plain, limits)
	}
	for _, st := range []playback.Step{
		{Lesson: playback.LessonDevice, Device: playback.DeviceLesson{Picture: hevc4K}},
		{Lesson: playback.LessonDevice, Device: playback.DeviceLesson{Picture: playback.PictureKind{Codec: "h264", BitDepth: 8, Size: playback.Size1080}, MaxHeight: 720}},
		{Lesson: playback.LessonDevice, Device: playback.DeviceLesson{Picture: playback.PictureKind{Codec: "h264", BitDepth: 8, Size: playback.Size720}, MaxHeight: 480}},
		{Lesson: playback.LessonNetwork, Network: playback.NetworkLesson{MaxRate: 5_000_000, Hours: 24}},
		{Lesson: playback.LessonDraw, Draw: playback.Drawing{Styled: true, Picture: true}},
		// A step's own "start again lighter" belongs to the play that took
		// it, and must not be kept.
		{Lesson: playback.LessonPC, Lower: 3, PC: playback.SpeedLesson{Class: playback.ClassHD, Rung: 2, Encoder: "x264", Speed: 0.5}},
	} {
		for _, what := range lessonsIn(st) {
			l.learn(l.a, what)
			l.learn(l.b, what) // a second file: about the kind
		}
	}
	l.pc.do("POST", "/api/device/hdr-wrong", map[string]bool{"on": true})

	caps, limits := l.s.planFor(u, false).file(l.c)
	if caps.Draw.Styled || caps.Draw.Picture || !caps.PageFullscreen || !caps.Video["h264"] {
		t.Errorf("the device is planned for as %+v", caps)
	}
	if limits.MaxHeight != 480 || limits.MaxRate != 5_000_000 || !limits.NoHDR || len(limits.Trouble) != 1 || limits.Trouble[0] != hevc4K || limits.Lower != 0 {
		t.Errorf("films are planned with %+v", limits)
	}
	// What the device itself reported is as it was.
	if again := l.s.capsFor(u); !again.Draw.Styled || !again.Draw.Picture {
		t.Errorf("a lesson changed the device's own report: %+v", again.Draw)
	}
	// Another device, with the same report, is planned for as before.
	phone := l.signedIn(safariOnIPhone)
	phone.do("POST", "/api/capabilities", draws)
	caps, limits = l.s.planFor(l.user(phone), false).file(l.c)
	if !caps.Draw.Styled || limits.MaxHeight != 0 || limits.MaxRate != 0 || limits.NoHDR || len(limits.Trouble) != 0 {
		t.Errorf("another device is planned for with this one's lessons: %+v %+v", caps.Draw, limits)
	}
	// A request without a device is planned for as if nothing was learned.
	none := *u
	none.DeviceID = ""
	if caps, limits = l.s.planFor(&none, false).file(l.c); !caps.Draw.Styled || limits.MaxHeight != 0 || len(limits.Trouble) != 0 {
		t.Errorf("a request without a device is planned for with %+v %+v", caps.Draw, limits)
	}
}

// What was learned is read once for a whole list, however many films the
// list has: the lists are read on every page, and a library has thousands.
func TestWhatWasLearnedIsReadOnceForAWholeList(t *testing.T) {
	l := newLessonEnv(t)
	l.learn(l.a, trouble(hevc4K))
	list := func() (films, reads int) {
		before := l.clock.read()
		var out struct{ Items []itemView }
		w := l.pc.do("GET", "/api/movies", nil)
		if w.Code != 200 {
			t.Fatalf("the list: %d %s", w.Code, w.Body)
		}
		decode(t, w, &out)
		return len(out.Items), l.clock.read() - before
	}
	few, readsForFew := list()
	for i := 0; i < 60; i++ {
		os.WriteFile(filepath.Join(l.media, fmt.Sprintf("Another Film %02d (2010).mkv", i)), []byte{byte(i)}, 0o644)
	}
	if w := l.pc.do("POST", "/api/libraries/scan", nil); w.Code != 200 {
		t.Fatal(w.Body)
	}
	many, readsForMany := list()
	if many != few+60 {
		t.Fatalf("the list has %d films after 60 were added to %d", many, few)
	}
	// Everything that reads what was learned looks at the clock, so the
	// clock tells how often that happened.
	if readsForFew == 0 || readsForMany != readsForFew {
		t.Errorf("listing %d films looked at the clock %d times and listing %d films %d times: what was learned is read per film", few, readsForFew, many, readsForMany)
	}
}

// Each lesson is said in one sentence that names kinds, never a film.
func TestTheSentenceForEachLesson(t *testing.T) {
	for _, tc := range []struct {
		l          lesson
		canConvert bool
		want       string
	}{
		{lesson{Kind: lessonPicture, Value: "hevc/10/4k"}, true, "This device had trouble showing 4K 10-bit HEVC (H.265) films, so they are converted for it."},
		{lesson{Kind: lessonPicture, Value: "hevc/8/4k"}, true, "This device had trouble showing 4K HEVC (H.265) films, so they are converted for it."},
		{lesson{Kind: lessonPicture, Value: "hevc/10/4k", File: "f"}, true, "This device had trouble showing one 4K 10-bit HEVC (H.265) film, so that film is converted for it."},
		{lesson{Kind: lessonPicture, Value: "av1/8/1080p"}, true, "This device had trouble showing 1080p AV1 films (or larger), so they are converted for it."},
		{lesson{Kind: lessonPicture, Value: "h264/10/480p"}, true, "This device had trouble showing 10-bit H.264 films, so they are converted for it."},
		{lesson{Kind: lessonPicture, Value: "vp9/8/"}, true, "This device had trouble showing VP9 films, so they are converted for it."},
		{lesson{Kind: lessonPicture, Value: "hevc/10/4k"}, false, "This device had trouble showing 4K 10-bit HEVC (H.265) films, but this PC cannot convert pictures, so nothing is changed for now."},
		{lesson{Kind: lessonPicture, Value: "hevc/10/4k", File: "f"}, false, "This device had trouble showing one 4K 10-bit HEVC (H.265) film, but this PC cannot convert pictures, so nothing is changed for now."},
		{lesson{Kind: lessonHeight, Value: "720"}, true, "This device had trouble showing films that the PC had converted for it, so they are now made no larger than 720p."},
		{lesson{Kind: lessonHeight, Value: "480", File: "f"}, true, "This device had trouble showing one film that the PC had converted for it, so that film is now made no larger than 480p."},
		{lesson{Kind: lessonRate, Value: "5000000"}, true, "Films were reaching this device too slowly, so for a day films that need more than 5 megabits a second are converted to a lighter picture for it."},
		{lesson{Kind: lessonRate, Value: "2500000"}, true, "Films were reaching this device too slowly, so for a day films that need more than 2.5 megabits a second are converted to a lighter picture for it."},
		{lesson{Kind: lessonRate, Value: "5000000"}, false, "Films were reaching this device too slowly, but this PC cannot convert pictures, so nothing is changed for now."},
		{lesson{Kind: lessonHDR}, true, "HDR colours were reported to look wrong on this device, so HDR films are converted to normal colour for it."},
		{lesson{Kind: lessonHDR}, false, "HDR colours were reported to look wrong on this device, but this PC cannot convert pictures, so nothing is changed for now."},
		{lesson{Kind: lessonDraw, Value: "styled"}, true, "This device could not draw subtitles that have their own design over the film, so they are put into the picture or shown as plain text."},
		{lesson{Kind: lessonDraw, Value: "picture", File: "f"}, false, "This device could not draw subtitles that are stored as pictures over one film, so for that film they are put into the picture."},
		{lesson{Kind: "something newer", Value: "x"}, true, "Something was learned about this device that this version of the app cannot describe."},
		{lesson{Kind: lessonPicture, Value: "not a kind"}, true, "Something was learned about this device that this version of the app cannot describe."},
	} {
		if got := lessonText(tc.l, tc.canConvert); got != tc.want {
			t.Errorf("%+v (this PC converts: %v):\n got %q\nwant %q", tc.l, tc.canConvert, got, tc.want)
		}
	}
}
