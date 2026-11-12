package server

import (
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// A lesson is one thing trouble while playing has taught about a device.
// Package playback decides what a piece of trouble teaches (StepDown) and
// what a lesson changes about the next plan (Step.Apply). This file keeps
// the lessons: which hold, for which films, and for how long.
//
// The rules, each of which has a test in lessons_test.go:
//
//  1. There is one lesson for each device, kind and value: "this iPhone
//     had trouble showing 4K 10-bit HEVC" is one, however often it happens.
//     A lesson names a kind of film. It never holds a title, a file name or
//     a folder.
//  2. A lesson about a kind of film (a picture the device had trouble
//     showing, a cap on the size of a converted picture, subtitles its page
//     could not draw) starts out as a lesson about one file: the first
//     failure may have been that file's fault. It is planned with for that
//     file only. The file is remembered by its row in the library, and the
//     lesson goes when that row does. Trouble of the same kind with a
//     second file makes it a lesson about the kind, and it is then planned
//     with for every film. The same file failing again changes nothing
//     about that.
//  3. A lesson about the device as a whole holds for every film from the
//     start: a connection that could not keep up, and the viewer's own word
//     that HDR colours look wrong.
//  4. A lesson about a kind of film lasts 30 days. Learned again after it
//     ran out, it lasts twice as long as it did, up to 120 days. Learned
//     again while it still holds (which happens where this PC could not act
//     on it), it lasts as long as it did, counted from that day.
//  5. A lesson about the connection lasts as long as playback says (a day),
//     every time, and never longer: connections get better. The viewer's
//     word about HDR lasts 30 days, every time.
//  6. A lesson that has run out is not planned with and not listed: the
//     next such film is tried as it is, without a word to anyone. That is
//     the one quiet retry. If it fails, rule 4 doubles the lesson. So that
//     rule 4 can tell, the lesson's row is kept for a year after it ran out.
//  7. Forgetting a lesson removes its row: nothing about it is remembered,
//     and trouble of that kind starts again from rule 2.
//
// What this PC was seen to convert at is not a lesson about a device. It is
// kept in the speed table (speeds.go).

// The kinds of lesson, as the database names them.
const (
	lessonPicture = "picture" // a kind of picture the device had trouble showing; the value names it ("hevc/10/4k")
	lessonHeight  = "height"  // a converted picture is sent no larger than this many lines
	lessonRate    = "rate"    // no more than this many bits a second are sent
	lessonHDR     = "hdr"     // the viewer said HDR colours look wrong on the device
	lessonDraw    = "draw"    // the device's page could not draw this kind of subtitles ("styled", "picture")
)

const (
	day = 24 * time.Hour
	// lessonLife is how long a lesson about a kind of film lasts the first
	// time, and lessonLongest the longest it gets by doubling.
	lessonLife    = 30 * day
	lessonLongest = 120 * day
	// hdrLife is how long the viewer's word about HDR colours lasts.
	hdrLife = 30 * day
	// lessonMemory is how long a lesson that ran out is still remembered,
	// so that learned again it lasts longer.
	lessonMemory = 365 * day
	// maxLessons bounds what is kept about one device. There are only so
	// many kinds of picture, so it is not reached by honest means.
	maxLessons = 60
)

// lesson is one row of what was learned about a device.
type lesson struct {
	ID      string
	Device  string
	Kind    string
	Value   string
	File    string // "" when the lesson is about the kind, not about one file
	Hits    int
	Learned int64
	Expires int64
}

// pictureValue names a kind of picture in a lesson: its format, whether it
// has more than 8 bits, and its size class. The depth is kept as 8 or 10,
// because that is all a lesson asks about (see playback.Limits.Trouble).
// It is "" for a format name that is not one ffprobe would give, which is
// then not kept at all.
func pictureValue(k playback.PictureKind) string {
	if !codecName.MatchString(k.Codec) {
		return ""
	}
	depth := "8"
	if k.BitDepth > 8 {
		depth = "10"
	}
	return k.Codec + "/" + depth + "/" + string(k.Size)
}

var codecName = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)

func pictureOfValue(value string) (playback.PictureKind, bool) {
	part := strings.Split(value, "/")
	if len(part) != 3 || !codecName.MatchString(part[0]) {
		return playback.PictureKind{}, false
	}
	k := playback.PictureKind{Codec: part[0], BitDepth: 8, Size: playback.Size(part[2])}
	if part[1] == "10" {
		k.BitDepth = 10
	}
	switch k.Size {
	case "", playback.Size480, playback.Size720, playback.Size1080, playback.Size4K:
		return k, true
	}
	return playback.PictureKind{}, false
}

// step is the lesson as package playback understands it, to be worked into
// what a film is planned with (playback.Step.Apply). ok is false for the
// one kind that has no step (HDR colours) and for a row that cannot be
// read.
func (l lesson) step() (st playback.Step, ok bool) {
	switch l.Kind {
	case lessonPicture:
		k, ok := pictureOfValue(l.Value)
		return playback.Step{Lesson: playback.LessonDevice, Device: playback.DeviceLesson{Picture: k}}, ok
	case lessonHeight:
		n, err := strconv.Atoi(l.Value)
		return playback.Step{Lesson: playback.LessonDevice, Device: playback.DeviceLesson{MaxHeight: n}}, err == nil && n > 0
	case lessonRate:
		n, err := strconv.Atoi(l.Value)
		return playback.Step{Lesson: playback.LessonNetwork, Network: playback.NetworkLesson{MaxRate: n}}, err == nil && n > 0
	case lessonDraw:
		off := playback.Drawing{Styled: l.Value == "styled", Picture: l.Value == "picture"}
		return playback.Step{Lesson: playback.LessonDraw, Draw: off}, off.Styled || off.Picture
	}
	return playback.Step{}, false
}

// learned is one lesson to keep, as a step from package playback gives it.
type learned struct {
	kind, value string
	// aboutFilms says the lesson is about a kind of film (rules 2 and 4),
	// not about the device as a whole (rules 3 and 5).
	aboutFilms bool
	life       time.Duration
}

// lessonsIn lists what a step teaches about a device. A step about this
// PC's speed teaches nothing about a device (see sawSpeed).
func lessonsIn(st playback.Step) []learned {
	switch st.Lesson {
	case playback.LessonDevice:
		if st.Device.MaxHeight > 0 {
			return []learned{{lessonHeight, strconv.Itoa(st.Device.MaxHeight), true, lessonLife}}
		}
		if v := pictureValue(st.Device.Picture); v != "" {
			return []learned{{lessonPicture, v, true, lessonLife}}
		}
	case playback.LessonNetwork:
		if st.Network.MaxRate > 0 && st.Network.Hours > 0 {
			return []learned{{lessonRate, strconv.Itoa(st.Network.MaxRate), false, time.Duration(st.Network.Hours) * time.Hour}}
		}
	case playback.LessonDraw:
		var out []learned
		if st.Draw.Styled {
			out = append(out, learned{lessonDraw, "styled", true, lessonLife})
		}
		if st.Draw.Picture {
			out = append(out, learned{lessonDraw, "picture", true, lessonLife})
		}
		return out
	}
	return nil
}

// learn keeps one lesson about a device by the rules at the top of this
// file, and returns its id. fileID is the file that was playing.
func (s *Server) learn(deviceID, fileID string, what learned) (string, error) {
	if deviceID == "" || (what.aboutFilms && fileID == "") {
		return "", errors.New("a lesson needs a device, and one about a kind of film needs the file that showed it")
	}
	now := s.now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// What ran out more than a year ago is forgotten for good (rule 6).
	if _, err := tx.Exec(`DELETE FROM lessons WHERE expires_at < ?`, now-int64(lessonMemory.Seconds())); err != nil {
		return "", err
	}
	var have lesson
	var file sql.NullString
	err = tx.QueryRow(`SELECT id, file_id, learned_at, expires_at FROM lessons WHERE device_id=? AND kind=? AND value=?`,
		deviceID, what.kind, what.value).Scan(&have.ID, &file, &have.Learned, &have.Expires)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A device cannot be made to fill the table: at the bound, the
		// lesson that runs out first makes room.
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM lessons WHERE device_id=?`, deviceID).Scan(&n); err != nil {
			return "", err
		}
		if n >= maxLessons {
			if _, err := tx.Exec(`DELETE FROM lessons WHERE id IN (SELECT id FROM lessons WHERE device_id=? ORDER BY expires_at, learned_at LIMIT ?)`,
				deviceID, n-maxLessons+1); err != nil {
				return "", err
			}
		}
		id := newID()
		about := sql.NullString{String: fileID, Valid: what.aboutFilms}
		if _, err := tx.Exec(`INSERT INTO lessons(id,device_id,kind,value,file_id,hits,learned_at,expires_at) VALUES(?,?,?,?,?,1,?,?)`,
			id, deviceID, what.kind, what.value, about, now, now+int64(what.life.Seconds())); err != nil {
			return "", err
		}
		return id, tx.Commit()
	case err != nil:
		return "", err
	}
	life := what.life
	if what.aboutFilms {
		// Rule 4: as long as it lasted, or twice that if it had run out.
		if before := time.Duration(have.Expires-have.Learned) * time.Second; before > life {
			life = before
		}
		if now >= have.Expires {
			life *= 2
		}
		life = min(life, lessonLongest)
	}
	// Rule 2: a second file makes it a lesson about the kind.
	about := sql.NullString{}
	if what.aboutFilms && file.Valid && file.String == fileID {
		about = file
	}
	if _, err := tx.Exec(`UPDATE lessons SET file_id=?, hits=hits+1, learned_at=?, expires_at=? WHERE id=?`,
		about, now, now+int64(life.Seconds()), have.ID); err != nil {
		return "", err
	}
	return have.ID, tx.Commit()
}

// lessonsOf lists the lessons that hold for a device right now, oldest
// first. "" lists them for every device.
func (s *Server) lessonsOf(deviceID string) ([]lesson, error) {
	q := `SELECT id, device_id, kind, value, COALESCE(file_id,''), hits, learned_at, expires_at FROM lessons WHERE expires_at > ?`
	args := []any{s.now().Unix()}
	if deviceID != "" {
		q += ` AND device_id = ?`
		args = append(args, deviceID)
	}
	// Those learned in the same second come in the order they were first
	// learned, so that a list reads the same each time it is asked for.
	rows, err := s.db.Query(q+` ORDER BY learned_at, rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lesson
	for rows.Next() {
		var l lesson
		if err := rows.Scan(&l.ID, &l.Device, &l.Kind, &l.Value, &l.File, &l.Hits, &l.Learned, &l.Expires); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// forgetLesson removes one lesson of a device (rule 7), and reports whether
// there was such a lesson. A lesson of another device is not touched.
func (s *Server) forgetLesson(deviceID, lessonID string) bool {
	res, err := s.db.Exec(`DELETE FROM lessons WHERE id=? AND device_id=?`, lessonID, deviceID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// plans is what the films of one request are planned with: what the device
// says it plays, what this PC can do and how fast, and what was learned
// about the device. Most of it is the same for every film; a lesson about
// one file is worked in for that file only (see file).
type plans struct {
	caps   *playback.Caps
	limits playback.Limits
	byFile map[string][]lesson
}

// planFor gathers what to plan a signed-in request's films with. It is the
// one place this is put together: Play and the lists both ask here, so a
// list says about a film what Play then does.
//
// idle asks what a film would need with the PC otherwise idle, as the
// lists do: a list must not call a film unplayable because the PC happens
// to be converting for someone else at this moment.
//
// It costs one question to the database however many films are planned.
// A request without a device (see requestDevice) is planned for as if
// nothing had been learned.
func (s *Server) planFor(u *User, idle bool) *plans {
	who := u
	if idle {
		who = nil
	}
	p := &plans{caps: s.capsFor(u), limits: s.limits(who)}
	p.limits.Speed = s.speeds(p.limits.Encoder)
	if u.DeviceID == "" {
		return p
	}
	held, err := s.lessonsOf(u.DeviceID)
	if err != nil {
		s.log.Printf("what was learned about a device could not be read, so it is not used for now: %s", s.pathless(err.Error()))
		return p
	}
	for _, l := range held {
		if l.File != "" {
			if p.byFile == nil {
				p.byFile = map[string][]lesson{}
			}
			p.byFile[l.File] = append(p.byFile[l.File], l)
			continue
		}
		p.caps, p.limits = withLesson(l, p.caps, p.limits)
	}
	return p
}

// file is what to plan one file with.
func (p *plans) file(fileID string) (*playback.Caps, playback.Limits) {
	c, l := p.caps, p.limits
	for _, about := range p.byFile[fileID] {
		c, l = withLesson(about, c, l)
	}
	return c, l
}

// withLesson works one lesson into what a film is planned with. Nothing
// that is passed in is changed.
func withLesson(about lesson, c *playback.Caps, l playback.Limits) (*playback.Caps, playback.Limits) {
	if about.Kind == lessonHDR {
		l.NoHDR = true
		return c, l
	}
	if st, ok := about.step(); ok {
		c, _, l = st.Apply(c, playback.Want{}, l)
	}
	return c, l
}

// lessonView is a lesson as the page shows it.
type lessonView struct {
	ID string `json:"id"`
	// Kind is "picture", "height", "rate", "hdr" or "draw" for a lesson
	// about a device, and "speed" for one about this PC.
	Kind string `json:"kind"`
	// Text is the lesson in one sentence a person understands.
	Text      string `json:"text"`
	LearnedAt string `json:"learnedAt"`
	ExpiresAt string `json:"expiresAt"`
}

func stamp(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.RFC3339) }

// viewsOf turns lessons into what the page shows. canConvert says this PC
// can convert pictures, which is what most lessons lead to.
func viewsOf(held []lesson, canConvert bool) []lessonView {
	out := []lessonView{}
	for _, l := range held {
		out = append(out, lessonView{ID: l.ID, Kind: l.Kind, Text: lessonText(l, canConvert), LearnedAt: stamp(l.Learned), ExpiresAt: stamp(l.Expires)})
	}
	return out
}

// lessonText says a lesson in one plain sentence: what went wrong, and what
// is done about it. It names kinds of film and never a film.
//
// Most lessons lead to the picture being converted. On a PC that cannot
// convert pictures nothing changes, and the sentence says so instead of
// promising what does not happen.
func lessonText(l lesson, canConvert bool) string {
	one := l.File != ""
	const cannot = ", but this PC cannot convert pictures, so nothing is changed for now."
	switch l.Kind {
	case lessonPicture:
		k, ok := pictureOfValue(l.Value)
		if !ok {
			break
		}
		name := playback.VideoName(playback.Track{Codec: k.Codec, BitDepth: k.BitDepth})
		// A lesson holds for that size and larger ones (a device that
		// could not show a format at 720p is not sent it at 1080p).
		size := map[playback.Size]string{playback.Size4K: "4K ", playback.Size1080: "1080p ", playback.Size720: "720p ", playback.Size480: ""}[k.Size]
		larger := ""
		if k.Size == playback.Size1080 || k.Size == playback.Size720 {
			larger = " (or larger)"
		}
		switch {
		case one && !canConvert:
			return "This device had trouble showing one " + size + name + " film" + cannot
		case one:
			return "This device had trouble showing one " + size + name + " film, so that film is converted for it."
		case !canConvert:
			return "This device had trouble showing " + size + name + " films" + larger + cannot
		}
		return "This device had trouble showing " + size + name + " films" + larger + ", so they are converted for it."
	case lessonHeight:
		lines := l.Value + "p"
		if one {
			return "This device had trouble showing one film that the PC had converted for it, so that film is now made no larger than " + lines + "."
		}
		return "This device had trouble showing films that the PC had converted for it, so they are now made no larger than " + lines + "."
	case lessonRate:
		n, _ := strconv.Atoi(l.Value)
		rate := megabits(n)
		if !canConvert {
			return "Films were reaching this device too slowly" + cannot
		}
		return "Films were reaching this device too slowly, so for a day films that need more than " + rate + " megabits a second are converted to a lighter picture for it."
	case lessonHDR:
		if !canConvert {
			return "HDR colours were reported to look wrong on this device" + cannot
		}
		return "HDR colours were reported to look wrong on this device, so HDR films are converted to normal colour for it."
	case lessonDraw:
		what := "subtitles that have their own design"
		how := "put into the picture or shown as plain text"
		if l.Value == "picture" {
			what, how = "subtitles that are stored as pictures", "put into the picture"
		}
		if one {
			return "This device could not draw " + what + " over one film, so for that film they are " + how + "."
		}
		return "This device could not draw " + what + " over the film, so they are " + how + "."
	}
	return "Something was learned about this device that this version of the app cannot describe."
}

// megabits writes bits a second as megabits, with one decimal where it has
// one ("2.5", "5").
func megabits(bits int) string {
	tenths := (bits + 50_000) / 100_000
	if tenths%10 == 0 {
		return strconv.Itoa(tenths / 10)
	}
	return strconv.Itoa(tenths/10) + "." + strconv.Itoa(tenths%10)
}
