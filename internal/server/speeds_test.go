package server

import (
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// These tests cover the PC's speed table (speeds.go). They need no ffmpeg:
// the server is told it has a converter that can convert pictures, which
// is all that planning asks.

// newPretendEnv is a server that believes it has a converter of the given
// version, able to convert pictures. Nothing can really be converted: the
// program it would start is not there.
func newPretendEnv(t *testing.T, version string) *env {
	t.Helper()
	data, media := t.TempDir(), t.TempDir()
	d, err := db.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := New(Config{DB: d, DataDir: data, Version: "test", Port: 8177, Assets: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}},
		FFmpeg: filepath.Join(t.TempDir(), "no-such-ffmpeg")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := &env{t: t, s: s, media: media}
	e.pretendConverter(version)
	return e
}

// pretendConverter replaces what the converter said about itself.
func (e *env) pretendConverter(version string) {
	e.s.abilities() // wait for the real question, asked at start, to be over
	c := e.s.helpers.convert
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked, c.can, c.version = true, hls.Abilities{X264: true, Basics: true}, version
}

// forgetSpeedsInMemory makes the server read its speed table from the
// settings again, as it does after a restart.
func (e *env) forgetSpeedsInMemory() {
	b := &e.s.helpers.speeds
	b.mu.Lock()
	defer b.mu.Unlock()
	b.key, b.byEncoder = "", nil
}

// An old film that has to be converted on any device: the setting it is
// planned at shows what the speed table says.
var oldFilmMedia = playback.Media{Probed: true, Container: "matroska",
	Video: []playback.Track{{Codec: "mpeg4", BitDepth: 8, Width: 1920, Height: 1080, FrameRate: 25}},
	Audio: []playback.Track{{Codec: "aac", Channels: 2}}}

var plainDevice = &playback.Caps{Containers: map[string]bool{"mp4": true, "matroska": true}, Video: map[string]bool{"h264": true},
	Audio: map[string]bool{"aac": true}, HLS: true, Tall: map[string]bool{}}

// rungNow is the setting the old film is converted at with the speed table
// as it stands.
func (e *env) rungNow() int {
	e.t.Helper()
	l := e.s.limits(nil)
	l.Speed = e.s.speeds(l.Encoder)
	p := playback.DecideFor(oldFilmMedia, plainDevice, playback.Want{Audio: -1, Subtitle: -1}, l)
	if p.Method != playback.Convert {
		e.t.Fatalf("the old film is not converted: %+v", p)
	}
	return p.Video.Rung
}

func seen(class playback.Class, rung int, speed float64) playback.SpeedLesson {
	return playback.SpeedLesson{Class: class, Rung: rung, Encoder: "x264", Speed: speed}
}

// What a real play showed lowers the table, never raises it, and counts
// for thirty days.
func TestASpeedSeenInARealPlayLowersTheTableForThirtyDays(t *testing.T) {
	e := newPretendEnv(t, "9.9")
	clock := e.clock()
	at := func(class playback.Class, rung int) float64 { return e.s.speeds("")[class][rung-1] }
	if e.rungNow() != 1 || at(playback.ClassHD, 1) != 0 {
		t.Fatalf("with nothing measured the film is converted at setting %d", e.rungNow())
	}
	if !e.s.sawSpeed(seen(playback.ClassHD, 1, 0.8)) || at(playback.ClassHD, 1) != 0.8 {
		t.Fatalf("a speed seen at a setting never measured was not taken: %v", at(playback.ClassHD, 1))
	}
	if e.rungNow() != 2 {
		t.Errorf("after setting 1 was seen too slow the film is converted at setting %d, want 2", e.rungNow())
	}
	// Never raised; lowered further by a slower play.
	clock.forward(10 * day)
	if e.s.sawSpeed(seen(playback.ClassHD, 1, 0.9)) || at(playback.ClassHD, 1) != 0.8 {
		t.Errorf("a faster play raised the figure to %v", at(playback.ClassHD, 1))
	}
	if !e.s.sawSpeed(seen(playback.ClassHD, 1, 0.6)) || at(playback.ClassHD, 1) != 0.6 {
		t.Errorf("a slower play did not lower the figure: %v", at(playback.ClassHD, 1))
	}
	// Other classes of film and other settings are untouched.
	if at(playback.ClassHD, 2) != 0 || at(playback.Class4K, 1) != 0 {
		t.Errorf("one figure lowered others: %v", e.s.speeds(""))
	}
	// Thirty days after it was last seen it no longer counts.
	clock.forward(30*day - time.Second)
	if at(playback.ClassHD, 1) != 0.6 {
		t.Error("the figure went before its thirty days were over")
	}
	clock.forward(time.Second)
	if at(playback.ClassHD, 1) != 0 || e.rungNow() != 1 {
		t.Errorf("after thirty days the figure is still %v and the film is converted at setting %d", at(playback.ClassHD, 1), e.rungNow())
	}

	// With a measurement, a real play lowers the measured figure and
	// gives it back when its thirty days are over.
	if err := e.s.setMeasuredSpeeds("", playback.Speeds{playback.ClassHD: {4, 5, 6, 7, 10}, "no such class": {1, 1, 1, 1, 1}}); err != nil {
		t.Fatal(err)
	}
	if at(playback.ClassHD, 1) != 4 || at(playback.ClassHD, 5) != 10 || len(e.s.speeds("")) != 1 {
		t.Errorf("the measured table is read back as %v", e.s.speeds(""))
	}
	if e.s.sawSpeed(seen(playback.ClassHD, 1, 5)) || !e.s.sawSpeed(seen(playback.ClassHD, 1, 1.2)) || at(playback.ClassHD, 1) != 1.2 {
		t.Errorf("against a measured 4: %v", at(playback.ClassHD, 1))
	}
	if e.rungNow() != 2 {
		t.Errorf("with setting 1 seen at 1.2 the film is converted at setting %d", e.rungNow())
	}
	clock.forward(31 * day)
	if at(playback.ClassHD, 1) != 4 || e.rungNow() != 1 {
		t.Errorf("after thirty days the measured figure is not back: %v", at(playback.ClassHD, 1))
	}
	// A new measurement replaces the old one and leaves what plays showed.
	e.s.sawSpeed(seen(playback.ClassHD, 2, 2))
	if err := e.s.setMeasuredSpeeds("x264", playback.Speeds{playback.ClassHD: {3, 3, 3, 0, math.Inf(1)}}); err != nil {
		t.Fatal(err)
	}
	if got := e.s.speeds("")[playback.ClassHD]; got != [playback.NumRungs]float64{3, 2, 3, 0, 0} {
		t.Errorf("after measuring again the table is %v", got)
	}

	// What is not a speed is not taken.
	before := e.s.setting(e.s.speedKey())
	for name, bad := range map[string]playback.SpeedLesson{
		"no speed": seen(playback.ClassHD, 3, 0), "a negative speed": seen(playback.ClassHD, 3, -1), "not a number": seen(playback.ClassHD, 3, math.NaN()),
		"endless": seen(playback.ClassHD, 3, math.Inf(1)), "setting 0": seen(playback.ClassHD, 0, 1), "setting 6": seen(playback.ClassHD, 6, 1),
		"a class there is none of": seen("films", 3, 1),
	} {
		if e.s.sawSpeed(bad) {
			t.Errorf("%s was taken for a speed", name)
		}
	}
	if e.s.setting(e.s.speedKey()) != before {
		t.Error("something that was not a speed changed the table")
	}
}

// The table belongs to one converter on one PC. It survives a restart, and
// is not used for another version of the converter.
func TestTheSpeedTableIsKeptForThisConverterOnThisPC(t *testing.T) {
	e := newPretendEnv(t, "9.9")
	e.clock()
	e.s.sawSpeed(seen(playback.Class4KHDR, 3, 0.7))
	e.s.sawSpeed(playback.SpeedLesson{Class: playback.ClassHD, Rung: 1, Encoder: "h264_nvenc", Speed: 0.5})
	key := "speed:9.9:" + strconv.Itoa(runtime.NumCPU())
	if raw := e.s.setting(key); !strings.Contains(raw, `"4k-hdr"`) || !strings.Contains(raw, "0.7") || !strings.Contains(raw, "h264_nvenc") {
		t.Fatalf("the table is not kept under %s: %q", key, raw)
	}
	// Each encoder has figures of its own.
	if e.s.speeds("")[playback.ClassHD][0] != 0 || e.s.speeds("h264_nvenc")[playback.ClassHD][0] != 0.5 || e.s.speeds("x264")[playback.Class4KHDR][2] != 0.7 {
		t.Errorf("the encoders' figures are mixed up: %v and %v", e.s.speeds(""), e.s.speeds("h264_nvenc"))
	}
	// After a restart it is read back.
	e.forgetSpeedsInMemory()
	if e.s.speeds("")[playback.Class4KHDR][2] != 0.7 {
		t.Error("the table did not survive a restart")
	}
	// Another version of the converter starts with nothing, and the old
	// table goes when the new one is first written.
	e.pretendConverter("10.0")
	if got := e.s.speeds(""); len(got) != 0 {
		t.Errorf("a new converter is planned with the old one's speeds: %v", got)
	}
	if e.s.setting(key) == "" {
		t.Error("the old table went before there was a new one")
	}
	e.s.sawSpeed(seen(playback.ClassHD, 2, 1.1))
	if n := e.count(`SELECT count(*) FROM settings WHERE key LIKE 'speed:%'`); n != 1 || e.s.setting(key) != "" {
		t.Errorf("%d speed tables are kept after the converter changed", n)
	}
	// A table that cannot be read is no table, not a crash.
	e.must(`UPDATE settings SET value=? WHERE key LIKE 'speed:%'`, []byte(`{"x264": null, "broken": `))
	e.forgetSpeedsInMemory()
	if got := e.s.speeds(""); len(got) != 0 {
		t.Errorf("a table that cannot be read gave %v", got)
	}
	e.must(`UPDATE settings SET value=? WHERE key LIKE 'speed:%'`, []byte(`{"x264": null}`))
	e.forgetSpeedsInMemory()
	if !e.s.sawSpeed(seen(playback.ClassHD, 2, 1.1)) || e.s.speeds("")[playback.ClassHD][1] != 1.1 {
		t.Error("a table with an empty entry could not be written to")
	}
	// A PC with no converter has no speeds and takes none.
	plain := newEnv(t)
	if plain.s.sawSpeed(seen(playback.ClassHD, 1, 0.5)); plain.s.speeds("") != nil || len(plain.s.speedLessons()) != 0 {
		t.Error("a PC without a converter has a speed table")
	}
}

// The owner is shown what real plays taught about the PC's speed, in a
// sentence, and can forget it. Nobody else can.
func TestTheOwnerSeesAndForgetsWhatWasLearnedAboutThePCsSpeed(t *testing.T) {
	e := newPretendEnv(t, "9.9")
	clock := e.clock()
	owner := e.browser(chromeOnWindows)
	owner.setup()
	e.s.sawSpeed(seen(playback.ClassHD, 1, 0.84))
	e.s.sawSpeed(seen(playback.Class4KHDR, 3, 0.999))
	var list struct {
		PC struct{ Lessons []lessonView }
	}
	w := owner.do("GET", "/api/devices", nil)
	decode(t, w, &list)
	if w.Code != 200 || len(list.PC.Lessons) != 2 {
		t.Fatalf("the owner's list: %d %s", w.Code, w.Body)
	}
	first, second := list.PC.Lessons[0], list.PC.Lessons[1]
	if first.ID != "x264.hd.1" || first.Kind != "speed" || first.Text != "While a film played, this PC converted only 0.8 seconds of it each second (films up to 1080p, at setting 1 of 5), so a lighter setting is picked for such films." {
		t.Errorf("the first lesson: %+v", first)
	}
	// A speed just under 1 must not read as 1.0.
	if second.ID != "x264.4k-hdr.3" || !strings.Contains(second.Text, "converted only 0.9 seconds of it each second (4K HDR films, at setting 3 of 5)") {
		t.Errorf("the second lesson: %+v", second)
	}
	if first.LearnedAt != stamp(clock.at.Unix()) || first.ExpiresAt != stamp(clock.at.Add(30*day).Unix()) {
		t.Errorf("learned %s, runs out %s", first.LearnedAt, first.ExpiresAt)
	}

	// Someone who is not the owner sees and forgets none of it.
	e.member("kid", "member password")
	kid := e.browser(safariOnIPhone)
	kid.signIn("kid", "member password")
	for _, try := range [][2]string{{"GET", "/api/devices"}, {"DELETE", "/api/devices/pc/lessons/x264.hd.1"}} {
		if w := kid.do(try[0], try[1], nil); w.Code != 403 {
			t.Errorf("%s %s by someone who is not the owner: %d", try[0], try[1], w.Code)
		}
		if w := e.browser(chromeOnWindows).do(try[0], try[1], nil); w.Code != 401 {
			t.Errorf("%s %s without a sign-in: %d", try[0], try[1], w.Code)
		}
	}
	if e.rungNow() != 2 {
		t.Fatalf("the film is converted at setting %d", e.rungNow())
	}
	for _, bad := range []string{"x264.hd.9", "x264.hd.0", "x264.films.1", "nvenc.hd.1", "hd.1", "x264.hd", "x264.hd.one", "x264.4k-hdr.1", strings.Repeat("a", 200)} {
		if w := owner.do("DELETE", "/api/devices/pc/lessons/"+bad, nil); w.Code != 404 {
			t.Errorf("forgetting %q: %d", bad, w.Code)
		}
	}
	if w := owner.do("DELETE", "/api/devices/pc/lessons/x264.hd.1", nil); w.Code != 200 {
		t.Fatalf("forgetting: %d %s", w.Code, w.Body)
	}
	if e.rungNow() != 1 {
		t.Errorf("after forgetting, the film is converted at setting %d", e.rungNow())
	}
	if w := owner.do("DELETE", "/api/devices/pc/lessons/x264.hd.1", nil); w.Code != 404 {
		t.Errorf("forgetting it twice: %d", w.Code)
	}
	// It is forgotten for good, not only in memory.
	e.forgetSpeedsInMemory()
	list.PC.Lessons = nil
	decode(t, owner.do("GET", "/api/devices", nil), &list)
	if len(list.PC.Lessons) != 1 || list.PC.Lessons[0].ID != "x264.4k-hdr.3" {
		t.Errorf("after forgetting one, the list is %+v", list.PC.Lessons)
	}
	// What ran out is not listed.
	clock.forward(31 * day)
	list.PC.Lessons = nil
	decode(t, owner.do("GET", "/api/devices", nil), &list)
	if len(list.PC.Lessons) != 0 {
		t.Errorf("a speed seen 31 days ago is still listed: %+v", list.PC.Lessons)
	}
}
