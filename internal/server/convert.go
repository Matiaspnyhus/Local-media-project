package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/hls"
	"github.com/Matiaspnyhus/Local-media-project/internal/playback"
)

// This file is where the decision to convert a film's picture (package
// playback) meets the converter that does it (package hls): what this PC's
// converter can do, the facts about a film's picture that a conversion
// starts from, and the settings one play is converted at.

// conversion is what the server keeps in memory for converting pictures.
type conversion struct {
	// What the converter on this PC was built to do. ffmpeg is asked in the
	// background (see askConverter); nothing holds mu while it runs.
	mu        sync.Mutex
	asked     bool          // can is ffmpeg's own answer
	can       hls.Abilities // nothing, until ffmpeg has answered
	asking    chan struct{} // closed when the ask under way ends; nil when none is
	waitUntil time.Time     // until when a request may wait for that ask
	retryAt   time.Time     // after a failed ask: not before this

	// Facts about films' pictures, kept while the program runs, the looks
	// at films that are under way, and the looks that failed a while ago.
	factMu    sync.Mutex
	facts     map[string]keptFacts  // by file
	looks     map[string]*factsLook // by file and stamp
	lookFails map[string]lookFailure
	factSlots chan struct{}
}

type keptFacts struct {
	stamp string
	facts hls.PictureFacts
}

// factsLook is one look at a film's picture. Every Play of the film while
// it runs waits for the same one.
type factsLook struct {
	done  chan struct{} // closed when facts and err are set
	facts hls.PictureFacts
	err   error
}

// lookFailure remembers that a look failed for a file as it was then.
type lookFailure struct {
	stamp string
	at    time.Time
	err   error
}

const (
	// maxFactLooks is how many films are looked at at once. A look is two
	// short reads, so this is hardly ever reached.
	maxFactLooks = 2
	// factLookLimit is how long one look may take. It takes a moment,
	// except on a disk that has to wake up first.
	factLookLimit = 45 * time.Second
	// lookRetryAfter is how long a failed look at a file is remembered. A
	// file ffprobe could not make sense of will not have changed its mind
	// by the next press of Play. It is shorter than for a failed search
	// for cuts (cutRetryAfter), because here the film does not play
	// meanwhile.
	lookRetryAfter = 5 * time.Minute
	// maxKeptFacts bounds the memory the kept facts take.
	maxKeptFacts = 5000
	// abilityAskLimit is how long ffmpeg is given to say what it can do,
	// and abilityRetry how long after a failed ask it is asked again.
	abilityAskLimit = 30 * time.Second
	abilityRetry    = time.Minute
)

// abilityWait is how long after the program has started a list or a Play
// waits for ffmpeg's first answer about what it can do, and lookWait how
// long a Play waits for the look at a film's picture. (Variables so that
// tests need not wait as long.)
var (
	abilityWait = 2 * time.Second
	lookWait    = 20 * time.Second
)

// errLookSlow says a look at a film's picture did not finish in the time a
// Play waits for it. The look carries on.
var errLookSlow = errors.New("the look at the film's picture is taking long")

func newConversion() *conversion {
	return &conversion{facts: map[string]keptFacts{}, looks: map[string]*factsLook{}, lookFails: map[string]lookFailure{},
		factSlots: make(chan struct{}, maxFactLooks)}
}

// abilities says what this PC's converter can do, and whether that is
// known: until ffmpeg has answered, the answer is "nothing, as far as is
// known".
//
// ffmpeg is asked when the program starts (see askConverterSoon), in the
// background. No request ever runs it or waits behind a lock for it: the
// program is large, and on a PC that is busy starting up it can take its
// time, or hang. A request that arrives in the first moments waits a
// little for that first answer (abilityWait, counted from when ffmpeg was
// asked, not from when the request came) and after that is answered from
// what is known. Should asking fail, nothing can be converted for now, and
// the first request a minute later has it asked again, without waiting for
// it: a PC that was busy starting up must not lose conversion until the
// program is next started.
func (s *Server) abilities() (can hls.Abilities, known bool) {
	if s.ffmpeg == "" {
		return hls.Abilities{}, true
	}
	c := s.helpers.convert
	c.mu.Lock()
	if !c.asked && c.asking == nil && !time.Now().Before(c.retryAt) {
		s.askConverter(0)
	}
	asking, wait := c.asking, time.Until(c.waitUntil)
	can, known = c.can, c.asked
	c.mu.Unlock()
	if known || asking == nil || wait <= 0 {
		return can, known
	}
	limit := time.NewTimer(wait)
	defer limit.Stop()
	select {
	case <-asking:
	case <-limit.C:
	case <-s.ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.can, c.asked
}

// askConverter has ffmpeg asked what it can do (it is started twice, for a
// moment each) and the answer kept. It returns at once; requests may wait
// for the answer for as long as wait says. The caller holds c.mu.
func (s *Server) askConverter(wait time.Duration) {
	c := s.helpers.convert
	if s.ctx.Err() != nil {
		return // the program is closing
	}
	done := make(chan struct{})
	c.asking, c.waitUntil = done, time.Now().Add(wait)
	ffmpeg := s.ffmpeg
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		ctx, cancel := context.WithTimeout(s.ctx, abilityAskLimit)
		can, err := hls.ReadAbilities(ctx, ffmpeg)
		cancel()
		c.mu.Lock()
		if err != nil {
			if s.ctx.Err() == nil {
				s.log.Printf("the converter could not be asked what it can do, so pictures are not converted for now: %s", s.pathless(err.Error()))
			}
			c.retryAt = time.Now().Add(abilityRetry)
		} else {
			c.asked, c.can = true, can
		}
		c.asking = nil
		c.mu.Unlock()
		close(done)
	}()
}

// askConverterSoon asks the converter what it can do when the program
// starts, so that the answer is usually there before anyone needs it.
func (s *Server) askConverterSoon() {
	c := s.helpers.convert
	c.mu.Lock()
	defer c.mu.Unlock()
	s.askConverter(abilityWait)
}

// limits says what this PC can do for one play: whether it can repackage
// and convert at all, and how many pictures it is converting already for
// other viewers. u is the viewer the play is for; nil where the question
// is what a film would need on an idle PC, as the lists ask.
//
// How fast this PC converts has not been measured yet, so the decision
// starts from the safe settings (see playback.Limits.Speed). Turning HDR
// into normal colour is not built yet either, so no ability to do it is
// claimed, and the decision says plainly that an HDR film cannot be
// converted here.
func (s *Server) limits(u *User) playback.Limits {
	if s.hls == nil {
		return playback.Limits{NoConverter: true}
	}
	can, _ := s.abilities()
	l := playback.Limits{Convert: can.CanConvert()}
	if u != nil {
		l.Busy = s.hls.Converting(u.SessionID)
	}
	return l
}

// decide is the decision how a film is played (playback.DecideFor) as this
// server gives it. The rules are playback's. Two answers are put right
// here, where it is known what stands behind the limits:
//
//   - A device's word that it does not play 4K smoothly is advice, as a
//     lesson is: where this PC can make a smaller picture, it does. Where
//     it cannot (no converter, an HDR film, too slow), the film is planned
//     as if the device had not said so, and plays as it did before
//     pictures could be converted. Stuttering beats not playing.
//   - A PC whose converter cannot convert pictures, or has not said yet
//     whether it can, gets the rules' answer for a PC without picture
//     conversion, which is worded for the version before there was any
//     ("arrives in a later version") and blames the browser. The reasons
//     are taken from what the film would need, and the PC is named.
func (s *Server) decide(m playback.Media, c *playback.Caps, w playback.Want, l playback.Limits) playback.Plan {
	p := playback.DecideFor(m, c, w, l)
	if p.Playable != playback.No || c == nil {
		return p
	}
	if p.PCCannot {
		anySize := *c
		anySize.Tall = maps.Clone(c.Video)
		if q := playback.DecideFor(m, &anySize, w, l); q.Playable != playback.No {
			return q
		}
		return p
	}
	if l.Convert || l.NoConverter {
		return p
	}
	// What would be decided on a PC that converts anything?
	able := l
	able.Convert, able.ToneMap, able.DoViTone = true, true, true
	q := playback.DecideFor(m, c, w, able)
	switch {
	case q.Playable == playback.No && !q.PCCannot:
		return q // the device could not play it then either, and this says why
	case q.Playable != playback.No && q.Method != playback.Convert:
		return p
	}
	reason := "The converter on this PC cannot convert pictures, so the film cannot be converted for this device."
	if _, known := s.abilities(); !known {
		cv := s.helpers.convert
		cv.mu.Lock()
		waiting := cv.asking != nil
		cv.mu.Unlock()
		reason = "The converter on this PC could not be asked what it can do, so pictures cannot be converted at the moment. It will be asked again within a minute or two."
		if waiting {
			reason = "The converter on this PC has not said yet what it can do, so pictures cannot be converted at the moment. Try again in a minute."
		}
	}
	no := unplayable(q, reason)
	no.PCCannot = true
	return no
}

// pictureFacts returns the facts about a film's picture that converting it
// starts from (see hls.PictureFacts), looking at the file the first time
// and keeping the answer for as long as the file stays the same. size and
// mtime are the file's own, read when Play was pressed: a file that was
// replaced since the library last scanned it is looked at afresh.
//
// A look that failed is remembered for a while, as a failed search for
// cuts is: pressing Play again must not start the same doomed look, with
// every other film waiting behind it. A look that is slow (a disk waking
// up) is waited for only so long; errLookSlow then says that it is still
// going on, and the next Play finds what it found.
func (s *Server) pictureFacts(ctx context.Context, fileID, path string, size, mtime int64) (hls.PictureFacts, error) {
	if s.ffprobe == "" {
		return hls.PictureFacts{}, errors.New("the file inspector is not installed")
	}
	stamp := fmt.Sprintf("%d-%d", size, mtime)
	c := s.helpers.convert
	c.factMu.Lock()
	if k, ok := c.facts[fileID]; ok && k.stamp == stamp {
		c.factMu.Unlock()
		return k.facts, nil
	}
	if f, ok := c.lookFails[fileID]; ok && f.stamp == stamp && time.Since(f.at) < lookRetryAfter {
		c.factMu.Unlock()
		return hls.PictureFacts{}, f.err // it failed a moment ago; it would fail again
	}
	key := fileID + " " + stamp
	look := c.looks[key]
	if look == nil {
		if s.ctx.Err() != nil {
			c.factMu.Unlock()
			return hls.PictureFacts{}, s.ctx.Err()
		}
		look = &factsLook{done: make(chan struct{})}
		c.looks[key] = look
		s.background.Add(1)
		go s.lookAtPicture(look, key, fileID, stamp, path)
	}
	c.factMu.Unlock()
	// The look belongs to no one request: a viewer who gives up leaves it
	// running for the next.
	limit := time.NewTimer(lookWait)
	defer limit.Stop()
	select {
	case <-look.done:
		return look.facts, look.err
	case <-ctx.Done():
		return hls.PictureFacts{}, ctx.Err()
	case <-limit.C:
		return hls.PictureFacts{}, errLookSlow
	}
}

// lookAtPicture runs one look, when its turn comes, and keeps what it finds.
func (s *Server) lookAtPicture(look *factsLook, key, fileID, stamp, path string) {
	defer s.background.Done()
	c := s.helpers.convert
	cutShort := false // by the clock or by the program closing, which says nothing about the file
	select {
	case c.factSlots <- struct{}{}:
		ctx, cancel := context.WithTimeout(s.ctx, factLookLimit)
		look.facts, look.err = hls.ProbePicture(ctx, s.ffprobe, path)
		cutShort = ctx.Err() != nil
		cancel()
		<-c.factSlots
	case <-s.ctx.Done():
		look.err, cutShort = s.ctx.Err(), true
	}
	c.factMu.Lock()
	delete(c.looks, key)
	switch {
	case look.err == nil:
		if len(c.facts) >= maxKeptFacts {
			c.facts = map[string]keptFacts{} // each is found again when next needed
		}
		c.facts[fileID] = keptFacts{stamp: stamp, facts: look.facts}
		delete(c.lookFails, fileID)
	case !cutShort:
		// Old entries go when a new one comes.
		for id, f := range c.lookFails {
			if time.Since(f.at) >= lookRetryAfter {
				delete(c.lookFails, id)
			}
		}
		c.lookFails[fileID] = lookFailure{stamp: stamp, at: time.Now(), err: look.err}
	}
	c.factMu.Unlock()
	close(look.done)
}

// sizeFor is how large a film's picture is made at a setting whose frame is
// lines high. The arithmetic is hls.PictureFacts.Size's. Two kinds of
// picture are held against a different frame here, the one the decision
// goes by when it says what is lost (playback's fit and sizeOf):
//
//   - A picture that stands taller than it is wide, as a phone held upright
//     films it, is fitted to the frame stood on end. 1080 by 1920 is as
//     much picture as 1920 by 1080 and stays whole at the 1080p settings;
//     fitted by its height alone it came out 608 by 1080.
//   - A picture a few lines over 1080 (1088, as some files store 1080p) is
//     left as it is at the 1080p settings, where shaving off its eight
//     lines made 1906 by 1080 of it.
func sizeFor(f hls.PictureFacts, lines int) (w, h int) {
	// The picture as it is shown, at its own size: turned upright, and with
	// square pixels.
	ownW, ownH := f.Size(1 << 20)
	lyingW, lyingH := max(ownW, ownH), min(ownW, ownH)
	if lines == 1080 && lyingH <= 1088 && lyingW <= 1920 {
		return ownW, ownH
	}
	if ownH > ownW {
		// Stood on end the frame is lines wide and 16:9 of that high. Size
		// goes by the height: the most this picture may have is what
		// leaves it inside both.
		return f.Size(min(lines*ownH/ownW, lines*16/9))
	}
	return f.Size(lines)
}

// convertSpec works out how one film's picture is converted for one play:
// where it is cut, and the size and quality of the rung the decision
// picked.
func convertSpec(f hls.PictureFacts, v playback.Video) (hls.Grid, *hls.Convert, error) {
	if v.Rung < 1 || v.Rung > playback.NumRungs {
		return hls.Grid{}, nil, fmt.Errorf("the decision names setting %d, and there are %d", v.Rung, playback.NumRungs)
	}
	rung := playback.Ladder()[v.Rung-1]
	if rung.HalveFast && f.RateNum > 30*f.RateDen {
		// Every second picture is left out: the converted picture runs at
		// half the film's rate, and is cut by that rate's clock.
		if f.RateNum%2 == 0 {
			f.RateNum /= 2 // 50 becomes 25, 60000/1001 becomes 30000/1001
		} else {
			f.RateDen *= 2
		}
	}
	g, c := f.Lattice()
	if !g.Valid() {
		return hls.Grid{}, nil, errors.New("the film does not say enough about its picture's timing to be cut into chunks")
	}
	c.W, c.H = sizeFor(f, rung.Height)
	if c.W < 2 || c.H < 2 {
		return hls.Grid{}, nil, fmt.Errorf("the film gives its picture's size as %d by %d", f.Width, f.Height)
	}
	switch rung.Preset {
	case "veryfast":
		c.Preset = hls.VeryFast
	case "superfast":
		c.Preset = hls.SuperFast
	case "ultrafast":
		c.Preset = hls.UltraFast
	default:
		return hls.Grid{}, nil, fmt.Errorf("the decision names an effort %q that the converter does not know", rung.Preset)
	}
	c.MaxRateK = rung.MaxRate / 1000
	return g, &c, nil
}

// pictureOf fills in what the library knows about a film's picture beyond
// its format: how large it is, how many pictures a second it shows, and
// how it stores brightness and colour.
func pictureOf(t *playback.Track, width, height int, frameRate, colorTransfer, sideData string) {
	t.Width, t.Height = width, height
	t.FrameRate = frameRateOf(frameRate)
	t.Transfer = transferOf(colorTransfer)
	t.DoVi = doviOf(sideData)
}

// How a picture stores brightness, from the name ffprobe gives it.
func transferOf(colorTransfer string) string {
	switch colorTransfer {
	case "smpte2084":
		return playback.PQ
	case "arib-std-b67":
		return playback.HLG
	}
	return playback.SDR
}

// doviOf reads a picture's Dolby Vision profile from what the inspector
// noted about the track ("dovi:5"), 0 for none.
func doviOf(sideData string) int {
	for _, note := range strings.Split(sideData, ",") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(note), "dovi:"); ok {
			if n, err := strconv.Atoi(rest); err == nil && n > 0 && n < 100 {
				return n
			}
		}
	}
	return 0
}

// frameRateOf reads a frame rate as ffprobe writes it ("24000/1001", "25"),
// 0 where there is none.
func frameRateOf(text string) float64 {
	num, den, isRatio := strings.Cut(text, "/")
	a, err := strconv.ParseFloat(num, 64)
	if err != nil || a <= 0 {
		return 0
	}
	if !isRatio {
		return a
	}
	b, err := strconv.ParseFloat(den, 64)
	if err != nil || b <= 0 {
		return 0
	}
	return a / b
}

// planView is a decision as the page gets it: the decision itself, except
// that whether the film has sound is left open (null) for a file that has
// not been inspected, where the decision's "yes" is only a guess.
type planView struct {
	playback.Plan
	HasAudio *bool `json:"hasAudio"`
}

func viewOf(p playback.Plan, probed bool) planView {
	v := planView{Plan: p}
	if v.Reasons == nil {
		v.Reasons = []string{}
	}
	if v.Lost == nil {
		v.Lost = []string{}
	}
	if probed {
		v.HasAudio = &p.HasAudio
	}
	return v
}

// unplayable turns a decision into "this cannot be played here" for a
// reason found only when the film was about to be opened. What the
// decision said so far about why the original is not simply sent stays,
// with the new reason after it.
func unplayable(p playback.Plan, reason string) playback.Plan {
	return playback.Plan{Playable: playback.No, Method: playback.None, HasAudio: p.HasAudio, AudioTrack: p.AudioTrack,
		Reasons: append(append([]string{}, p.Reasons...), reason), Subtitles: playback.SubsNone, Lost: []string{}}
}
