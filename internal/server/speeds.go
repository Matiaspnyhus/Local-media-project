package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// How fast this PC converts pictures is a fact about the PC, not about any
// device, so it is kept in one place: the speed table. For each encoder it
// holds, for every class of film and every setting (playback.Speeds), how
// many seconds of film the PC converts in a second.
//
// Two things fill it:
//
//   - A measurement of the PC (setMeasuredSpeeds). The measuring itself is
//     not built yet; whatever does it calls that function.
//   - What a real play showed (sawSpeed): a film that was converted slower
//     than it plays. That only ever lowers a figure, only when the film
//     was the one picture being converted at the time, and counts for 30
//     days: the PC may have been busy with something else that evening.
//
// The table belongs to one converter on one PC: it is kept in the settings
// under the converter's version and the number of processors, and a table
// kept under anything else is dropped when this one is written. Another
// version of ffmpeg, or the data folder moved to another PC, starts afresh.

// seenLife is how long a speed seen in a real play counts.
const seenLife = 30 * day

// seenSpeed is a speed a real play showed, and when.
type seenSpeed struct {
	Speed float64 `json:"speed"`
	At    int64   `json:"at"`
}

// speedTable is what is known about one encoder's speed on this PC.
type speedTable struct {
	Measured playback.Speeds                                 `json:"measured,omitempty"`
	Seen     map[playback.Class][playback.NumRungs]seenSpeed `json:"seen,omitempty"`
}

// speedBook is the speed tables in memory, as they are in the settings.
type speedBook struct {
	mu        sync.Mutex
	key       string // the setting they were read from
	byEncoder map[string]*speedTable
}

var speedClasses = map[playback.Class]string{
	playback.ClassHD:    "films up to 1080p",
	playback.ClassHDR:   "HDR films up to 1080p",
	playback.Class4K:    "4K films",
	playback.Class4KHDR: "4K HDR films",
}

// softwareEncoder is the name the speed table files the PC's own processor
// under, which is also what a plan calls it (playback.Video.Encoder).
const softwareEncoder = "x264"

func encoderOf(name string) string {
	if name == "" {
		return softwareEncoder
	}
	return name
}

var versionWord = regexp.MustCompile(`[^A-Za-z0-9._+-]`)

// readConverterVersion asks ffmpeg which version it is ("6.1.1", "8.1.3-
// Jellyfin"). The speed table is filed under it.
func readConverterVersion(ctx context.Context, ffmpeg string) string {
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// "ffmpeg version 6.1.1-3ubuntu5 Copyright (c) 2000-2023 the FFmpeg developers"
	f := strings.Fields(string(out))
	if len(f) < 3 || f[0] != "ffmpeg" || f[1] != "version" {
		return ""
	}
	return clip(versionWord.ReplaceAllString(f[2], ""), 60)
}

// converterVersion is the version of this PC's converter, "" when there is
// none or it would not say.
func (s *Server) converterVersion() string {
	if s.ffmpeg == "" {
		return ""
	}
	s.abilities() // the version is asked for along with what the converter can do
	c := s.helpers.convert
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// speedKey is the setting this PC's speed tables are kept under.
func (s *Server) speedKey() string {
	v := s.converterVersion()
	if v == "" {
		v = "unknown"
	}
	return "speed:" + v + ":" + strconv.Itoa(runtime.NumCPU())
}

// openSpeedBook returns the speed tables, read from the settings the first
// time. The caller holds the book's lock (which this takes) until it calls
// the returned function.
func (s *Server) openSpeedBook() (book *speedBook, done func()) {
	book = &s.helpers.speeds
	key := s.speedKey() // may wait for the converter; asked before the lock is taken
	book.mu.Lock()
	if book.key != key || book.byEncoder == nil {
		book.key, book.byEncoder = key, map[string]*speedTable{}
		if raw := s.setting(key); raw != "" {
			if json.Unmarshal([]byte(raw), &book.byEncoder) != nil || book.byEncoder == nil {
				book.byEncoder = map[string]*speedTable{}
			}
		}
	}
	return book, book.mu.Unlock
}

// table is one encoder's table, made if there is none yet. An entry in the
// settings that says nothing ("x264": null) counts as none.
func (b *speedBook) table(encoder string) *speedTable {
	t := b.byEncoder[encoder]
	if t == nil {
		t = &speedTable{}
		b.byEncoder[encoder] = t
	}
	return t
}

// saveSpeedBook writes the tables to the settings and drops tables kept for
// another converter or another number of processors.
func (s *Server) saveSpeedBook(book *speedBook) error {
	raw, err := json.Marshal(book.byEncoder)
	if err != nil {
		return err
	}
	if err := s.setSetting(book.key, string(raw)); err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM settings WHERE key LIKE 'speed:%' AND key <> ?`, book.key)
	return err
}

// counts says a speed seen in a real play still counts at the moment now.
func (v seenSpeed) counts(now int64) bool {
	return v.Speed > 0 && now < v.At+int64(seenLife.Seconds())
}

// effective is the table to plan with: what was measured, lowered by what
// real plays have shown in the last 30 days. A setting that was never
// measured takes what a real play showed.
func (t *speedTable) effective(now int64) playback.Speeds {
	if t == nil {
		return nil
	}
	out := playback.Speeds{}
	for class, row := range t.Measured {
		out[class] = row
	}
	for class, seen := range t.Seen {
		row := out[class]
		for i, v := range seen {
			if v.counts(now) && (row[i] <= 0 || v.Speed < row[i]) {
				row[i] = v.Speed
			}
		}
		out[class] = row
	}
	return out
}

// speeds is how fast this PC converts with an encoder, for planning films
// with (playback.Limits.Speed). It is empty while nothing has been measured
// or seen, and the plan then starts from safe settings.
func (s *Server) speeds(encoder string) playback.Speeds {
	if s.hls == nil {
		return nil
	}
	book, done := s.openSpeedBook()
	defer done()
	return book.byEncoder[encoderOf(encoder)].effective(s.now().Unix())
}

// setMeasuredSpeeds records how fast this PC was measured to convert with
// an encoder, in place of any earlier measurement for it. It is what a
// measurement of the PC (the test button, or the one run after an install)
// calls with its results. What real plays have shown stays, and goes on
// lowering the new figures until it is 30 days old.
func (s *Server) setMeasuredSpeeds(encoder string, measured playback.Speeds) error {
	if s.hls == nil {
		return errors.New("this PC has no converter to keep speeds for")
	}
	book, done := s.openSpeedBook()
	defer done()
	kept := playback.Speeds{}
	for class, row := range measured {
		if speedClasses[class] == "" {
			continue
		}
		for i := range row {
			if !(row[i] > 0) || math.IsInf(row[i], 0) {
				row[i] = 0 // not measured
			}
		}
		kept[class] = row
	}
	book.table(encoderOf(encoder)).Measured = kept
	return s.saveSpeedBook(book)
}

// sawSpeed lowers the speed table to what a film that was playing showed:
// the PC converted it at this speed. It reports whether the table changed.
// A figure is never raised, and the caller must not pass a speed seen
// while the PC was converting more than this one picture (see
// playback.SpeedLesson).
func (s *Server) sawSpeed(l playback.SpeedLesson) bool {
	if s.hls == nil || !(l.Speed > 0) || math.IsInf(l.Speed, 0) || l.Rung < 1 || l.Rung > playback.NumRungs || speedClasses[l.Class] == "" {
		return false
	}
	book, done := s.openSpeedBook()
	defer done()
	now := s.now().Unix()
	t := book.table(encoderOf(l.Encoder))
	if have := t.effective(now)[l.Class][l.Rung-1]; have > 0 && have <= l.Speed {
		return false
	}
	if t.Seen == nil {
		t.Seen = map[playback.Class][playback.NumRungs]seenSpeed{}
	}
	row := t.Seen[l.Class]
	// Kept with two decimals, cut off rather than rounded: a speed just
	// under the film's own must not be written down as having reached it.
	row[l.Rung-1] = seenSpeed{Speed: math.Floor(l.Speed*100) / 100, At: now}
	if !(row[l.Rung-1].Speed > 0) {
		row[l.Rung-1].Speed = 0.01 // slower than can be written: still a speed, not "never seen"
	}
	t.Seen[l.Class] = row
	if err := s.saveSpeedBook(book); err != nil {
		s.log.Printf("a speed this PC was seen to convert at could not be stored, so it is kept only until the program next starts: %s", s.pathless(err.Error()))
	}
	return true
}

// speedLessons lists what real plays have shown about this PC's speed and
// still counts, as lessons the owner can read and forget.
func (s *Server) speedLessons() []lessonView {
	out := []lessonView{}
	if s.hls == nil {
		return out
	}
	book, done := s.openSpeedBook()
	defer done()
	now := s.now().Unix()
	for _, encoder := range sortedKeys(book.byEncoder) {
		t := book.byEncoder[encoder]
		if t == nil {
			continue
		}
		for _, class := range []playback.Class{playback.ClassHD, playback.ClassHDR, playback.Class4K, playback.Class4KHDR} {
			for i, v := range t.Seen[class] {
				if !v.counts(now) {
					continue
				}
				with := ""
				if encoder != softwareEncoder {
					with = ", with the graphics card"
				}
				out = append(out, lessonView{
					ID: encoder + "." + string(class) + "." + strconv.Itoa(i+1), Kind: "speed",
					Text: "While a film played, this PC converted only " + oneDecimal(v.Speed) + " seconds of it each second (" + speedClasses[class] +
						", at setting " + strconv.Itoa(i+1) + " of " + strconv.Itoa(playback.NumRungs) + with +
						"), so a lighter setting is picked for such films.",
					LearnedAt: stamp(v.At), ExpiresAt: stamp(v.At + int64(seenLife.Seconds())),
				})
			}
		}
	}
	return out
}

// speedReport is the speed tables for the support bundle: for each encoder
// what was measured, what real plays showed, and what is planned with.
func (s *Server) speedReport() map[string]any {
	out := map[string]any{}
	if s.hls == nil {
		return out
	}
	book, done := s.openSpeedBook()
	defer done()
	now := s.now().Unix()
	for encoder, t := range book.byEncoder {
		if t == nil {
			continue
		}
		seen := []map[string]any{}
		for _, class := range []playback.Class{playback.ClassHD, playback.ClassHDR, playback.Class4K, playback.Class4KHDR} {
			for i, v := range t.Seen[class] {
				if v.At > 0 {
					seen = append(seen, map[string]any{"class": class, "setting": i + 1, "speed": v.Speed,
						"daysAgo": (now - v.At) / int64(day.Seconds()), "stillCounts": v.counts(now)})
				}
			}
		}
		out[encoder] = map[string]any{"measured": t.Measured, "seenInRealPlays": seen, "plannedWith": t.effective(now)}
	}
	return out
}

// forgetSpeed removes one speed a real play showed, named as speedLessons
// names it, and reports whether there was one.
func (s *Server) forgetSpeed(id string) bool {
	if s.hls == nil {
		return false
	}
	// "x264.4k-hdr.3": the encoder's own name may hold dots, the other two
	// parts do not.
	last := strings.LastIndexByte(id, '.')
	if last < 0 {
		return false
	}
	rung, err := strconv.Atoi(id[last+1:])
	mid := strings.LastIndexByte(id[:last], '.')
	if err != nil || rung < 1 || rung > playback.NumRungs || mid < 0 {
		return false
	}
	encoder, class := id[:mid], playback.Class(id[mid+1:last])
	book, done := s.openSpeedBook()
	defer done()
	t := book.byEncoder[encoder]
	if t == nil || t.Seen[class][rung-1].At == 0 {
		return false
	}
	row := t.Seen[class]
	row[rung-1] = seenSpeed{}
	t.Seen[class] = row
	if err := s.saveSpeedBook(book); err != nil {
		s.log.Printf("the speed table could not be stored: %s", s.pathless(err.Error()))
	}
	return true
}

// oneDecimal writes a speed with one decimal, cut off rather than rounded,
// so that a speed just under 1 never reads as 1.0.
func oneDecimal(x float64) string {
	return strconv.FormatFloat(math.Floor(x*10+1e-9)/10, 'f', 1, 64)
}

func sortedKeys(m map[string]*speedTable) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
