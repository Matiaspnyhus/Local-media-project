package server

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// versionsEnv is a library with one film kept as two files, a 1080p copy
// and a 2160p copy, which the library takes for two versions of one film.
// The two are told apart in answers by their length: the 1080p copy is
// recorded as an hour long, the 2160p copy as a second longer.
type versionsEnv struct {
	trackEnv
	file map[string]string // "1080p" and "2160p": the file's id
}

var versionLength = map[string]int64{"1080p": 3600000, "2160p": 3601000}

func twoVersions(t *testing.T) versionsEnv {
	t.Helper()
	e := newEnv(t)
	dir := filepath.Join(e.media, "Films", "Two Sizes (2019)")
	os.MkdirAll(dir, 0o755)
	// On disk the 2160p copy is the larger file, as it would be.
	os.WriteFile(filepath.Join(dir, "Two Sizes (2019) - 1080p.mkv"), bytes.Repeat([]byte("x"), 100), 0o644)
	os.WriteFile(filepath.Join(dir, "Two Sizes (2019) - 2160p.mkv"), bytes.Repeat([]byte("x"), 400), 0o644)
	c := e.setup()
	v := versionsEnv{trackEnv{env: e, c: c, film: e.firstItem(c, "Two Sizes")}, map[string]string{}}
	for size := range versionLength {
		var id string
		e.s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=? AND f.rel_path LIKE ?`, v.film, "%"+size+"%").Scan(&id)
		if id == "" {
			t.Fatalf("the %s copy is not a version of the film", size)
		}
		v.file[size] = id
	}
	return v
}

// Rows for the streams table, as the inspector would write them.
func picture(codec string, bits, width, height int) string {
	return fmt.Sprintf(`'video','%s',%d,%d,%d,1,0,0,'',''`, codec, bits, width, height)
}

func sound(codec, language, title string, main, commentary bool) string {
	return fmt.Sprintf(`'audio','%s',0,0,0,%d,0,%d,'%s','%s'`, codec, b2i(main), b2i(commentary), language, title)
}

func captions(language string, forced bool) string {
	return fmt.Sprintf(`'subtitle','subrip',0,0,0,0,%d,0,'%s',''`, b2i(forced), language)
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

// inspected records what the inspector found in one of the two copies.
func (v versionsEnv) inspected(t *testing.T, size, container string, rows ...string) {
	t.Helper()
	v.s.db.Exec(`UPDATE files SET probed=1, container=?, duration_ms=? WHERE id=?`, container, versionLength[size], v.file[size])
	for i, row := range rows {
		if _, err := v.s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,bit_depth,width,height,is_default,is_forced,is_commentary,language,title) VALUES(?,?,`+row+`)`, v.file[size], i); err != nil {
			t.Fatal(err)
		}
	}
}

// versionAnswer is the part of a play answer these tests read.
type versionAnswer struct {
	URL        string
	PositionMs int64
	DurationMs int64
	Plan       struct{ Playable, Method string }
	Audio      []audioView
	Subtitles  []subtitleView
}

// answers asks the list and Play about the film, as one device.
func (v versionsEnv) answers(t *testing.T, c *http.Cookie) (list itemView, play versionAnswer) {
	t.Helper()
	var l struct{ Items []itemView }
	decode(t, v.do("GET", "/api/movies", remote, c, nil), &l)
	for _, it := range l.Items {
		if it.ID == v.film {
			list = it
		}
	}
	w := v.do("POST", "/api/items/"+v.film+"/play", remote, c, nil)
	if w.Code != 200 {
		t.Fatalf("play: %d %s", w.Code, w.Body)
	}
	decode(t, w, &play)
	return list, play
}

// which names the copy an answer is about, by its length.
func which(durationMs int64) string {
	for size, ms := range versionLength {
		if ms == durationMs {
			return size
		}
	}
	return fmt.Sprintf("neither (a length of %d ms)", durationMs)
}

// Two devices: a phone that shows H.264 only, and a TV that shows 10-bit
// HEVC as well. Both open MKV and play repackaged video.
var (
	phoneLike = map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true},
		"video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}, "hls": true}
	tvLike = map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true},
		"video": map[string]bool{"h264": true, "hevc": true, "hevc10": true}, "audio": map[string]bool{"aac": true}, "hls": true}
)

// The fault: of two versions of a film, the list judged the larger file
// and Play opened whichever the database gave first. So the list could say
// "plays" for a film that Play then refused although a copy that plays was
// there, or say "cannot play" for a film that played.
func TestTheListAndPlayAgreeOnWhichVersion(t *testing.T) {
	for _, plays := range []string{"1080p", "2160p"} {
		refused := map[string]string{"1080p": "2160p", "2160p": "1080p"}[plays]
		v := twoVersions(t)
		v.inspected(t, plays, "matroska", picture("h264", 8, 1920, 1080), sound("aac", "eng", "", true, false))
		v.inspected(t, refused, "matroska", picture("hevc", 10, 1920, 1080), sound("aac", "eng", "", true, false)) // this device cannot show it
		v.do("POST", "/api/capabilities", remote, v.c, phoneLike)
		list, play := v.answers(t, v.c)
		if list.Playable != "yes" || list.Method != "direct" || which(list.DurationMs) != plays {
			t.Errorf("only the %s copy plays on this device: the list says %s/%s, judging the %s copy", plays, list.Playable, list.Method, which(list.DurationMs))
		}
		if play.Plan.Playable != "yes" || !strings.HasPrefix(play.URL, "/stream/"+v.file[plays]+"?") || which(play.DurationMs) != plays {
			t.Errorf("only the %s copy plays on this device: Play answers %s/%s with the %s copy (%s)", plays, play.Plan.Playable, play.Plan.Method, which(play.DurationMs), play.URL)
		}
	}
}

// Among the versions of a film a device gets the one it plays most
// cheaply: as it is, before repackaged, before with its sound converted,
// before not at all. Among versions that cost the same it gets the better
// picture. The list and Play say the same in every case.
func TestTheVersionADevicePlaysMostCheaplyIsChosen(t *testing.T) {
	const (
		small = "1080p"
		large = "2160p"
	)
	h264, hevc10 := picture("h264", 8, 1920, 1080), picture("hevc", 10, 3840, 2160)
	h264Large := picture("h264", 8, 3840, 2160)
	aac, dts := sound("aac", "eng", "", true, false), sound("dts", "eng", "", true, false)
	type copyOf struct {
		container string
		rows      []string // nil: not inspected yet
	}
	onlyMP4 := map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}, "hls": true}
	for _, c := range []struct {
		name         string
		small, large copyOf
		device       map[string]any // nil: the device has not said what it plays
		biggerOnDisk string         // "" leaves the 2160p copy the bigger file
		want         string
		method       string
	}{
		{name: "as it is before repackaged, though the other has the better picture",
			small: copyOf{"mp4", []string{h264, aac}}, large: copyOf{"matroska", []string{h264Large, aac}}, device: onlyMP4, want: small, method: "direct"},
		{name: "as it is before repackaged, the other way round",
			small: copyOf{"matroska", []string{h264, aac}}, large: copyOf{"mp4", []string{h264Large, aac}}, device: onlyMP4, want: large, method: "direct"},
		{name: "repackaged before sound converted",
			small: copyOf{"matroska", []string{h264, aac}}, large: copyOf{"matroska", []string{h264Large, dts}}, device: onlyMP4, want: small, method: "remux"},
		{name: "sound converted before not at all",
			small: copyOf{"matroska", []string{h264, dts}}, large: copyOf{"matroska", []string{hevc10, aac}}, device: onlyMP4, want: small, method: "audio"},
		{name: "among equals the better picture",
			small: copyOf{"matroska", []string{h264, aac}}, large: copyOf{"matroska", []string{h264Large, aac}}, device: phoneLike, want: large, method: "direct"},
		{name: "the better picture, not the bigger file",
			small: copyOf{"matroska", []string{h264, aac}}, large: copyOf{"matroska", []string{h264Large, aac}}, device: phoneLike, biggerOnDisk: small, want: large, method: "direct"},
		{name: "a copy known to play before one not inspected yet",
			small: copyOf{"matroska", []string{h264, dts}}, large: copyOf{}, device: phoneLike, want: small, method: "audio"},
		{name: "a copy not inspected yet before one known not to play",
			small: copyOf{}, large: copyOf{"matroska", []string{hevc10, aac}}, device: phoneLike, want: small, method: "direct"},
		{name: "a device that has not said what it plays gets the better picture",
			small: copyOf{"matroska", []string{h264, aac}}, large: copyOf{"matroska", []string{hevc10, aac}}, want: large, method: "direct"},
	} {
		v := twoVersions(t)
		v.pretendConverter(t)
		for size, copy := range map[string]copyOf{small: c.small, large: c.large} {
			if copy.rows != nil {
				v.inspected(t, size, copy.container, copy.rows...)
			} else {
				v.s.db.Exec(`UPDATE files SET duration_ms=? WHERE id=?`, versionLength[size], v.file[size])
			}
		}
		if c.biggerOnDisk != "" {
			v.s.db.Exec(`UPDATE files SET size=100000 WHERE id=?`, v.file[c.biggerOnDisk])
		}
		if c.device != nil {
			v.do("POST", "/api/capabilities", remote, v.c, c.device)
		}
		list, play := v.answers(t, v.c)
		if which(list.DurationMs) != c.want || list.Method != c.method {
			t.Errorf("%s: the list judges the %s copy (%s/%s), want the %s copy (%s)", c.name, which(list.DurationMs), list.Playable, list.Method, c.want, c.method)
		}
		if which(play.DurationMs) != c.want || play.Plan.Method != c.method {
			t.Errorf("%s: Play opens the %s copy (%s/%s), want the %s copy (%s)", c.name, which(play.DurationMs), play.Plan.Playable, play.Plan.Method, c.want, c.method)
		}
		if list.Playable != play.Plan.Playable || list.Method != play.Plan.Method {
			t.Errorf("%s: the list says %s/%s and Play %s/%s", c.name, list.Playable, list.Method, play.Plan.Playable, play.Plan.Method)
		}
	}
}

// The order itself, apart from any library.
func TestTheOrderOfVersions(t *testing.T) {
	plan := func(a playback.Answer, m playback.Method) playback.Plan { return playback.Plan{Playable: a, Method: m} }
	inOrder := []*version{
		{rel: "direct, 4K", plan: plan(playback.Yes, playback.Direct), pixels: 3840 * 2160, size: 10},
		// These two differ in the size of the file alone. Their names are in
		// the other order, so that it is the size that is seen to decide.
		{rel: "direct, 1080p, the larger file", plan: plan(playback.Yes, playback.Direct), pixels: 1920 * 1080, size: 30},
		{rel: "direct, 1080p, a smaller file", plan: plan(playback.Yes, playback.Direct), pixels: 1920 * 1080, size: 20},
		// And these two in nothing but their names.
		{rel: "direct, 720p, copy 1", plan: plan(playback.Yes, playback.Direct), pixels: 1280 * 720, size: 20},
		{rel: "direct, 720p, copy 2", plan: plan(playback.Yes, playback.Direct), pixels: 1280 * 720, size: 20},
		{rel: "repackaged", plan: plan(playback.Yes, playback.Remux), pixels: 3840 * 2160, size: 90},
		{rel: "sound converted", plan: plan(playback.Yes, playback.Audio), pixels: 3840 * 2160, size: 90},
		{rel: "picture converted", plan: plan(playback.Yes, playback.Convert), pixels: 3840 * 2160, size: 90},
		{rel: "unknown", plan: plan(playback.Unknown, playback.Direct), pixels: 3840 * 2160, size: 90},
		{rel: "not at all", plan: plan(playback.No, playback.None), pixels: 3840 * 2160, size: 90},
	}
	for i, a := range inOrder {
		for j, b := range inOrder {
			if got, want := a.before(b), i < j; got != want {
				t.Errorf("%q before %q: %v, want %v", a.rel, b.rel, got, want)
			}
		}
		// Whatever order they are found in, the same one is chosen.
		turned := append(append([]*version{}, inOrder[i:]...), inOrder[:i]...)
		if got := preferred(turned); got != inOrder[0] {
			t.Errorf("starting from %q, %q was chosen", a.rel, got.rel)
		}
	}
}

// A film is watched on a phone, which plays one copy, and carried on with
// on a TV, which plays the other. Where the viewer stopped, "continue
// watching" and the sound and subtitles they picked belong to the film, so
// all of them carry over. The two copies have different tracks: a pick is
// read against the copy it was made on, and on the other copy the track of
// the same language and kind is used, as it is for the next episode of a
// show.
func TestProgressAndPicksCarryOverBetweenVersions(t *testing.T) {
	v := twoVersions(t)
	v.pretendConverter(t)
	v.inspected(t, "1080p", "matroska", picture("h264", 8, 1920, 1080),
		sound("aac", "eng", "", true, false), sound("aac", "nor", "", false, false),
		captions("eng", false))
	v.inspected(t, "2160p", "matroska", picture("hevc", 10, 3840, 2160),
		sound("aac", "nor", "", true, false), sound("aac", "eng", "", false, false), sound("aac", "eng", "Commentary", false, true),
		captions("nor", false), captions("eng", false))
	phone := v.c
	tv := sessionCookie(v.do("POST", "/api/login", "192.168.1.60:1", nil, map[string]string{"name": "owner", "password": "a good password"}))
	v.do("POST", "/api/capabilities", remote, phone, phoneLike)
	v.do("POST", "/api/capabilities", remote, tv, tvLike)
	// picked is what an answer starts with: "sound 1 no, subtitles e0 en".
	picked := func(p versionAnswer) string {
		audio, sub := "none", "none"
		for _, a := range p.Audio {
			if a.Selected {
				audio = fmt.Sprintf("%d %s", a.Index, a.Language)
			}
		}
		for _, s := range p.Subtitles {
			if s.Selected {
				sub = s.Key + " " + s.Language
			}
		}
		return "sound " + audio + ", subtitles " + sub
	}
	choose := func(c *http.Cookie, body map[string]any) {
		t.Helper()
		if w := v.do("PUT", "/api/items/"+v.film+"/choice", remote, c, body); w.Code != 200 {
			t.Fatalf("choice %v: %d %s", body, w.Code, w.Body)
		}
	}

	_, onPhone := v.answers(t, phone)
	_, onTV := v.answers(t, tv)
	if which(onPhone.DurationMs) != "1080p" || which(onTV.DurationMs) != "2160p" {
		t.Fatalf("the phone gets the %s copy and the TV the %s copy; want 1080p and 2160p", which(onPhone.DurationMs), which(onTV.DurationMs))
	}

	// Stopped ten minutes in on the phone.
	v.watch(phone, v.film, 600000, onPhone.DurationMs)
	var home homeOut
	decode(t, v.do("GET", "/api/home", remote, tv, nil), &home)
	if len(home.Continue) != 1 || home.Continue[0].ID != v.film || home.Continue[0].PositionMs != 600000 || which(home.Continue[0].DurationMs) != "2160p" {
		t.Errorf("the TV's \"continue watching\" after ten minutes on the phone: %+v", home.Continue)
	}
	if _, onTV = v.answers(t, tv); onTV.PositionMs != 600000 || which(onTV.DurationMs) != "2160p" {
		t.Errorf("the TV starts the %s copy at %d ms, want its own copy at 600000", which(onTV.DurationMs), onTV.PositionMs)
	}

	// On the phone: Norwegian sound (its second track) and the English
	// subtitles (its only ones).
	choose(phone, map[string]any{"audioLang": "no", "subLang": "en", "subsOn": true, "audioIndex": 1, "subKey": "e0"})
	if _, onPhone = v.answers(t, phone); picked(onPhone) != "sound 1 no, subtitles e0 en" {
		t.Errorf("the phone comes back with %v, want what was picked on it", picked(onPhone))
	}
	// On the TV's copy Norwegian sound is the first track and the English
	// subtitles are the second.
	if _, onTV = v.answers(t, tv); picked(onTV) != "sound 0 no, subtitles e1 en" {
		t.Errorf("the TV starts with %v; want Norwegian sound and English subtitles, which are tracks 0 and e1 of its copy", picked(onTV))
	}

	// On the TV: the commentary (its third track) and Norwegian subtitles.
	// The pick is read against the TV's copy, not the phone's, whose third
	// sound track does not exist.
	choose(tv, map[string]any{"audioLang": "en", "subLang": "no", "subsOn": true, "audioIndex": 2, "subKey": "e0"})
	if _, onTV = v.answers(t, tv); picked(onTV) != "sound 2 en, subtitles e0 no" {
		t.Errorf("the TV comes back with %v, want what was picked on it", picked(onTV))
	}
	// The phone's copy has no commentary: it plays its main sound.
	if _, onPhone = v.answers(t, phone); !strings.HasPrefix(picked(onPhone), "sound 0 en,") {
		t.Errorf("the phone, whose copy has no commentary, starts with %v; want its main sound", picked(onPhone))
	}
	// And the plain English sound picked on the TV (its second track) is
	// the phone's first.
	choose(tv, map[string]any{"audioLang": "en", "subLang": "no", "subsOn": true, "audioIndex": 1, "subKey": "e0"})
	if _, onPhone = v.answers(t, phone); !strings.HasPrefix(picked(onPhone), "sound 0 en,") {
		t.Errorf("after English sound was picked on the TV the phone starts with %v", picked(onPhone))
	}
}

// A copy that does not say how long it is cannot be handed out as chunks,
// so it cannot be repackaged or have its sound converted. That rule was
// known to Play alone. The lists ranked such a copy as one that plays
// repackaged: it was preferred to a copy that really plays, the list said
// "plays", and Play then refused the film.
func TestACopyThatCannotBeRepackagedIsNotPreferredToOneThatPlays(t *testing.T) {
	onlyMP4 := map[string]any{"containers": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}, "hls": true}
	h264, h264Large := picture("h264", 8, 1920, 1080), picture("h264", 8, 3840, 2160)
	aac, dts := sound("aac", "eng", "", true, false), sound("dts", "eng", "", true, false)
	const noLength = "does not say how long it is"
	// reasons asks Play again for the reasons it gives.
	reasons := func(v versionsEnv) string {
		var p struct{ Plan struct{ Reasons []string } }
		decode(t, v.do("POST", "/api/items/"+v.film+"/play", remote, v.c, nil), &p)
		return strings.Join(p.Plan.Reasons, " ")
	}

	// The other copy plays with its sound converted: that one is chosen.
	v := twoVersions(t)
	v.pretendConverter(t)
	v.do("POST", "/api/capabilities", remote, v.c, onlyMP4)
	v.inspected(t, "1080p", "matroska", h264, dts)
	v.inspected(t, "2160p", "matroska", h264Large, aac)                       // would only need repackaging,
	v.s.db.Exec(`UPDATE files SET duration_ms=0 WHERE id=?`, v.file["2160p"]) // but does not say how long it is
	list, play := v.answers(t, v.c)
	if list.Playable != "yes" || list.Method != "audio" || which(list.DurationMs) != "1080p" {
		t.Errorf("the list says %s/%s, judging the %s copy; want the 1080p copy, which plays with its sound converted", list.Playable, list.Method, which(list.DurationMs))
	}
	if play.Plan.Playable != "yes" || play.Plan.Method != "audio" || which(play.DurationMs) != "1080p" {
		t.Errorf("Play answers %s/%s with the %s copy; want the 1080p copy, which plays with its sound converted", play.Plan.Playable, play.Plan.Method, which(play.DurationMs))
	}

	// No copy plays: the list and Play both say so, and both say why.
	v = twoVersions(t)
	v.pretendConverter(t)
	v.do("POST", "/api/capabilities", remote, v.c, onlyMP4)
	v.inspected(t, "1080p", "matroska", picture("hevc", 10, 1920, 1080), aac) // this device cannot show it
	v.inspected(t, "2160p", "matroska", h264Large, aac)
	v.s.db.Exec(`UPDATE files SET duration_ms=0 WHERE id=?`, v.file["2160p"])
	list, play = v.answers(t, v.c)
	if list.Playable != "no" || list.Method != "none" || !strings.Contains(strings.Join(list.Reasons, " "), noLength) {
		t.Errorf("the list says %s/%s because %q; want no, because the copy %s", list.Playable, list.Method, list.Reasons, noLength)
	}
	if why := reasons(v); play.Plan.Playable != "no" || play.Plan.Method != "none" || !strings.Contains(why, noLength) {
		t.Errorf("Play answers %s/%s because %q; want no, because the copy %s", play.Plan.Playable, play.Plan.Method, why, noLength)
	}

	// A copy of unknown length that this device plays as it is. The viewer
	// prefers its second sound track, which only repackaging can reach. The
	// film's own main sound is better than no film: that is the rule for
	// every other obstacle to the preferred sound, and it holds for this one.
	v = twoVersions(t)
	v.pretendConverter(t)
	v.do("POST", "/api/capabilities", remote, v.c, onlyMP4)
	v.inspected(t, "1080p", "matroska", picture("hevc", 10, 1920, 1080), aac)
	v.inspected(t, "2160p", "mp4", h264Large, aac, sound("aac", "nor", "", false, false))
	v.s.db.Exec(`UPDATE files SET duration_ms=0 WHERE id=?`, v.file["2160p"])
	if w := v.do("PUT", "/api/prefs", remote, v.c, map[string]any{"audioLang": "no", "subLang": "", "subs": "auto"}); w.Code != 200 {
		t.Fatalf("preferences: %d %s", w.Code, w.Body)
	}
	list, play = v.answers(t, v.c)
	if list.Playable != "yes" || list.Method != "direct" {
		t.Errorf("the list says %s/%s of a copy that plays as it is", list.Playable, list.Method)
	}
	if play.Plan.Playable != "yes" || play.Plan.Method != "direct" || !strings.HasPrefix(play.URL, "/stream/"+v.file["2160p"]+"?") {
		t.Errorf("Play answers %s/%s (%s) because %q; want the file as it is, with its main sound", play.Plan.Playable, play.Plan.Method, play.URL, reasons(v))
	}
}

// The library is looked over every quarter of an hour. A copy deleted from
// the disk since then is still in the lists, and Play used to hand it out,
// though it could not be fetched, while the film's other copy was there.
func TestACopyThatIsGoneFromTheDiskIsNotHandedOut(t *testing.T) {
	v := twoVersions(t)
	v.do("POST", "/api/capabilities", remote, v.c, phoneLike)
	v.inspected(t, "1080p", "matroska", picture("h264", 8, 1920, 1080), sound("aac", "eng", "", true, false), sound("aac", "nor", "", false, false))
	v.inspected(t, "2160p", "matroska", picture("h264", 8, 3840, 2160), sound("aac", "eng", "", true, false))
	fetched := func(p versionAnswer) int { return v.do("GET", p.URL, remote, nil, nil).Code }
	dir := filepath.Join(v.media, "Films", "Two Sizes (2019)")

	_, play := v.answers(t, v.c)
	if which(play.DurationMs) != "2160p" || fetched(play) != 200 {
		t.Fatalf("with both copies there Play hands out the %s copy, and fetching it answers %d; want the 2160p copy and 200", which(play.DurationMs), fetched(play))
	}
	if err := os.Remove(filepath.Join(dir, "Two Sizes (2019) - 2160p.mkv")); err != nil {
		t.Fatal(err)
	}
	_, play = v.answers(t, v.c)
	if which(play.DurationMs) != "1080p" || fetched(play) != 200 {
		t.Errorf("with the 2160p copy gone from the disk Play hands out the %s copy, and fetching it answers %d; want the 1080p copy, which is there", which(play.DurationMs), fetched(play))
	}
	// A pick of sound is read against the copy that plays now: the second
	// sound track is one only the 1080p copy has.
	if w := v.do("PUT", "/api/items/"+v.film+"/choice", remote, v.c, map[string]any{"audioLang": "no", "subLang": "", "subsOn": false, "audioIndex": 1, "subKey": ""}); w.Code != 200 {
		t.Fatalf("choice: %d %s", w.Code, w.Body)
	}
	var lang, kind string
	var index int
	v.s.db.QueryRow(`SELECT audio_lang, audio_index, audio_kind FROM track_memory`).Scan(&lang, &index, &kind)
	if lang != "no" || index != 1 {
		t.Errorf("the pick of the 1080p copy's second sound track was remembered as language %q, track %d", lang, index)
	}

	// With every copy gone nothing is left to choose from, and the answer is
	// what it always was for a film whose file is gone: an address that
	// answers "not available".
	if err := os.Remove(filepath.Join(dir, "Two Sizes (2019) - 1080p.mkv")); err != nil {
		t.Fatal(err)
	}
	_, play = v.answers(t, v.c)
	if code := fetched(play); which(play.DurationMs) != "2160p" || code != 404 {
		t.Errorf("with both copies gone Play hands out the %s copy, and fetching it answers %d; want the 2160p copy as before, and 404", which(play.DurationMs), code)
	}
}
