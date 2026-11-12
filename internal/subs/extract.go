package subs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tools are the two programs this package runs.
type Tools struct {
	FFmpeg, FFprobe string
}

const (
	// indexName is the file that says what a folder holds. It is written
	// last, when everything it lists is in place and complete, so a folder
	// that has one has all of it.
	indexName = "index.json"
	// workName is the folder inside the caller's that ffmpeg writes into.
	// Nothing in it counts until it has been moved out.
	workName = "making"
	// indexVersion is raised when what is written changes in a way an
	// older index does not describe; folders with another number are read
	// as empty and made again.
	indexVersion = 1
)

// ErrNotExtracted means a folder holds no finished result: nothing was
// ever written there, a run was cut short, or files have gone since.
var ErrNotExtracted = errors.New("the subtitles of this film have not been taken out")

// ErrBusy means a run for the same folder is still going.
var ErrBusy = errors.New("the subtitles of this film are being taken out right now")

// ErrNotReadThrough means the film could not be read to its end: its file
// stops before the film does (a copy that was broken off), or the drive it
// is on stopped answering. Nothing is kept of such a run. It is worth
// trying again later, when the file is whole or the drive is back.
var ErrNotReadThrough = errors.New("the film could not be read to its end")

// Each folder has one run at a time: two would write the same files.
var (
	busyMu sync.Mutex
	busy   = map[string]bool{}
)

// Extract reads film through once and writes every subtitle track it can
// use and every font the film carries into dir, which is made if it is not
// there. The files are named by this package (see Track.File and
// Font.File), never after anything the film says. Files with such names
// that an earlier run left in dir are removed; nothing else in it is
// touched.
//
// progress, if not nil, is told how far through the film the reading has
// got, as a whole percentage, each time that number rises; 100 is only
// said once everything is in place. It is called from the goroutine that
// called Extract.
//
// A track whose own data is damaged does not cost the film its other
// tracks: it is left out and marked (Track.Damaged), and the rest is a
// result like any other. Finding it takes several readings of the film
// (see salvage); while those go on, progress is silent until a reading
// gets further than the one before.
//
// When ctx ends, ffmpeg is stopped and nothing is kept. In every case
// where an error is returned the folder has no index, and Load says
// ErrNotExtracted: a run counts only when ffmpeg read the film to its end
// (ErrNotReadThrough if it could not), ended well, and every file it was
// to write is there and complete.
//
// A caller that may already have the result asks Load first; Extract
// always does the work.
func Extract(ctx context.Context, tools Tools, film, dir string, progress func(percent int)) (Result, error) {
	return (&extraction{tools: tools, film: film, dir: dir, progress: progress}).run(ctx)
}

type extraction struct {
	tools     Tools
	film, dir string
	progress  func(int)
	// reported is the last percentage that was passed on, so that the next
	// is always higher, also when the film has to be read more than once.
	reported int
	// prepared, when set, is called with the working folder when it is
	// ready and ffmpeg has not been started. Only tests set it: it lets
	// them put something in ffmpeg's way.
	prepared func(work string)
}

// job is everything a command line is made of besides the film's path:
// numbers, and words from this package's own lists.
type job struct {
	forms []form // one for each subtitle track, in order; formNone for a track that is left
	fonts []int  // which attachments are written out
	// fontsLeft is how many more might have been fonts, in a film that has
	// more attachments than are taken.
	fontsLeft int
	clock     clock
	// through has the film read to its end even when no track is taken
	// from it, to find out whether it can be.
	through bool
}

// only is the same job for some of the tracks.
func (j job) only(tracks []int) job {
	few := job{forms: make([]form, len(j.forms)), fonts: j.fonts, clock: j.clock}
	for _, n := range tracks {
		few.forms[n] = j.forms[n]
	}
	return few
}

// plan decides what is taken from a film.
func plan(f inventory) job {
	j := job{clock: f.clock}
	for n, t := range f.tracks {
		form := formOf(t.Codec)
		if n >= maxTracks {
			form = formNone
		}
		j.forms = append(j.forms, form)
	}
	for k, a := range f.attachments {
		switch {
		case !a.mayBeFont():
		case len(j.fonts) < maxFonts:
			j.fonts = append(j.fonts, k)
		default:
			j.fontsLeft++
		}
	}
	return j
}

// outputs is how many subtitle tracks the job writes.
func (j job) outputs() int {
	n := 0
	for _, f := range j.forms {
		if f != formNone {
			n++
		}
	}
	return n
}

// runs says whether there is anything for ffmpeg to do.
func (j job) runs() bool {
	// ffmpeg wants something to write. With no track to take out, that is
	// the clock alone; with no sound or picture either there is nothing.
	return j.outputs() > 0 || len(j.fonts) > 0 && j.clock != clockNone
}

// args builds the ffmpeg command line for one film. All of it but the
// film's path is made of numbers and of words that are written out here.
//
// It is run with the working folder as its current folder, so every file
// it writes is named by a bare name, and no folder of the PC is in it but
// the film's own.
func (j job) args(film string) []string {
	a := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-y"}
	// Attachments are written as the file is opened, each by its place
	// among them.
	for _, k := range j.fonts {
		a = append(a, "-dump_attachment:t:"+strconv.Itoa(k), fontFile(k))
	}
	// Every time is left as the film has it ("-copyts"). Otherwise ffmpeg
	// counts from the film's first moment of sound or picture, which is a
	// little off zero in most films and hours off in a TV recording, and
	// the subtitles would be on a different clock from the picture they
	// are later laid over.
	a = append(a, "-copyts", "-i", film)
	for n, f := range j.forms {
		if f == formNone {
			continue
		}
		a = append(a, "-map", "0:s:"+strconv.Itoa(n))
		switch f {
		case formASS:
			a = append(a, "-c", "copy", "-f", "ass")
		case formVTT:
			a = append(a, "-c", "webvtt", "-f", "webvtt")
		case formSUP:
			a = append(a, "-c", "copy", "-f", "sup")
		case formTXT, formMKS:
			// A Matroska file would also be given the film's title and
			// chapters, which have no business in a subtitle track.
			a = append(a, "-c", "copy", "-map_metadata", "-1", "-map_chapters", "-1", "-f", "matroska")
		}
		a = append(a, f.made(n))
	}
	// The clock: one more output, which is nothing but the time of each
	// piece of sound (or picture) as ffmpeg passes it, one number a line,
	// sent here at once. It says how far the reading has got. ffmpeg's own
	// progress report cannot be used: with "-copyts" it counts from
	// whatever time it happened to see first.
	switch j.clock {
	case clockSound:
		a = append(a, "-map", "0:a:0")
	case clockPicture:
		a = append(a, "-map", "0:V:0")
	default:
		return a
	}
	a = append(a, "-c", "copy")
	if j.outputs() == 0 && !j.through {
		// Only fonts are wanted, and those are already written by the time
		// reading begins: none of the film needs to be read.
		a = append(a, "-t", "0")
	}
	return append(a, "-f", "mkvtimestamp_v2", "-flush_packets", "1", "pipe:1")
}

func (e *extraction) run(ctx context.Context) (res Result, err error) {
	if e.tools.FFmpeg == "" {
		return Result{}, errors.New("ffmpeg is not available")
	}
	e.reported = -1
	dir, err := filepath.Abs(e.dir)
	if err != nil {
		return Result{}, err
	}
	// Whatever goes wrong, the error does not say where the folder is.
	defer func() {
		if err != nil {
			err = placeless(err, dir)
		}
	}()
	// ffmpeg is started in another folder, where a path that is not whole
	// would lead somewhere else: the film's, and ffmpeg's own if it was
	// given as a path and not as a bare name to look up.
	film, err := filepath.Abs(e.film)
	if err != nil {
		return Result{}, err
	}
	if filepath.Base(e.tools.FFmpeg) != e.tools.FFmpeg {
		if e.tools.FFmpeg, err = filepath.Abs(e.tools.FFmpeg); err != nil {
			return Result{}, err
		}
	}
	busyMu.Lock()
	if busy[dir] {
		busyMu.Unlock()
		return Result{}, ErrBusy
	}
	busy[dir] = true
	busyMu.Unlock()
	defer func() {
		busyMu.Lock()
		delete(busy, dir)
		busyMu.Unlock()
	}()

	if stopsEarly(film) {
		return Result{}, fmt.Errorf("%w: its file ends before the film does", ErrNotReadThrough)
	}
	seen, err := inspect(ctx, e.tools.FFprobe, film)
	if err != nil {
		return Result{}, err
	}
	j := plan(seen)
	res = Result{DurationMs: seen.durationMs, StartMs: seen.startMs, Tracks: seen.tracks, Fonts: []Font{}, FontsLeftOut: j.fontsLeft}
	if res.Tracks == nil {
		res.Tracks = []Track{}
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, err
	}
	// The index of an earlier run goes first, so that the folder never has
	// one while the files it lists are being replaced.
	if err := os.Remove(filepath.Join(dir, indexName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Result{}, err
	}
	// So does whatever a run that was cut short left in the working folder.
	work := filepath.Join(dir, workName)
	if err := removeWork(work); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Join(work, "fonts"), 0o700); err != nil {
		return Result{}, err
	}
	defer removeWork(work)
	if e.prepared != nil {
		e.prepared(work)
	}

	var damaged map[int]bool
	if j.runs() {
		err := e.convert(ctx, j, seen, film, work)
		if j.outputs() > 0 && dataFault(err) {
			damaged, err = e.salvage(ctx, j, seen, film, work, err)
		}
		if err != nil {
			return Result{}, err
		}
	}

	// ffmpeg says it is done. Each file is now looked at: it has to be
	// there and whole, and it gives up the facts that are kept about it.
	var ready []string
	for n := range res.Tracks {
		t := &res.Tracks[n]
		f := j.forms[n]
		if f == formNone {
			continue
		}
		if damaged[n] {
			t.Damaged = true
			continue
		}
		name := f.file(n)
		facts, size, err := check(f, work, n)
		if errors.Is(err, errNotText) {
			// A track that calls itself text and is not: left out, like a
			// track ffmpeg could make nothing of.
			t.Damaged = true
			continue
		}
		if err != nil {
			return Result{}, fmt.Errorf("subtitle track %d was not written whole: %w", n, err)
		}
		t.File, t.Size, t.FirstMs, t.Width, t.Height, t.Done = name, size, facts.FirstMs, facts.Width, facts.Height, true
		ready = append(ready, name)
	}
	for _, k := range j.fonts {
		name := fontFile(k)
		size, font, err := checkFont(filepath.Join(work, filepath.FromSlash(name)), seen.attachments[k])
		if err != nil {
			return Result{}, fmt.Errorf("font %d was not written whole: %w", k, err)
		}
		if !font {
			continue
		}
		a := seen.attachments[k]
		res.Fonts = append(res.Fonts, Font{K: k, File: name, Size: size, Name: clip(a.name), Mime: clip(a.mime)})
		ready = append(ready, name)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// Everything is moved into place, and only then is the index written.
	// What an earlier run left in the folder goes first.
	if err := os.MkdirAll(filepath.Join(dir, "fonts"), 0o700); err != nil {
		return Result{}, err
	}
	if err := clearOld(dir); err != nil {
		return Result{}, err
	}
	for _, name := range ready {
		if err := os.Rename(filepath.Join(work, filepath.FromSlash(name)), filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			return Result{}, err
		}
	}
	if err := writeIndex(dir, res); err != nil {
		return Result{}, err
	}
	if e.progress != nil {
		e.progress(100)
	}
	return res, nil
}

// ownFile matches the name of every file a finished folder can hold
// besides its index.
var ownFile = regexp.MustCompile(`^(s[0-9]+\.ass|t[0-9]+\.vtt|p[0-9]+\.(sup|mks)|fonts/f[0-9]+)$`)

// clearOld removes the files an earlier run left in a folder: every file
// with a name this package gives, and nothing else. Without this a folder
// that is used again (for a film that has changed, or after this package
// has) would keep tracks and fonts that the new result does not have; and
// a font left lying there would be drawn with, since the program that
// draws styled subtitles takes every font in the folder it is given.
func clearOld(dir string) error {
	for _, sub := range []string{"", "fonts"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if sub != "" {
				name = sub + "/" + name
			}
			if entry.IsDir() || !ownFile.MatchString(name) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeWork removes the working folder and what is in it. On Windows a
// folder can stay in use for a moment after the program that was working
// in it has been stopped, so a refusal is not taken as final at once.
func removeWork(work string) error {
	var err error
	for try := 0; try < 30; try++ {
		if err = os.RemoveAll(work); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// stopped is a run of ffmpeg that ended badly.
type stopped struct {
	err  error  // how it ended
	said string // its last words, without the film's folder
}

func (s *stopped) Error() string {
	return fmt.Sprintf("the converter stopped before the subtitles were written: %v %s", s.err, s.said)
}

func (s *stopped) Unwrap() error { return s.err }

// dataFault says whether ffmpeg ended the way it does when something in
// the film is not in order: with the status that stands for "invalid data
// found", or the one for "too many captions could not be read". (Out of
// disk space, a film that is not there, being stopped: those end with
// other numbers, and are no reason to look for a damaged track.)
//
// "Invalid data" is a negative number inside ffmpeg. As a status, Linux
// and macOS keep its lowest eight bits (183) and Windows all 32.
func dataFault(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	code := exit.ExitCode()
	return code == 183 || uint32(code) == 0xBEBBB1B7 || code == 69
}

// salvage is what is done when ffmpeg gave up on a film because something
// in it is damaged. One damaged track stops the whole pass; ffmpeg has no
// way of leaving just that one out, and what it says does not name the
// track in any form to rely on. So the film is read again for half the
// tracks at a time, and a half that fails is halved again, until each
// damaged track has failed in a reading of its own. Those are returned;
// every other track has by then been written in a reading that ended
// well.
//
// That costs a reading of the film for each try: for one damaged track
// among thirty some eight of them, where trying each track by itself
// would be thirty. It happens once for a film, since the outcome is kept.
//
// A track is only called damaged if the film itself can be read to its
// end. If nothing came out at all, the film is read once more for its
// clock alone; if that fails too, the fault is not a track's, and the
// first error is returned.
func (e *extraction) salvage(ctx context.Context, j job, seen inventory, film, work string, first error) (map[int]bool, error) {
	var all []int
	for n, f := range j.forms {
		if f != formNone {
			all = append(all, n)
		}
	}
	damaged := map[int]bool{}
	through := false // a reading has ended well
	// try reads the film for a group of tracks, and for parts of the group
	// if that fails. fails says the group is already known to fail as a
	// whole. It reports whether every track of the group is in order.
	var try func(group []int, fails bool) (bool, error)
	try = func(group []int, fails bool) (bool, error) {
		// (A single track is always read for, whatever is known: a track
		// is not blamed for what was only seen to go wrong in company.)
		if !fails || len(group) == 1 {
			err := e.convert(ctx, j.only(group), seen, film, work)
			switch {
			case err == nil:
				through = true
				return true, nil
			case !dataFault(err):
				return false, err
			case len(group) == 1:
				damaged[group[0]] = true
				return false, nil
			}
		}
		half := len(group) / 2
		sound, err := try(group[:half], false)
		if err != nil {
			return false, err
		}
		// If the first half is in order, the second is what fails.
		_, err = try(group[half:], sound)
		return false, err
	}
	if _, err := try(all, true); err != nil {
		return nil, err
	}
	if !through {
		if j.clock == clockNone {
			return nil, first // nothing to tell by how far a reading gets
		}
		none := j.only(nil)
		none.through = true
		if err := e.convert(ctx, none, seen, film, work); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, first
		}
	}
	return damaged, nil
}

// convert runs ffmpeg and follows its clock.
func (e *extraction) convert(ctx context.Context, j job, seen inventory, film, work string) error {
	cmd := exec.CommandContext(ctx, e.tools.FFmpeg, j.args(film)...)
	cmd.Dir = work
	hideWindow(cmd)
	// Should anything keep ffmpeg's output open after it has been stopped,
	// it is not waited for for ever.
	cmd.WaitDelay = 10 * time.Second
	said := &heard{}
	cmd.Stderr = said
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("the converter could not be started: %w", err)
	}
	reading := j.outputs() > 0 || j.through // otherwise only fonts are fetched, and none of the film is read
	reachedMs, timed := e.follow(out, seen, reading)
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return &stopped{err: err, said: pathless(said.String(), film)}
	}
	// ffmpeg ends well even when it could not read the film to its end: it
	// says what stopped it and makes do with what it has, and every file
	// it wrote is in good form as far as it goes. So what it said is
	// looked at.
	if failure := said.readFailure(); failure != "" {
		return fmt.Errorf("%w: %s", ErrNotReadThrough, pathless(failure, film))
	}
	// Its wording for that has changed before and will again. So a
	// complaint of any kind counts as that one if the reading also stopped
	// well short of the length the film says it has.
	//
	// The clock alone is not gone by. A film can be shorter than it says
	// without anything being wrong with it: its length is that of its
	// longest track, a single stray caption far past the end among them,
	// and the clock is only one of its tracks.
	if reading && timed && seen.durationMs > 0 && said.String() != "" {
		if short := seen.durationMs - (reachedMs - seen.startMs); short > max(10000, seen.durationMs/50) {
			return fmt.Errorf("%w: reading stopped %d seconds before the end: %s", ErrNotReadThrough, short/1000, pathless(said.String(), film))
		}
	}
	return nil
}

// follow reads the clock to its end, which is when ffmpeg closes it, and
// passes on how far through the film it has got. The numbers are the times
// of the film's own clock in thousandths of a second. It returns the
// latest of them, if there was one.
func (e *extraction) follow(out io.Reader, seen inventory, reading bool) (reachedMs int64, timed bool) {
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		ms, err := strconv.ParseInt(strings.TrimSpace(lines.Text()), 10, 64)
		if err != nil {
			continue // the heading
		}
		if !timed || ms > reachedMs {
			reachedMs, timed = ms, true
		}
		if e.progress == nil || !reading || seen.durationMs <= 0 {
			continue // still read, so that ffmpeg is never kept waiting
		}
		// 100 is kept for "finished and in place".
		percent := int(min(max((ms-seen.startMs)*100/seen.durationMs, 0), 99))
		if percent > e.reported {
			e.reported = percent
			e.progress(percent)
		}
	}
	// Whatever is left if a line was too long to be one of these.
	io.Copy(io.Discard, out)
	return reachedMs, timed
}

// check looks at what ffmpeg wrote for track n in the working folder. It
// returns what the track's file says about the track and the file's size,
// or an error if it is missing or not complete. The file is also made sure
// to be on the disk and not just on its way there, so that a full disk is
// found out now.
func check(f form, work string, n int) (PictureFacts, int64, error) {
	facts := PictureFacts{FirstMs: -1}
	path := filepath.Join(work, f.file(n))
	if f == formTXT {
		// The track's file is yet to be written, from ffmpeg's copy.
		first, size, err := textTrack(filepath.Join(work, f.made(n)), path)
		facts.FirstMs = first
		return facts, size, err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return facts, 0, err
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return facts, 0, err
	}
	st, err := file.Stat()
	if err != nil {
		return facts, 0, err
	}
	switch f {
	case formSUP:
		facts, err = readSUP(file, true)
	case formMKS:
		facts, err = readMKS(file, st.Size(), true)
	case formASS:
		// A styled track ends with a caption, which ffmpeg ends with the
		// end of a line, or with whatever the film's script had after its
		// captions, which ffmpeg 6.1 writes as it finds it, line end or
		// none. So a file that stops without a line end is cut short if
		// what it stops in is a caption.
		//
		// (One kind of script is turned away wrongly by this, with ffmpeg
		// 6.1 only: one whose own last line, after its captions, is
		// itself written like a caption and has no line end. The
		// converter that ships ends that line, and all is well.)
		facts.FirstMs, err = firstLine(file, st.Size(), assStart, func(last []byte) bool {
			return bytes.HasPrefix(last, []byte("Dialogue:")) || bytes.HasPrefix([]byte("Dialogue:"), last)
		})
	case formVTT:
		var head [6]byte
		if _, err := file.ReadAt(head[:], 0); err != nil || string(head[:]) != "WEBVTT" {
			return facts, 0, errNotWhole
		}
		// Every line of WebVTT that ffmpeg writes is ended.
		facts.FirstMs, err = firstLine(file, st.Size(), vttStart, func([]byte) bool { return true })
	}
	return facts, st.Size(), err
}

// firstLine goes through a text file line by line and returns the earliest
// time that at gives for any of them, or -1. If the file's last line has no
// line end, cut is asked whether a file may not stop there, given the
// line's beginning; if so the file is not whole.
func firstLine(file *os.File, size int64, at func(line []byte) (int64, bool), cut func(last []byte) bool) (int64, error) {
	first := int64(-1)
	if size == 0 {
		return first, nil // a track with nothing in it, not even a heading
	}
	var end [1]byte
	if _, err := file.ReadAt(end[:], size-1); err != nil {
		return first, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return first, err
	}
	br := bufio.NewReaderSize(file, 64<<10)
	var last []byte
	for {
		// A line may be far longer than the buffer (a drawing can run to
		// hundreds of thousands of characters). Its beginning is all that
		// is looked at; the rest is passed over.
		line, more, err := br.ReadLine()
		if err == io.EOF {
			if end[0] != '\n' && cut(last) {
				return first, errNotWhole
			}
			return first, nil
		}
		if err != nil {
			return first, err
		}
		if ms, ok := at(line); ok && (first < 0 || ms < first) {
			first = ms
		}
		last = append(last[:0], line[:min(len(line), 64)]...)
		for more && err == nil {
			_, more, err = br.ReadLine()
		}
	}
}

// assStart reads the time a line of a styled track begins to be shown.
// ffmpeg writes every one the same way: "Dialogue: 0,0:00:01.00,...".
func assStart(line []byte) (int64, bool) {
	rest, ok := bytes.CutPrefix(line, []byte("Dialogue:"))
	if !ok {
		return 0, false
	}
	fields := bytes.SplitN(rest, []byte(","), 3)
	if len(fields) < 3 {
		return 0, false
	}
	return assTime(string(fields[1]))
}

var vttCue = regexp.MustCompile(`^((?:\d+:)?\d{2}:\d{2}\.\d{3})[ \t]+-->`)

// vttStart reads the time from a line of WebVTT that gives a caption's
// times: "00:01.000 --> 00:04.000".
func vttStart(line []byte) (int64, bool) {
	m := vttCue.FindSubmatch(line)
	if m == nil {
		return 0, false
	}
	var total int64
	clock, frac, _ := strings.Cut(string(m[1]), ".")
	for _, p := range strings.Split(clock, ":") {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0, false
		}
		total = total*60 + v
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, false
	}
	return total*1000 + f, true
}

// checkFont looks at an attachment that was written out. Fonts are kept;
// anything else is removed again, since it was only written to find out.
// An attachment that is not there at all had nothing in it.
func checkFont(path string, a attachment) (size int64, font bool, err error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var head [4]byte
	n, _ := io.ReadFull(file, head[:])
	st, err := file.Stat()
	if err == nil {
		err = file.Sync()
	}
	file.Close()
	if err != nil {
		return 0, false, err
	}
	// What the file begins with decides. A film's word for it is taken
	// only where the beginning is not one this package knows: there are
	// more kinds of font file than are listed here.
	if st.Size() == 0 || !looksLikeFont(head[:n]) && !a.claimsFont() {
		return 0, false, os.Remove(path)
	}
	return st.Size(), true, nil
}

// index is the file that is written last.
type index struct {
	Version int `json:"version"`
	Result
}

func writeIndex(dir string, res Result) error {
	b, err := json.MarshalIndent(index{Version: indexVersion, Result: res}, "", " ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, indexName+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, indexName))
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Load returns what an earlier Extract left in dir. It answers
// ErrNotExtracted unless the folder holds a finished result with every
// file it lists still there at the length it was written with.
func Load(dir string) (Result, error) {
	path := filepath.Join(dir, indexName)
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 16<<20 {
		return Result{}, ErrNotExtracted
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Result{}, ErrNotExtracted
	}
	var idx index
	if err := json.Unmarshal(b, &idx); err != nil || idx.Version != indexVersion {
		return Result{}, ErrNotExtracted
	}
	res := idx.Result
	// The index is this package's own, but it is still a file on a disk:
	// a name in it is used only if it is one this package would have given.
	for n, t := range res.Tracks {
		if t.N != n {
			return Result{}, ErrNotExtracted
		}
		if t.File == "" && !t.Done {
			continue
		}
		if f := formOf(t.Codec); f == formNone || t.File != f.file(n) || !t.Done || !sized(dir, t.File, t.Size) {
			return Result{}, ErrNotExtracted
		}
	}
	for _, f := range res.Fonts {
		if f.K < 0 || f.File != fontFile(f.K) || !sized(dir, f.File, f.Size) {
			return Result{}, ErrNotExtracted
		}
	}
	return res, nil
}

// sized says whether a file of the folder is there, at this length.
func sized(dir, name string, size int64) bool {
	st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
	return err == nil && st.Mode().IsRegular() && st.Size() == size
}

// heard is what ffmpeg wrote to its error output: the last part of it, for
// saying what went wrong, and whether any of it said that the film could
// not be read.
type heard struct {
	mu     sync.Mutex
	b      []byte
	cut    bool   // the beginning has been dropped
	line   []byte // the line that is being written
	failed string // the first line that says reading failed
}

// What ffmpeg says, at one place or another, when reading a film fails or
// the file ends before it should. The first is said by ffmpeg itself
// whatever the kind of file, with the reason after it ("Input/output
// error", "Connection timed out"); the others by its readers of MKV and
// MP4 files.
var readFailures = []string{"error during demuxing", "error retrieving a packet from demuxer", "read error", "file ended prematurely", "partial file", "input/output error"}

func (h *heard) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.b = append(h.b, p...)
	if len(h.b) > 4096 {
		h.b, h.cut = h.b[len(h.b)-4096:], true
	}
	// Every line is looked at as it comes by: the one that matters may be
	// followed by more than is kept.
	for _, c := range p {
		if c != '\n' && c != '\r' {
			if len(h.line) < 512 {
				h.line = append(h.line, c)
			}
			continue
		}
		h.look()
	}
	return len(p), nil
}

// look takes in the line that has just been written.
func (h *heard) look() {
	if h.failed == "" {
		low := strings.ToLower(string(h.line))
		for _, words := range readFailures {
			if strings.Contains(low, words) {
				h.failed = strings.TrimSpace(string(h.line))
				break
			}
		}
	}
	h.line = h.line[:0]
}

// readFailure returns the line in which ffmpeg said it could not read the
// film, or "".
func (h *heard) readFailure() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.look() // a last line that was not ended
	return h.failed
}

func (h *heard) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.b
	if h.cut {
		// Only whole lines: the first is likely cut off at its beginning,
		// and half a path cannot be told from a whole one by pathless.
		_, rest, _ := bytes.Cut(b, []byte("\n"))
		b = rest
	}
	return strings.TrimSpace(string(b))
}

// placeless takes the place of the output folder out of an error's words:
// the folder is somewhere under the home of whoever runs the program, and
// errors are logged and travel in support bundles. What the error is about
// stays readable ("open the subtitle folder/making/s0.ass: ..."), and what
// kind of error it is can still be asked of it.
func placeless(err error, dir string) error {
	words := strings.ReplaceAll(err.Error(), dir, "the subtitle folder")
	for up := filepath.Dir(dir); len(up) > 1 && up != filepath.Dir(up); up = filepath.Dir(up) {
		words = strings.ReplaceAll(words, up, "a folder above the subtitle folder")
	}
	if words == err.Error() {
		return err
	}
	return &reworded{err: err, words: words}
}

// reworded is an error that says what another says in other words.
type reworded struct {
	err   error
	words string
}

func (r *reworded) Error() string { return r.words }
func (r *reworded) Unwrap() error { return r.err }

// pathless takes the folder the film is in out of what ffmpeg said.
// ffmpeg names the file it was given in full; only the file's own name is
// let out of this package, into errors that may be logged or travel in a
// support bundle.
func pathless(text, film string) string {
	text = strings.ReplaceAll(text, film, filepath.Base(film))
	if dir := filepath.Dir(film); len(dir) > 1 {
		// And anything else it names in the same folder.
		text = strings.ReplaceAll(text, dir+string(filepath.Separator), "")
	}
	return text
}
