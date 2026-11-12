package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// This file is the server's part in what happens when a film that is
// playing runs into trouble. The page notices (the picture will not show,
// the player runs dry, the subtitles cannot be drawn) and reports it. What
// the trouble teaches and what to do about it is decided by package
// playback (Replan). Here the server remembers what each open film was
// planned with, so that it can ask; keeps what was learned (lessons.go,
// speeds.go); and tells the page whether to carry on or to start the film
// again.
//
// A film is started again at most twice in one play, however the page
// behaves: the count is kept here and handed from a play to the one that
// replaces it (the "from" of Play), never taken from the page.

const (
	// maxOpenFilms and maxOpenPerSignIn bound what is remembered about open
	// films. One sign-in watches one film at a time, give or take a tab.
	maxOpenFilms     = 600
	maxOpenPerSignIn = 12
	// handover is how long a film that has been closed is still remembered,
	// so that the play that replaces it can take over its count of steps
	// whichever of "stop the old one" and "start the new one" arrives first.
	handover = 10 * time.Minute
	// troublePerMinute is how many reports of trouble one sign-in may send
	// in a minute. A page in good order sends one or two in a film.
	troublePerMinute = 12
)

// openFilm is what the server remembers about one film a device has open:
// everything the film was planned with, and what has happened to it since.
type openFilm struct {
	session, item, file string
	// chunks says the film reaches its player as a list of chunks, and that
	// its token is the converter's. Otherwise the original file is sent,
	// and the token exists only for reporting trouble.
	chunks bool
	media  playback.Media
	caps   *playback.Caps
	want   playback.Want
	limits playback.Limits
	plan   playback.Plan
	// taken is the steps this play has taken so far, the plays it replaced
	// included. There are never more than playback allows (two).
	taken []playback.Step
	// noSubs says a step left the subtitles out because they could neither
	// be drawn nor be put into the picture.
	noSubs bool
	// replay is the answer that told the page to start the film again; it
	// is repeated to a page that asks again. said is what each kind of
	// trouble was answered with when nothing could be done.
	replay *troubleAnswer
	said   map[playback.Evidence]troubleAnswer

	opened time.Time
	closed time.Time // zero while the film is open
	turn   uint64
}

// openFilms is the films devices have open, by token.
type openFilms struct {
	mu    sync.Mutex
	films map[string]*openFilm
	turns uint64
	// reporting lets one report of trouble be dealt with at a time, so that
	// two reports about one film cannot both count as its first step.
	reporting sync.Mutex
}

// troubleAnswer is what the page is told after reporting trouble.
type troubleAnswer struct {
	// Action is "replay" when the film is to be started again where it was
	// (Play with "from" set to this film's token), and "wait" when it is to
	// be left playing.
	Action string `json:"action"`
	// Notice is one sentence for the viewer, "" when there is nothing to say.
	Notice string `json:"notice"`
	// Lower is the best setting the film may be converted at when it is
	// started again; 0 for any. The page need not send it back: the new
	// play takes it over through "from".
	Lower int `json:"lower"`
	// Lessons are the lessons about this device that the report added or
	// renewed (see GET /api/device). Forgetting them and playing again
	// without "from" undoes the step.
	Lessons []string `json:"lessons"`
}

// noteOpen remembers a film that has just been opened for a sign-in, and
// forgets the ones that are over.
func (s *Server) noteOpen(token string, f *openFilm) {
	o := &s.helpers.open
	now := s.now()
	// Films that reach their player in chunks are over when the converter
	// has closed them. It is asked without the lock held: it has a lock of
	// its own.
	o.mu.Lock()
	var ask []string
	for t, have := range o.films {
		if have.chunks && have.closed.IsZero() {
			ask = append(ask, t)
		}
	}
	o.mu.Unlock()
	var over []string
	for _, t := range ask {
		if s.hls == nil || s.hls.Owner(t) == "" {
			over = append(over, t)
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.films == nil {
		o.films = map[string]*openFilm{}
	}
	for _, t := range over {
		if have := o.films[t]; have != nil && have.closed.IsZero() {
			have.closed = now
		}
	}
	mine := 0
	for t, have := range o.films {
		switch {
		case !have.closed.IsZero() && now.Sub(have.closed) > handover:
			delete(o.films, t)
		case !have.chunks && now.Sub(have.opened) > linkLife:
			delete(o.films, t) // the link to the original file has run out
		case have.session == f.session:
			mine++
		}
	}
	for ; mine >= maxOpenPerSignIn; mine-- {
		o.dropOldest(f.session)
	}
	for len(o.films) >= maxOpenFilms {
		o.dropOldest("")
	}
	o.turns++
	f.opened, f.turn = now, o.turns
	o.films[token] = f
}

// dropOldest forgets the film opened longest ago: of one sign-in, or of
// anyone when session is "". The caller holds the lock.
func (o *openFilms) dropOldest(session string) {
	oldest := ""
	for t, have := range o.films {
		if (session == "" || have.session == session) && (oldest == "" || have.turn < o.films[oldest].turn) {
			oldest = t
		}
	}
	delete(o.films, oldest)
}

// filmOf returns what is remembered about a film a sign-in has (or just
// had) open under a token. known is false for a token that is not this
// sign-in's, which looks the same as one that was never handed out. open
// says the film has not been closed.
func (s *Server) filmOf(token, sessionID string) (f openFilm, known, open bool) {
	o := &s.helpers.open
	o.mu.Lock()
	have := o.films[token]
	if have == nil || have.session != sessionID {
		o.mu.Unlock()
		return openFilm{}, false, false
	}
	f = *have
	o.mu.Unlock()
	if !f.closed.IsZero() {
		return f, true, false
	}
	now := s.now()
	over := false
	if f.chunks {
		over = s.hls == nil || s.hls.Owner(token) != sessionID
	} else {
		over = now.Sub(f.opened) > linkLife
	}
	if over {
		s.closeFilm(token, sessionID)
	}
	return f, true, !over
}

// closeFilm notes that a sign-in's film has been closed. It is remembered a
// little longer for the play that may replace it (see handover).
func (s *Server) closeFilm(token, sessionID string) {
	o := &s.helpers.open
	o.mu.Lock()
	defer o.mu.Unlock()
	if have := o.films[token]; have != nil && have.session == sessionID && have.closed.IsZero() {
		have.closed = s.now()
	}
}

// forgetFilms forgets every film of a sign-in that has signed out.
func (s *Server) forgetFilms(sessionID string) {
	o := &s.helpers.open
	o.mu.Lock()
	defer o.mu.Unlock()
	for t, have := range o.films {
		if have.session == sessionID {
			delete(o.films, t)
		}
	}
}

// troubleLimiter keeps a page that reports trouble in a loop from filling
// the log and the database.
type troubleLimiter struct {
	mu     sync.Mutex
	recent map[string][]time.Time // by sign-in
}

// allow says a sign-in may report trouble now, and notes that it did.
func (l *troubleLimiter) allow(sessionID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recent == nil || len(l.recent) > 2000 {
		l.recent = map[string][]time.Time{} // sign-ins that reported long ago are not worth keeping
	}
	kept := l.recent[sessionID][:0]
	for _, at := range l.recent[sessionID] {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	if len(kept) >= troublePerMinute {
		l.recent[sessionID] = kept
		return false
	}
	l.recent[sessionID] = append(kept, now)
	return true
}

// troubleLog is the limiter and the count of reports by kind of evidence
// and by what came of them, which is kept in the settings so that the
// support bundle can say how often films stall on this PC.
type troubleLog struct {
	limiter troubleLimiter

	mu     sync.Mutex
	loaded bool
	counts troubleCounts
}

type troubleCounts struct {
	Since  int64          `json:"since"`  // when counting began
	Counts map[string]int `json:"counts"` // by evidence and outcome: "starved replay"
}

// What can come of a report.
const (
	cameReplay    = "replay"    // the film was started again another way
	cameUnchanged = "unchanged" // something was learned, but nothing better could be done for this film
	cameNothing   = "nothing"   // nothing was learned
	cameLimit     = "limit"     // the film had been started again twice already
)

func (s *Server) countTrouble(ev playback.Evidence, came string) {
	t := &s.helpers.trouble
	t.mu.Lock()
	defer t.mu.Unlock()
	s.loadTroubleCounts()
	t.counts.Counts[string(ev)+" "+came]++
	if raw, err := json.Marshal(t.counts); err == nil {
		s.setSetting("trouble_counts", string(raw))
	}
}

// loadTroubleCounts reads the counts from the settings the first time. The
// caller holds the log's lock.
func (s *Server) loadTroubleCounts() {
	t := &s.helpers.trouble
	if t.loaded {
		return
	}
	t.loaded = true
	if json.Unmarshal([]byte(s.setting("trouble_counts")), &t.counts) != nil || t.counts.Counts == nil {
		t.counts = troubleCounts{Since: s.now().Unix(), Counts: map[string]int{}}
	}
}

// troubleReport is the counts as the support bundle shows them.
func (s *Server) troubleReport() map[string]any {
	t := &s.helpers.trouble
	t.mu.Lock()
	defer t.mu.Unlock()
	s.loadTroubleCounts()
	type line struct {
		Evidence string `json:"evidence"`
		Outcome  string `json:"outcome"`
		Count    int    `json:"count"`
	}
	lines := []line{}
	for key, n := range t.counts.Counts {
		ev, came, _ := strings.Cut(key, " ")
		lines = append(lines, line{ev, came, n})
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].Evidence != lines[j].Evidence {
			return lines[i].Evidence < lines[j].Evidence
		}
		return lines[i].Outcome < lines[j].Outcome
	})
	return map[string]any{"countedSince": stamp(t.counts.Since), "reports": lines}
}

// handleTrouble takes the page's report that a film it is playing has run
// into trouble, and answers what to do about it.
//
// Only the sign-in the film was opened for may report about it. A report
// about a film that has been closed, or about a token that was never handed
// out, is refused in the same words as a chunk address that has run out.
func (s *Server) handleTrouble(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	token := chi.URLParam(r, "token")
	var in struct {
		Evidence string `json:"evidence"`
		// Detail is a few words from the page for the log ("3 stalls in a
		// minute"). It changes nothing about the answer.
		Detail string `json:"detail"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ev := playback.Evidence(in.Evidence)
	if ev != playback.DecodeTrouble && ev != playback.Starved && ev != playback.RendererFailed {
		writeErr(w, http.StatusBadRequest, "That report could not be read.")
		return
	}
	if !ids.Valid(token) {
		writeErr(w, http.StatusForbidden, "This link is not valid any more.")
		return
	}
	if !s.helpers.trouble.limiter.allow(u.SessionID, s.now()) {
		// Nothing is looked up, kept or logged for a page that reports in
		// a loop.
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "Too many reports of trouble in a short time. Wait a minute.")
		return
	}
	o := &s.helpers.open
	o.reporting.Lock()
	defer o.reporting.Unlock()
	f, known, open := s.filmOf(token, u.SessionID)
	if !known || !open {
		writeErr(w, http.StatusForbidden, "This link is not valid any more.")
		return
	}
	// A page that reports the same thing again is told the same thing
	// again. Nothing more is learned from it and nothing more is counted.
	if f.replay != nil {
		writeJSON(w, http.StatusOK, f.replay)
		return
	}
	if ans, ok := f.said[ev]; ok {
		writeJSON(w, http.StatusOK, ans)
		return
	}

	// How fast the film was being converted counts only as playback wants
	// it: over three chunks. Before that nothing is known.
	pace := 0.0
	if f.chunks {
		if p, n := s.paceOf(token); n >= 3 {
			pace = p
		}
	}
	out := playback.Replan(f.media, f.caps, f.want, f.limits, ev, pace, len(f.taken))
	ans := troubleAnswer{Action: "wait", Notice: out.Notice, Lessons: []string{}}
	came := cameNothing
	switch {
	case out.Step.Lesson == "" && len(f.taken) > 0 && playback.StepDown(out.Plan, ev, pace, 0).Lesson != "":
		// There would have been something to try, had the film not been
		// started again twice already.
		came = cameLimit
		ans.Notice = "This film has already been changed twice to make it play better and is still having trouble, so nothing more was changed."
	case out.Step.Lesson == "":
	default:
		ans.Lessons = s.keepStep(u.DeviceID, f, out.Step)
		came = cameUnchanged
		if out.Changed {
			came = cameReplay
			ans.Action, ans.Lower = "replay", out.Limits.Lower
		}
	}

	o.mu.Lock()
	if have := o.films[token]; have != nil {
		if out.Changed {
			have.taken = append(append([]playback.Step{}, have.taken...), out.Step)
			have.noSubs = have.noSubs || (out.Want.Subtitle < 0 && have.want.Subtitle >= 0)
			have.replay = &ans
		} else {
			if have.said == nil {
				have.said = map[playback.Evidence]troubleAnswer{}
			}
			have.said[ev] = ans
		}
	}
	o.mu.Unlock()

	s.countTrouble(ev, came)
	s.log.Printf("trouble while playing: %s", troubleLine(ev, in.Detail, f.plan, pace, out, came))
	writeJSON(w, http.StatusOK, ans)
}

// paceOf is how fast a film's picture is being converted, and over how
// many chunks that was measured (see hls.Manager.Pace). Tests set the pace
// themselves: what a report of trouble leads to must not depend on how
// fast the machine running the test happens to be.
func (s *Server) paceOf(token string) (pace float64, chunks int) {
	if s.testPace != nil {
		return s.testPace(token)
	}
	if s.hls == nil {
		return 0, 0
	}
	return s.hls.Pace(token)
}

// keepStep keeps what a step teaches: a lesson about the device the report
// came from, or a speed this PC was seen at. It returns the lessons about
// the device that were added or renewed.
func (s *Server) keepStep(deviceID string, f openFilm, st playback.Step) []string {
	kept := []string{}
	if st.Lesson == playback.LessonPC {
		// A speed counts only when this was the one picture being converted
		// (see playback.SpeedLesson): not planned beside another, and none
		// running beside it now, this sign-in's own other films included.
		if s.hls != nil && f.limits.Busy == 0 && s.hls.Converting(f.session) == 0 && s.hls.Converting("") <= 1 {
			s.sawSpeed(st.PC)
		}
		return kept
	}
	if deviceID == "" {
		return kept // a request without a device: the step still holds for this play
	}
	for _, what := range lessonsIn(st) {
		id, err := s.learn(deviceID, f.file, what)
		if err != nil {
			s.log.Printf("what was learned about a device could not be kept: %s", s.pathless(err.Error()))
			continue
		}
		kept = append(kept, id)
	}
	return kept
}

// troubleLine is one line for the log about a report of trouble. It names
// kinds (of evidence, of picture, of device) and never a film.
func troubleLine(ev playback.Evidence, detail string, p playback.Plan, pace float64, out playback.Outcome, came string) string {
	line := string(ev) + " in a film sent by " + string(p.Method)
	if p.Method == playback.Convert {
		line += " at setting " + strconv.Itoa(p.Video.Rung)
		if pace > 0 {
			line += fmt.Sprintf(", converted at %.2f seconds of film a second", pace)
		}
	}
	if k := pictureValue(p.Picture); k != "" {
		line += " (picture " + k + ")"
	}
	switch out.Step.Lesson {
	case playback.LessonDevice:
		line += "; learned about the device: it has trouble showing this picture"
	case playback.LessonPC:
		line += "; learned about this PC: it converts slower than was thought"
	case playback.LessonNetwork:
		line += "; learned about the device: films reach it too slowly"
	case playback.LessonDraw:
		line += "; learned about the device: its page cannot draw these subtitles"
	}
	switch came {
	case cameReplay:
		line += "; the film is started again by " + string(out.Plan.Method)
		if out.Plan.Method == playback.Convert {
			line += " at setting " + strconv.Itoa(out.Plan.Video.Rung)
		}
	case cameUnchanged:
		line += "; nothing better could be done for this film"
	case cameLimit:
		line += "; the film had been started again twice already, so nothing was changed"
	default:
		line += "; nothing was changed"
	}
	if d := plainWords(detail, 120); d != "" {
		line += "; the page said: " + d
	}
	return line
}

// plainWords cuts text from a page down to one short line for the log.
func plainWords(text string, n int) string {
	text = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, text)
	return clip(strings.Join(strings.Fields(text), " "), n)
}

// playState is what the page is told about a film it has open.
type playState struct {
	// Alive says the film is still open on the PC. When it is not (the PC
	// was restarted, or closed the film because nobody asked for it for a
	// long time), the page has to ask Play again.
	Alive  bool   `json:"alive"`
	Method string `json:"method"`
	// Rung is the setting the picture is converted at, 0 when it is not.
	Rung int `json:"rung"`
	// Pace is how many seconds of film the PC converts in a second, over
	// the last Chunks chunks (three at most). It means nothing while
	// Chunks is under two.
	Pace   float64 `json:"pace"`
	Chunks int     `json:"chunks"`
	// Advice is "lower" when the PC is converting slower than the film
	// plays and a lighter picture would help: the page then reports the
	// film as starved without waiting for it to stall. Otherwise "".
	Advice string `json:"advice"`
}

// handlePlayState says how a film the page has open is doing. A token that
// is not this sign-in's gets the same answer as a film that is gone, and
// asking does not count as watching: it keeps no conversion alive.
func (s *Server) handlePlayState(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	token := chi.URLParam(r, "token")
	st := playState{}
	if !ids.Valid(token) {
		writeErr(w, http.StatusNotFound, "Not found.") // not a token at all
		return
	}
	f, known, open := s.filmOf(token, u.SessionID)
	if !known || !open {
		writeJSON(w, http.StatusOK, st)
		return
	}
	st.Alive, st.Method, st.Rung = true, string(f.plan.Method), f.plan.Video.Rung
	if f.chunks {
		pace, chunks := s.paceOf(token)
		if chunks > 0 && !math.IsInf(pace, 0) && !math.IsNaN(pace) {
			st.Pace, st.Chunks = math.Round(pace*100)/100, chunks
		}
		// Whether that is too slow, and whether anything lighter is to be
		// had, is for playback to say: it is asked what it would do if the
		// player ran dry at this pace.
		if chunks >= 3 && f.plan.Method == playback.Convert && f.replay == nil {
			if out := playback.Replan(f.media, f.caps, f.want, f.limits, playback.Starved, pace, len(f.taken)); out.Changed && out.Step.Lesson == playback.LessonPC {
				st.Advice = "lower"
			}
		}
	}
	writeJSON(w, http.StatusOK, st)
}
