package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Matiaspnyhus/Local-media-project/internal/db"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// What a player reports is kept for the support bundle. It must leave the
// PC scrubbed the same way the log is: no addresses, nothing long enough to
// be a secret.
func TestProblemsInTheSupportBundleAreScrubbedLikeTheLog(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	id := e.firstItem(c, "clip")
	e.do("POST", "/api/client-events", remote, c, map[string]string{"kind": "play-error", "itemId": id,
		"detail": "could not load http://192.168.1.50:8177/stream/abc?g=" + strings.Repeat("k", 43) + " MEDIA_ERR_NETWORK"})
	// Something was learned about the device as well, and the page said a
	// few words about trouble in a film, which go to the log.
	if w := e.do("POST", "/api/device/hdr-wrong", remote, c, map[string]bool{"on": true}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var p struct{ Play string }
	decode(t, e.do("POST", "/api/items/"+id+"/play", remote, c, nil), &p)
	e.do("POST", "/api/hls/"+p.Play+"/trouble", remote, c, map[string]string{"evidence": "starved",
		"detail": "stalled fetching http://192.168.1.50:8177/stream/abc?g=" + strings.Repeat("k", 43)})
	var device, lesson, signIn string
	if err := e.s.db.QueryRow(`SELECT s.id, s.device_id, l.id FROM sessions s JOIN lessons l ON l.device_id = s.device_id`).Scan(&signIn, &device, &lesson); err != nil {
		t.Fatal(err)
	}

	files := bundleFiles(t, e.do("GET", "/api/support-bundle", remote, c, nil))
	problems := files["playback-problems.json"]
	if !strings.Contains(problems, "MEDIA_ERR_NETWORK") || !strings.Contains(problems, "clip.mp4") {
		t.Fatalf("the problem itself is missing from the bundle: %s", problems)
	}
	if !strings.Contains(files["learned.json"], `"kind": "hdr"`) || !strings.Contains(files["log.txt"], "trouble while playing: starved") {
		t.Fatalf("what was learned, or the trouble, is missing from the bundle: %s\n%s", files["learned.json"], files["log.txt"])
	}
	for name, text := range files {
		// Addresses and secrets, and anything that could be used to pass
		// for a device or a sign-in or to find a film: ids, tokens.
		for _, bad := range []string{"192.168.1.50", strings.Repeat("k", 43), device, lesson, signIn, p.Play, id, e.s.deviceCookieValue(device)} {
			if strings.Contains(text, bad) {
				t.Errorf("%s in the bundle contains %q", name, bad)
			}
		}
	}
}

// What was learned leaves the PC as kinds and counts: kinds of device,
// kinds of film, how often films ran into trouble. Never a title, a file,
// a folder, a person, or what a browser said about itself. The same holds
// for what is kept about devices and lessons on the PC.
func TestWhatWasLearnedLeavesThePCAsKindsOnly(t *testing.T) {
	e := newTroubleEnv(t)
	// The PC: two large films it could not show, a film this PC converted
	// too slowly, and the owner's word about HDR.
	for _, film := range []string{e.big, e.big2} {
		if a := e.pc.mustTrouble(e.pc.play(film, nil).Play, "decode"); a.Action != "replay" {
			t.Fatalf("%+v", a)
		}
	}
	e.s.testPace = slow
	e.pc.mustTrouble(e.pc.play(e.oldFilm, nil).Play, "starved")
	e.pc.do("POST", "/api/device/hdr-wrong", map[string]bool{"on": true})
	// A phone someone else is signed in on, with a slow connection.
	e.member("kid", "member password")
	phone := e.browser(pixelPhone)
	phone.signIn("kid", "member password")
	phone.do("POST", "/api/capabilities", playsMost)
	e.s.testPace = healthy
	phone.mustTrouble(phone.play(e.big, nil).Play, "starved")

	type learnedFile struct {
		Devices []struct {
			Kind            string
			LastSeenDaysAgo int
			Lessons         []struct {
				Kind, About, HoldsFor   string
				TimesReported, DaysLeft int
				RanOut                  bool
			}
		}
		ThisPC struct {
			ConverterVersion string
			Processors       int
			Speeds           map[string]struct {
				SeenInRealPlays []struct {
					Class       string
					Setting     int
					Speed       float64
					StillCounts bool
				}
				PlannedWith map[string][]float64
			}
		}
		Trouble struct {
			CountedSince string
			Reports      []struct {
				Evidence, Outcome string
				Count             int
			}
		}
	}
	bundle := func() (map[string]string, learnedFile) {
		t.Helper()
		files := bundleFiles(t, e.pc.do("GET", "/api/support-bundle", nil))
		var l learnedFile
		if err := json.Unmarshal([]byte(files["learned.json"]), &l); err != nil {
			t.Fatalf("learned.json: %v\n%s", err, files["learned.json"])
		}
		return files, l
	}
	files, learned := bundle()
	if !strings.Contains(files["README.txt"], "learned.json") {
		t.Error("the bundle's own description does not mention learned.json")
	}
	if len(learned.Devices) != 2 {
		t.Fatalf("learned.json: %s", files["learned.json"])
	}
	for _, d := range learned.Devices {
		switch d.Kind {
		case "Windows PC, Chrome":
			if len(d.Lessons) != 2 || d.Lessons[0].Kind != "picture" || d.Lessons[0].About != "hevc/10/4k" || d.Lessons[0].HoldsFor != "every film" ||
				d.Lessons[0].TimesReported != 2 || d.Lessons[0].DaysLeft != 30 || d.Lessons[0].RanOut || d.Lessons[1].Kind != "hdr" {
				t.Errorf("the PC's lessons: %+v", d.Lessons)
			}
		case "Android phone":
			if len(d.Lessons) != 1 || d.Lessons[0].Kind != "rate" || d.Lessons[0].About != "5000000" || d.Lessons[0].DaysLeft != 1 {
				t.Errorf("the phone's lessons: %+v", d.Lessons)
			}
		default:
			t.Errorf("a device of kind %q", d.Kind)
		}
	}
	x264 := learned.ThisPC.Speeds["x264"]
	if learned.ThisPC.ConverterVersion != "9.9" || learned.ThisPC.Processors < 1 || len(x264.SeenInRealPlays) != 1 ||
		x264.SeenInRealPlays[0].Class != "hd" || x264.SeenInRealPlays[0].Setting != 1 || x264.SeenInRealPlays[0].Speed != 0.5 || !x264.SeenInRealPlays[0].StillCounts ||
		len(x264.PlannedWith["hd"]) != playback.NumRungs || x264.PlannedWith["hd"][0] != 0.5 {
		t.Errorf("what the bundle says about this PC: %+v", learned.ThisPC)
	}
	counted := map[string]int{}
	for _, r := range learned.Trouble.Reports {
		counted[r.Evidence+" "+r.Outcome] = r.Count
	}
	if counted["decode replay"] != 2 || counted["starved replay"] != 2 || len(counted) != 2 || learned.Trouble.CountedSince == "" {
		t.Errorf("the counts of trouble: %+v", learned.Trouble)
	}

	// Nothing that says which film, where, whose, or exactly which device.
	titles := []string{"Big Film", "Second Big", "Third Big", "Old Film", "Small Film", "(2020)", "(1999)"}
	var secrets []string
	for _, q := range []string{`SELECT id FROM devices`, `SELECT id FROM lessons`, `SELECT id FROM files`, `SELECT id FROM items`, `SELECT id FROM sessions`, `SELECT id FROM users`} {
		rows, err := e.s.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			rows.Scan(&id)
			secrets = append(secrets, id)
		}
		rows.Close()
	}
	if len(secrets) < 15 {
		t.Fatalf("only %d ids were found to look for", len(secrets))
	}
	for name, text := range files {
		for _, bad := range append(append([]string{e.media, e.s.dataDir, "kid"}, titles...), secrets...) {
			if strings.Contains(text, bad) {
				i := strings.Index(text, bad)
				t.Errorf("%s in the bundle contains %q: ...%s...", name, bad, text[max(0, i-80):min(len(text), i+80)])
			}
		}
	}
	// What a browser says about itself is in the bundle on purpose, in
	// devices.json. What was learned must not repeat it.
	for _, bad := range []string{"Pixel", "Mozilla", "AppleWebKit", "Chrome/130", ".mp4", "owner"} {
		if strings.Contains(files["learned.json"], bad) {
			t.Errorf("learned.json contains %q", bad)
		}
	}

	// The same for what is kept on the PC about devices, lessons, speeds
	// and counts. A lesson about one film holds that file's id, which is
	// how the film is remembered, and nothing else about it.
	var kept strings.Builder
	for _, q := range []string{
		`SELECT id || ' ' || label FROM devices`,
		`SELECT id || ' ' || device_id || ' ' || kind || ' ' || value FROM lessons`,
		`SELECT key || ' ' || CAST(value AS TEXT) FROM settings WHERE key LIKE 'speed:%' OR key = 'trouble_counts'`,
		`SELECT facts FROM picture_facts`,
	} {
		rows, err := e.s.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var line string
			rows.Scan(&line)
			kept.WriteString(line + "\n")
			n++
		}
		rows.Close()
		if n == 0 {
			t.Fatalf("%s found nothing to check", q)
		}
	}
	for _, bad := range append([]string{e.media, e.s.dataDir, "kid", "owner", "Pixel", "Mozilla", ".mp4"}, titles...) {
		if strings.Contains(kept.String(), bad) {
			t.Errorf("what is kept on the PC contains %q:\n%s", bad, kept.String())
		}
	}

	// A lesson that has run out is marked as such, not dropped: it is
	// still part of what the PC remembers.
	e.clock.forward(2 * day)
	_, learned = bundle()
	for _, d := range learned.Devices {
		if d.Kind == "Android phone" && (len(d.Lessons) != 1 || !d.Lessons[0].RanOut || d.Lessons[0].DaysLeft != 0 || d.LastSeenDaysAgo != 2) {
			t.Errorf("two days on, the phone: %+v", d)
		}
	}
}

// A subtitle track taken out of a film is kept for next time. If the copy
// could not be written in full (the disk filled up), it must not be kept:
// a short copy would be served to everyone from then on, with the film's
// subtitles stopping partway through and nothing to say why.
func TestASubtitleThatCouldNotBeWrittenInFullIsNotKept(t *testing.T) {
	e := newConvertEnv(t)
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Test Film")
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if len(p.Subtitles) == 0 || p.Subtitles[0].Key != "e0" {
		t.Fatalf("subtitles: %+v", p.Subtitles)
	}
	dir := filepath.Join(e.s.dataDir, "cache", "subtitles")
	inFolder := func() (kept, other []string) {
		entries, _ := os.ReadDir(dir)
		for _, f := range entries {
			if strings.HasSuffix(f.Name(), ".vtt") {
				kept = append(kept, f.Name())
			} else {
				other = append(other, f.Name())
			}
		}
		return kept, other
	}
	whole := func(when string) {
		t.Helper()
		w := e.do("GET", p.Subtitles[0].URL, remote, c, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Hello there") || !strings.Contains(w.Body.String(), "Goodbye") {
			t.Fatalf("%s the viewer got %d %q", when, w.Code, w.Body)
		}
	}

	// The disk is full: the file for the copy can be made, and every write
	// to it fails. (A file opened for reading only refuses writes too.)
	real := subtitleTemp
	subtitleTemp = func(dir, pattern string) (*os.File, error) {
		f, err := real(dir, pattern)
		if err != nil {
			return nil, err
		}
		f.Close()
		return os.Open(f.Name())
	}
	defer func() { subtitleTemp = real }()
	whole("while the copy could not be written,")
	if kept, other := inFolder(); len(kept) != 0 || len(other) != 0 {
		t.Fatalf("a copy that could not be written was kept, or its leftovers were: %v %v", kept, other)
	}

	// With room on the disk again, the subtitle is taken out afresh, whole,
	// and kept this time.
	subtitleTemp = real
	whole("afterwards")
	if kept, other := inFolder(); len(kept) != 1 || len(other) != 0 {
		t.Fatalf("after a good run the folder holds %v and %v, want one kept copy", kept, other)
	}
	whole("from the kept copy")
}

// The installer puts the program, with ffmpeg and ffprobe beside it, in a
// folder under the owner's account name. When one of the two cannot be
// started (an antivirus program removed it, or an update is half done) the
// operating system's error names it by its full path. That folder must not
// reach the log or the support bundle either.
func TestAConverterThatCannotBeStartedLeavesNoFolderNamesInTheLogOrTheBundle(t *testing.T) {
	program := filepath.Join(t.TempDir(), "Users", "Some Owner", "AppData", "Local", "Programs", "LocalMedia")
	// Neither file is there: both have been taken away.
	ffmpeg, ffprobe := filepath.Join(program, "ffmpeg.exe"), filepath.Join(program, "ffprobe.exe")
	data, media := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(media, "Films"), 0o755)
	os.WriteFile(filepath.Join(media, "Films", "Track Film (2019).mkv"), []byte("x"), 0o644)
	d, err := db.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := New(Config{DB: d, DataDir: data, Version: "test", Port: 8177, Assets: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}}, FFmpeg: ffmpeg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := &env{t: t, s: s, media: media}
	c := e.setup()
	film := e.firstItem(c, "Track Film")
	// The film was inspected while ffprobe was still there: H.264 picture
	// and DTS sound, which a browser needs converted.
	var fileID string
	s.db.QueryRow(`SELECT f.id FROM files f JOIN versions v ON v.id=f.version_id WHERE v.item_id=?`, film).Scan(&fileID)
	s.db.Exec(`UPDATE files SET probed=1, container='matroska', duration_ms=5400000 WHERE id=?`, fileID)
	if _, err := s.db.Exec(`INSERT INTO streams(file_id,idx,kind,codec,bit_depth,is_default) VALUES(?,0,'video','h264',8,1),(?,1,'audio','dts',0,1)`, fileID, fileID); err != nil {
		t.Fatal(err)
	}
	s.ffprobe = ffprobe
	e.do("POST", "/api/capabilities", remote, c, map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true},
		"video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}, "hls": true})
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if !strings.HasPrefix(p.URL, "/hls/") {
		t.Fatalf("play: %+v", p)
	}
	if w := e.do("GET", strings.TrimSuffix(p.URL, "index.m3u8")+"seg-0.m4s", remote, nil, nil); w.Code != 500 {
		t.Fatalf("a chunk, with no converter to make it, answered %d %s", w.Code, w.Body)
	}

	files := bundleFiles(t, e.do("GET", "/api/support-bundle", remote, c, nil))
	onDisk, err := os.ReadFile(filepath.Join(data, "logs", "localmedia.log"))
	if err != nil {
		t.Fatal(err)
	}
	files["the log on disk"] = string(onDisk)
	// Both failures are still reported, and say which program it was.
	if problems := files["playback-problems.json"]; !strings.Contains(problems, "convert-failed") || !strings.Contains(problems, "could not be started") || !strings.Contains(problems, "ffmpeg.exe") {
		t.Errorf("the bundle does not say the converter could not be started: %s", problems)
	}
	if !strings.Contains(string(onDisk), "could not find where to cut Track Film (2019).mkv") || !strings.Contains(string(onDisk), "ffprobe.exe") {
		t.Errorf("the log does not say the search for cuts failed for want of ffprobe:\n%s", onDisk)
	}
	for name, text := range files {
		// The made-up account name catches the folder however the error
		// wrote its path.
		for _, bad := range []string{program, "Some Owner", data} {
			if strings.Contains(text, bad) {
				i := strings.Index(text, bad)
				t.Errorf("%s contains %q: ...%s...", name, bad, text[max(0, i-100):min(len(text), i+100)])
			}
		}
	}
}

// Only a full path is taken out of what is logged. A data folder given as
// a short relative path ("--data data") is also an ordinary word, and
// replacing it everywhere would garble the very lines someone has to read.
func TestOnlyFullPathsAreTakenOutOfWhatIsLogged(t *testing.T) {
	e := newEnv(t)
	real := e.s.dataDir
	e.s.dataDir = "data"
	defer func() { e.s.dataDir = real }()
	line := "Invalid data found when processing input; metadata was read from data/cache/chunks/x"
	if got := e.s.pathless(line); got != line {
		t.Errorf("with the data folder given as \"data\", a line was rewritten:\n%s\nbecame\n%s", line, got)
	}
	// The same folder by its full path is still taken out.
	full, err := filepath.Abs("data")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.s.pathless("open " + filepath.Join(full, "cache", "x") + ": no such file"); strings.Contains(got, full) || !strings.Contains(got, "[data folder]") {
		t.Errorf("the data folder's full path was left in: %s", got)
	}
	// So is a program folder the way Go writes it inside quotes, with each
	// backslash doubled: some errors name a program they could not find so.
	e.s.ffmpeg = filepath.Join(real, `Users\Some "Other" Owner`, "ffmpeg.exe")
	quoted := (&exec.Error{Name: e.s.ffmpeg, Err: exec.ErrNotFound}).Error()
	if got := e.s.pathless(quoted); strings.Contains(got, "Owner") || !strings.Contains(got, "[program folder]") {
		t.Errorf("a quoted program path was left in:\n%s\nbecame\n%s", quoted, got)
	}
}

// When the converter cannot open a film (its drive dropped out, or it was
// moved while playing), ffmpeg names the file by its full path. On the
// owner's PC that path holds their account name, or the name or address of
// their network drive. Only the file's own name may reach the log and the
// support bundle.
func TestAFilmThatVanishesLeavesNoFolderNamesInTheLogOrTheBundle(t *testing.T) {
	e := newConvertEnv(t)
	// The library is on something that looks like a share on a NAS.
	lib := filepath.Join(t.TempDir(), "192.168.1.20", "Household Videos")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	made, _ := os.ReadDir(e.media)
	for _, f := range made {
		if err := os.Rename(filepath.Join(e.media, f.Name()), filepath.Join(lib, f.Name())); err != nil {
			t.Fatal(err)
		}
	}
	e.media = lib
	c := e.setup()
	e.s.InspectNow()
	film := e.firstItem(c, "Test Film")
	e.do("POST", "/api/capabilities", remote, c, map[string]any{"containers": map[string]bool{"mp4": true, "matroska": true},
		"video": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}, "hls": true})
	var p playAnswer
	decode(t, e.do("POST", "/api/items/"+film+"/play", remote, c, nil), &p)
	if !strings.HasPrefix(p.URL, "/hls/") {
		t.Fatalf("play: %+v", p)
	}
	// The film goes away while the player is open.
	src := filepath.Join(lib, "Test Film (2020).mkv")
	if err := os.Rename(src, src+".gone"); err != nil {
		t.Fatal(err)
	}
	if w := e.do("GET", strings.TrimSuffix(p.URL, "index.m3u8")+"seg-0.m4s", remote, nil, nil); w.Code != 500 {
		t.Fatalf("a chunk of a film that is gone answered %d %s", w.Code, w.Body)
	}

	files := bundleFiles(t, e.do("GET", "/api/support-bundle", remote, c, nil))
	if !strings.Contains(files["playback-problems.json"], "convert-failed") || !strings.Contains(files["playback-problems.json"], "Test Film (2020).mkv") {
		t.Errorf("the bundle does not say which film failed: %s", files["playback-problems.json"])
	}
	// The log as it lies on disk, before anything is scrubbed for a bundle.
	onDisk, err := os.ReadFile(filepath.Join(e.s.dataDir, "logs", "localmedia.log"))
	if err != nil {
		t.Fatal(err)
	}
	files["the log on disk"] = string(onDisk)
	if !strings.Contains(string(onDisk), "Test Film (2020).mkv") {
		t.Errorf("the log does not say which film failed:\n%s", onDisk)
	}
	for name, text := range files {
		for _, bad := range []string{lib, "Household Videos", e.s.dataDir} {
			if strings.Contains(text, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
		if name != "the log on disk" && strings.Contains(text, "192.168.1.20") {
			t.Errorf("%s contains the address 192.168.1.20", name)
		}
	}
}
