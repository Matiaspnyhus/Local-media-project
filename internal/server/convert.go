package server

import (
	"context"
	"errors"
	"fmt"
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
	// What the converter on this PC was built to do, asked once.
	mu      sync.Mutex
	asked   bool
	can     hls.Abilities
	retryAt time.Time // after a failed ask: not before this

	// Facts about films' pictures, kept while the program runs, and the
	// looks at films that are under way.
	factMu    sync.Mutex
	facts     map[string]keptFacts  // by file
	looks     map[string]*factsLook // by file and stamp
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

const (
	// maxFactLooks is how many films are looked at at once. A look is two
	// short reads, so this is hardly ever reached.
	maxFactLooks = 2
	// factLookLimit is how long one look may take. It takes a moment,
	// except on a disk that has to wake up first.
	factLookLimit = 45 * time.Second
	// maxKeptFacts bounds the memory the kept facts take.
	maxKeptFacts = 5000
)

func newConversion() *conversion {
	return &conversion{facts: map[string]keptFacts{}, looks: map[string]*factsLook{}, factSlots: make(chan struct{}, maxFactLooks)}
}

// abilities says what this PC's converter can do. The converter is asked
// once, when the program starts (see askConverterSoon), and the answer
// kept; a call that arrives while it is being asked waits for it. Should
// asking fail, nothing can be converted for now, and it is asked again a
// minute later: a PC that is busy starting up must not lose conversion
// until the program is next started.
func (s *Server) abilities() hls.Abilities {
	if s.ffmpeg == "" {
		return hls.Abilities{}
	}
	c := s.helpers.convert
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.asked && !time.Now().Before(c.retryAt) {
		s.askConverter(s.ffmpeg)
	}
	return c.can
}

// askConverter asks ffmpeg what it can do (it is started twice, for a
// moment each) and keeps the answer. The caller holds c.mu.
func (s *Server) askConverter(ffmpeg string) {
	c := s.helpers.convert
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	can, err := hls.ReadAbilities(ctx, ffmpeg)
	if err != nil {
		if s.ctx.Err() == nil {
			s.log.Printf("the converter could not be asked what it can do, so pictures are not converted for now: %s", s.pathless(err.Error()))
		}
		c.retryAt = time.Now().Add(time.Minute)
		return
	}
	c.asked, c.can = true, can
}

// askConverterSoon asks the converter what it can do, in the background, so
// that neither the program's start nor the first list or Play waits for
// ffmpeg to be loaded from the disk. The answer is held shut from this
// moment until it is there, so whoever needs it first gets this one.
func (s *Server) askConverterSoon() {
	c := s.helpers.convert
	ffmpeg := s.ffmpeg
	c.mu.Lock()
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		defer c.mu.Unlock()
		s.askConverter(ffmpeg)
	}()
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
	l := playback.Limits{Convert: s.abilities().CanConvert()}
	if u != nil {
		l.Busy = s.hls.Converting(u.SessionID)
	}
	return l
}

// pictureFacts returns the facts about a film's picture that converting it
// starts from (see hls.PictureFacts), looking at the file the first time
// and keeping the answer for as long as the file stays the same.
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
	select {
	case <-look.done:
		return look.facts, look.err
	case <-ctx.Done():
		return hls.PictureFacts{}, ctx.Err()
	}
}

// lookAtPicture runs one look, when its turn comes, and keeps what it finds.
func (s *Server) lookAtPicture(look *factsLook, key, fileID, stamp, path string) {
	defer s.background.Done()
	c := s.helpers.convert
	select {
	case c.factSlots <- struct{}{}:
		ctx, cancel := context.WithTimeout(s.ctx, factLookLimit)
		look.facts, look.err = hls.ProbePicture(ctx, s.ffprobe, path)
		cancel()
		<-c.factSlots
	case <-s.ctx.Done():
		look.err = s.ctx.Err()
	}
	c.factMu.Lock()
	delete(c.looks, key)
	if look.err == nil {
		if len(c.facts) >= maxKeptFacts {
			c.facts = map[string]keptFacts{} // each is found again when next needed
		}
		c.facts[fileID] = keptFacts{stamp: stamp, facts: look.facts}
	}
	c.factMu.Unlock()
	close(look.done)
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
	c.W, c.H = f.Size(rung.Height)
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
