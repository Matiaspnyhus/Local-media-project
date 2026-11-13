package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests convert the picture, with the real ffmpeg, of small films
// made for the purpose. Each is a kind of film that goes wrong when the
// converter is told its business a little differently:
//
//	film24:    22.6 seconds at 23.976 frames a second, as nearly every cinema
//	           film on a disc is, with frames stored out of order. A frame
//	           is not a whole number of milliseconds, nor three seconds a
//	           whole number of frames.
//	tv25:      MPEG-2 in MPEG-TS, as a TV recording: its clock does not start
//	           at zero, and a jump in it does not land on a key frame.
//	phone:     30 frames a second with every fifth frame missing, as a phone
//	           records in poor light.
//	frozen:    19 seconds in which nothing new is shown for nine: the file
//	           holds no frame between 4 and 13 seconds.
//	longsound: 9.8 seconds of picture with 22 seconds of sound. The last
//	           chunk has 0.8 seconds of picture in it and all the rest of
//	           the sound.
//	longgop:   H.264 in MPEG-TS with a key frame only every six seconds.
//	midstream: tv25 with its beginning cut off in the middle of things, as
//	           every real recording begins: the first frames in the file
//	           cannot be decoded, for want of the key frame before them.
//	shortsound: 22 seconds of picture whose sound stops after 4.
func convertFilm(t *testing.T, ffmpeg, name string) string {
	t.Helper()
	key := "to convert: " + name
	if p := films[key]; p != "" {
		return p
	}
	if filmDir == "" {
		dir, err := os.MkdirTemp("", "lm-hls-src-")
		if err != nil {
			t.Fatal(err)
		}
		filmDir = dir
	}
	pic := func(rate, seconds string) []string {
		return []string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=" + rate + ":duration=" + seconds}
	}
	snd := func(seconds string) []string {
		return []string{"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=" + seconds}
	}
	x264 := []string{"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-pix_fmt", "yuv420p"}
	join := func(parts ...[]string) (all []string) {
		for _, p := range parts {
			all = append(all, p...)
		}
		return all
	}
	p := filepath.Join(filmDir, "convert-"+name+".mkv")
	var recipe []string
	switch name {
	case "film24":
		recipe = join(pic("24000/1001", "22.6"), snd("22.6"), x264, []string{"-g", "100", "-c:a", "ac3", "-b:a", "96k", p})
	case "tv25":
		p = strings.TrimSuffix(p, ".mkv") + ".ts"
		recipe = join(pic("25", "16"), snd("16"), []string{"-c:v", "mpeg2video", "-bf", "2", "-g", "15", "-b:v", "800k", "-pix_fmt", "yuv420p",
			"-c:a", "mp2", "-b:a", "128k", "-f", "mpegts", p})
	case "phone":
		recipe = join(pic("30", "16"), snd("16"), []string{"-vf", `select=not(eq(mod(n\,5)\,3))`, "-fps_mode", "vfr"}, x264,
			[]string{"-g", "120", "-c:a", "aac", "-b:a", "96k", p})
	case "frozen":
		recipe = join(pic("30", "19"), snd("19"), []string{"-vf", `select=not(between(t\,4.01\,13))`, "-fps_mode", "vfr"}, x264,
			[]string{"-g", "120", "-c:a", "aac", "-b:a", "96k", p})
	case "longsound":
		recipe = join(pic("24", "9.8"), snd("22"), x264, []string{"-g", "100", "-c:a", "aac", "-b:a", "96k", p})
	case "midstream":
		// MPEG-TS is a row of packets of 188 bytes, made to be picked up
		// anywhere. A quarter of a second's worth is taken off the front.
		whole, err := os.ReadFile(convertFilm(t, ffmpeg, "tv25"))
		p = strings.TrimSuffix(p, ".mkv") + ".ts"
		if err == nil {
			err = os.WriteFile(p, whole[min(188*160, len(whole)):], 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		films[key] = p
		return p
	case "longgop":
		p = strings.TrimSuffix(p, ".mkv") + ".ts"
		recipe = join(pic("25", "19"), snd("19"), x264, []string{"-g", "150", "-keyint_min", "150", "-sc_threshold", "0",
			"-c:a", "aac", "-b:a", "96k", "-f", "mpegts", p})
	case "shortsound":
		recipe = join(pic("24", "22"), snd("4"), x264, []string{"-g", "48", "-c:a", "ac3", "-b:a", "96k", p})
	default:
		t.Fatalf("no recipe for a film called %q", name)
	}
	if out, err := exec.Command(ffmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, recipe...)...).CombinedOutput(); err != nil {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatalf("ffmpeg could not make the test film %q: %v %s", name, err, out)
		}
		t.Skipf("this ffmpeg cannot make the test film %q: %v %s", name, err, out)
	}
	films[key] = p
	return p
}

// convertSpec works out how to convert a film the way the server will: the
// facts from ffprobe, the lattice from the facts, the size from both.
func convertSpec(t *testing.T, ffprobe, src string) (Spec, PictureFacts) {
	t.Helper()
	facts, err := ProbePicture(context.Background(), ffprobe, src)
	if err != nil {
		t.Fatalf("the facts about the picture: %v", err)
	}
	g, c := facts.Lattice()
	c.W, c.H = facts.Size(1080)
	c.Preset, c.MaxRateK = VeryFast, 10000
	if err := c.fits(g); err != nil {
		t.Fatalf("the lattice: %v\n%+v\n%+v", err, g, c)
	}
	return Spec{Path: src, Grid: g, AudioTrack: 0, Convert: &c}, facts
}

// eventually waits for ok to become true, for as long as a busy machine
// might need to convert a few seconds of a small film.
func eventually(ok func() bool) bool {
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return ok()
}

// joined writes a header and chunks out as one file a player could open.
func joined(t *testing.T, parts ...[]byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "joined.mp4")
	if err := os.WriteFile(p, bytes.Join(parts, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// alike says how alike the pictures of two streams are at the frames
// numbered, as ffmpeg measures it: in decibels, where the same frame
// encoded twice comes to 30 or more and two different frames of these films
// to under 20.
func alike(t *testing.T, ffmpeg, a, b string, frames ...int) []float64 {
	t.Helper()
	var pick []string
	for _, n := range frames {
		pick = append(pick, fmt.Sprintf(`eq(n\,%d)`, n))
	}
	sel := "select=" + strings.Join(pick, "+")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-i", a, "-i", b,
		"-lavfi", "[0:v]"+sel+"[a];[1:v]"+sel+"[b];[a][b]psnr=stats_file=-", "-f", "null", "-")
	var said bytes.Buffer
	cmd.Stderr = &said
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("comparing the pictures: %v %s", err, said.String())
	}
	var db []float64
	for _, field := range strings.Fields(string(out)) {
		if v, ok := strings.CutPrefix(field, "psnr_avg:"); ok {
			if v == "inf" {
				db = append(db, math.Inf(1))
			} else {
				db = append(db, f(v))
			}
		}
	}
	return db
}

// boxes lists the types of the boxes a chunk is made of.
func boxes(chunk []byte) (types []string) {
	children(chunk, func(typ string, _ []byte) { types = append(types, typ) })
	return types
}

// The heart of it, for a converted picture: wherever ffmpeg is started,
// each chunk begins exactly where the list said it would, holds exactly the
// same frames and goes with the same header; and the frames show the same
// pictures, which the times alone do not prove.
func TestEveryConvertedChunkBeginsWhereTheListSays(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	for _, film := range []struct {
		name string
		// The sound each way it can go beside a converted picture: passed
		// through (named by its codec) or converted (""). Sound in MPEG-TS
		// is always converted.
		sounds []string
	}{
		{"film24", []string{"ac3", ""}},
		{"tv25", []string{""}},
		{"phone", []string{"aac", ""}},
		{"frozen", []string{"aac", ""}},
		{"longsound", []string{"aac", ""}},
		{"longgop", []string{""}},
		{"midstream", []string{""}},
	} {
		src := convertFilm(t, ffmpeg, film.name)
		t.Run(film.name, func(t *testing.T) {
			t.Parallel() // the films are checked side by side to save time
			spec, facts := convertSpec(t, ffprobe, src)
			g, c := spec.Grid, spec.Convert
			t.Logf("picture from %d to %d ms in a file that ends at %d, %d/%d frames a second; %d chunks cut at %v; lead-in %d ms",
				facts.StartMs, facts.LastMs, facts.EndMs, c.Num, c.Den, len(g.Cuts), g.Cuts, c.LeadMs)
			switch film.name {
			case "film24":
				// The cut that a wrong rounding puts a frame early.
				if len(g.Cuts) != 8 || g.Cuts[7] != 21021 || c.Targets[7] != 21 {
					t.Errorf("the film should have a cut asked for at 21 s, at 21021 ms: %v %v", g.Cuts, c.Targets)
				}
			case "tv25", "longgop", "midstream":
				// (Where exactly such a file's clock starts is up to ffmpeg's
				// writer of them: 1.4 seconds in, for many years now.)
				if d := g.Cuts[0] - facts.StartMs; d < -20 || d > 20 || g.Cuts[0] < 500 || g.Cuts[1]%1000 != 0 || c.ContainerStartMs < 500 {
					t.Errorf("the recording's clock should start well after zero, and the cuts be on whole seconds from the second on: %v, picture from %d ms", g.Cuts, facts.StartMs)
				}
				if film.name == "longgop" && c.LeadMs != 6500 {
					t.Errorf("with key frames 6 seconds apart the lead-in is %d ms, want 6500", c.LeadMs)
				}
			case "longsound":
				if last := g.Cuts[len(g.Cuts)-1]; g.EndMs-facts.LastMs < 9000 || last > facts.LastMs || len(g.Cuts) != 4 || !c.SoundRunsOn {
					t.Errorf("the list should end with the picture (last frame at %d ms) and the file's end stay where it is (%d): %v", facts.LastMs, facts.EndMs, g.Cuts)
				}
			}
			if c.SoundRunsOn != (film.name == "longsound") {
				t.Errorf("the sound is taken to run on after the picture: %v", c.SoundRunsOn)
			}
			for _, sound := range film.sounds {
				spec.AudioCopy, spec.AudioCodec = sound != "", sound
				everyConvertedChunk(t, ffmpeg, ffprobe, spec, facts)
			}
		})
	}
}

// everyConvertedChunk fetches each chunk of a film twice, once played
// through from the start and once by a fresh session that asks for that
// chunk before anything else, and checks both against the list and against
// each other.
func everyConvertedChunk(t *testing.T, ffmpeg, ffprobe string, spec Spec, facts PictureFacts) {
	t.Helper()
	g, c := spec.Grid, spec.Convert
	how := "sound converted"
	if spec.AudioCopy {
		how = "sound passed through"
	}
	m := manager(t, ffmpeg)
	defer m.Close()
	ctx := context.Background()
	straight := m.Open(spec)
	header, err := m.Init(ctx, straight)
	if err != nil || len(header) == 0 {
		t.Fatalf("%s: the header: %v", how, err)
	}
	var through [][]byte
	for k := range g.Cuts {
		b, err := m.Segment(ctx, straight, k)
		if err != nil || len(b) == 0 {
			t.Fatalf("%s: chunk %d played through: %v", how, k, err)
		}
		through = append(through, b)
	}
	m.mu.Lock()
	starts := m.sessions[straight].starts
	m.mu.Unlock()
	if starts != 1 {
		t.Errorf("%s: watching straight through started ffmpeg %d times", how, starts)
	}

	// The film as a whole. It decodes; it has a frame on every tick from
	// the first frame to the last, however many the file itself holds; and
	// its key frames are the cuts and nothing else.
	whole := joined(t, append([][]byte{header}, through...)...)
	decodes(t, ffmpeg, whole)
	got := inspect(t, ffprobe, whole)
	_, tick0 := anchor(facts.StartMs, c.Num, c.Den)
	ticks := divRound(facts.LastMs*c.Num, c.Den*1000) - tick0 + 1
	if got["video"].Codec != "h264" || got["video"].Packets != strconv.FormatInt(ticks, 10) {
		t.Errorf("%s: played through: %s frames of %s, want %d of h264 (one on every tick from the first frame to the last)", how, got["video"].Packets, got["video"].Codec, ticks)
	}
	wantSound := "aac"
	if spec.AudioCopy {
		wantSound = spec.AudioCodec
	}
	if got["audio"].Codec != wantSound || !spec.AudioCopy && got["audio"].Channels != 2 {
		t.Errorf("%s: the sound came out as %s with %d channels", how, got["audio"].Codec, got["audio"].Channels)
	}
	keys := realKeyFrames(t, ffprobe, whole)
	for k, cut := range g.Cuts {
		if !keys[cut+clockShiftMs] {
			t.Errorf("%s: played through: no key frame at cut %d (%d ms)", how, k, cut)
		}
	}
	if len(keys) != len(g.Cuts) {
		t.Errorf("%s: played through: %d key frames for %d chunks", how, len(keys), len(g.Cuts))
	}

	perSecond := int(divRound(c.Num, c.Den))
	var slowest time.Duration
	for k := range g.Cuts {
		// A fresh session whose first request is for chunk k: ffmpeg is
		// started there, and the header is the one that run wrote.
		jumped := m.Open(spec)
		asked := time.Now()
		direct, err := m.Segment(ctx, jumped, k)
		if err != nil || len(direct) == 0 {
			t.Errorf("%s: chunk %d jumped to: %v", how, k, err)
			m.Stop(jumped)
			continue
		}
		slowest = max(slowest, time.Since(asked))
		h2, err := m.Init(ctx, jumped)
		m.Stop(jumped)
		// A player keeps the header it was given first, whichever run made
		// it, so every run must write the same one.
		if err != nil || !bytes.Equal(h2, header) {
			t.Errorf("%s: the header written by a run that began at chunk %d differs from the one written from the top (%v)", how, k, err)
		}
		// One fragment is one chunk.
		for name, chunk := range map[string][]byte{"played through": through[k], "jumped to": direct} {
			if types := boxes(chunk); fmt.Sprint(types) != "[moof mdat]" {
				t.Errorf("%s: chunk %d %s is made of %v, want one fragment", how, k, name, types)
			}
		}
		pa, pb := joined(t, header, through[k]), joined(t, h2, direct)
		a, b := inspect(t, ffprobe, pa), inspect(t, ffprobe, pb)
		begins := float64(g.Cuts[k])/1000 + shift
		for _, x := range []struct {
			name string
			info map[string]streamInfo
		}{{"played through", a}, {"jumped to", b}} {
			if start := f(x.info["video"].StartTime); math.Abs(start-begins) > 0.002 {
				t.Errorf("%s: chunk %d %s begins at %.4f s, the list says %.4f", how, k, x.name, start, begins)
			}
			// The sound may begin a little before or after the picture: it
			// comes in pieces of a few hundredths of a second. In the first
			// chunk it also begins as much earlier as it does in the file.
			early := 0.1
			if k == 0 {
				early += float64(facts.StartMs-facts.ContainerStartMs) / 1000
			}
			if off := f(x.info["video"].StartTime) - f(x.info["audio"].StartTime); off > early || off < -0.1 {
				t.Errorf("%s: chunk %d %s: picture and sound start %.3f s apart", how, k, x.name, off)
			}
		}
		if a["video"].Packets != b["video"].Packets || spec.AudioCopy && a["audio"].Packets != b["audio"].Packets {
			t.Errorf("%s: chunk %d differs: played through %s/%s frames, jumped to %s/%s", how, k,
				a["video"].Packets, a["audio"].Packets, b["video"].Packets, b["audio"].Packets)
		}
		// The pictures: the chunk's first frame and the one a second in (or
		// the last, in a chunk shorter than that). Frames that sit at the
		// right time but show the wrong moment of the film come to under 20.
		frames, _ := strconv.Atoi(a["video"].Packets)
		at := []int{0, min(perSecond, frames-1)}
		db := alike(t, ffmpeg, pa, pb, at...)
		if len(db) != 2 {
			t.Errorf("%s: chunk %d: the pictures could not be compared (%v)", how, k, db)
		}
		for i, v := range db {
			if v < 25 {
				t.Errorf("%s: chunk %d: frame %d shows a different picture played through and jumped to (%.1f dB alike, want 25 or more)", how, k, at[i], v)
			}
		}
	}
	t.Logf("%s: %d chunks, %s frames; the slowest jump took %v", how, len(g.Cuts), got["video"].Packets, slowest.Round(10*time.Millisecond))
}

// The whole command line, for a run from the top of the film and for one
// that begins at a chunk, as the design sets it out.
func TestTheCommandLineForAConvertedPicture(t *testing.T) {
	g, c := Lattice(0, 24983, 25025, 24000, 1001)
	c.ContainerStartMs, c.LeadMs, c.W, c.H, c.Preset, c.MaxRateK = -5, 2000, 1920, 800, VeryFast, 10000
	spec := Spec{Path: "film.mkv", Grid: g, AudioTrack: 1, AudioCopy: true, AudioCodec: "ac3", Convert: &c}
	out := " -sn -dn -map_metadata -1 -map_chapters -1 -copyts -avoid_negative_ts disabled -output_ts_offset 1.000 -max_muxing_queue_size 2048" +
		" -f mp4 -movflags +frag_keyframe+delay_moov+default_base_moof+frag_discont pipe:1"
	want := "-hide_banner -nostdin -loglevel error -readrate 8 -readrate_initial_burst 60 -i film.mkv -map 0:v:0" +
		" -vf fps=24000/1001:start_time=0.000,scale=1920:800:flags=bicubic,setsar=1,format=yuv420p" +
		" -c:v libx264 -preset veryfast -crf 22 -maxrate 10000k -bufsize 20000k -profile:v high -bf 0 -sc_threshold 0 -g 146 -forced-idr 1" +
		" -fps_mode vfr -force_key_frames 3,6,9,12,15,18,21,24 -map 0:a:1 -c:a copy" + out
	if got := strings.Join(args(spec, 0, 8, 60), " "); got != want {
		t.Errorf("from the top:\n got %s\nwant %s", got, want)
	}
	// Chunk 5 is cut at 15.015 s. The run starts reading 2 seconds before
	// that, counted from where the file's clock starts (5 ms below zero),
	// drops what comes before three frames ahead of the cut, and asks for
	// key frames from this cut on.
	spec.AudioCopy, c.Preset, c.MaxRateK = false, SuperFast, 5000
	want = "-hide_banner -nostdin -loglevel error -ss 13.020 -noaccurate_seek -readrate 8 -readrate_initial_burst 60 -i film.mkv -map 0:v:0" +
		" -vf fps=24000/1001:start_time=0.000,trim=start=14.890,scale=1920:800:flags=bicubic,setsar=1,format=yuv420p" +
		" -c:v libx264 -preset superfast -crf 22 -maxrate 5000k -bufsize 10000k -profile:v high -bf 0 -sc_threshold 0 -g 146 -forced-idr 1" +
		" -fps_mode vfr -force_key_frames 15,18,21,24 -map 0:a:1 -af atrim=start=14.890 -c:a aac -ac 2 -b:a 192k" + out
	if got := strings.Join(args(spec, 5, 8, 60), " "); got != want {
		t.Errorf("from chunk 5:\n got %s\nwant %s", got, want)
	}
	// Chunk 1 is cut at 3.003 s. With a lead-in so long that reading would
	// begin before the file does, the film is read from the top; what
	// comes before the chunk is dropped all the same.
	if near := strings.Join(args(spec, 1, 8, 60), " "); !strings.Contains(near, " -ss 1.008 -noaccurate_seek ") || !strings.Contains(near, ",trim=start=2.878,") {
		t.Errorf("from chunk 1: %s", near)
	}
	c.LeadMs = 4000
	if top := strings.Join(args(spec, 1, 8, 60), " "); strings.Contains(top, "-ss") || strings.Contains(top, "accurate") ||
		!strings.Contains(top, ",trim=start=2.878,") || !strings.Contains(top, " -af atrim=start=2.878 ") || !strings.Contains(top, " -force_key_frames 3,6,") {
		t.Errorf("from chunk 1 with a lead-in of 4 seconds: %s", top)
	}
	c.LeadMs = 2000
	// The last chunk still has its own key frame asked for; a film of one
	// chunk has none to ask for.
	if last := strings.Join(args(spec, 8, 8, 60), " "); !strings.Contains(last, " -force_key_frames 24 ") {
		t.Errorf("from the last chunk: %s", last)
	}
	g1, c1 := Lattice(0, 2960, 3000, 25, 1)
	c1.W, c1.H, c1.MaxRateK = 320, 180, 2500
	if one := strings.Join(args(Spec{Path: "x", Grid: g1, AudioTrack: -1, Convert: &c1}, 0, 8, 60), " "); strings.Contains(one, "force_key_frames") || strings.Contains(one, "0:a") || !strings.Contains(one, " -g 152 ") {
		t.Errorf("a film of one chunk, without sound: %s", one)
	}
	// A film whose sound runs on after its picture: ffmpeg is told to wait
	// for the picture however long it takes. No other film is.
	if strings.Contains(strings.Join(args(spec, 0, 8, 60), " ")+strings.Join(args(spec, 5, 8, 60), " "), "interleave") {
		t.Errorf("a film that ends with its picture has ffmpeg told how long to wait for it")
	}
	c.SoundRunsOn = true
	for _, seg := range []int{0, 5} {
		if on := strings.Join(args(spec, seg, 8, 60), " "); !strings.Contains(on, " -b:a 192k -max_interleave_delta 0 -sn ") {
			t.Errorf("a film whose sound runs on, from chunk %d: %s", seg, on)
		}
	}
	c.SoundRunsOn = false
	// Sound that is passed through as AAC is rewrapped, as it is beside an
	// untouched picture; times below zero are written as such; and each
	// preset has its word.
	c.AnchorMs, c.Preset = -42, UltraFast
	spec.AudioCopy, spec.AudioCodec = true, "aac"
	if odd := strings.Join(args(spec, 0, 8, 60), " "); !strings.Contains(odd, " -c:a copy -bsf:a aac_adtstoasc ") || !strings.Contains(odd, "start_time=-0.042,") || !strings.Contains(odd, " -preset ultrafast ") {
		t.Errorf("AAC passed through, a start below zero, the fastest preset: %s", odd)
	}
}

// The twin of TestCommandLineIsBuiltOnlyFromTypedValues for a converted
// picture. Its command line has far more in it, some of it text that
// ffmpeg reads as a little language of its own (the filters), so it matters
// all the more that nothing in it is text from outside: every argument but
// the file's path is made of numbers and of words from this package.
func TestConvertCommandLineIsBuiltOnlyFromTypedValues(t *testing.T) {
	path := `C:\Films\A "quoted"; rm -rf, [in] 'x' fps=1:start_time=9.mkv`
	plain := regexp.MustCompile(`^[A-Za-z0-9_.:=+/,-]+$`)
	for _, start := range []int64{0, 1440, 36000021, -2500} {
		g, c := Lattice(start, start+7200000, start+7200040, 30000, 1001)
		c.ContainerStartMs, c.LeadMs, c.W, c.H, c.MaxRateK = start-21, 6500, 1280, 534, 5000
		for _, sound := range []Spec{{AudioTrack: -1}, {AudioTrack: 2, AudioCopy: true, AudioCodec: "aac"}, {AudioTrack: 0, AudioCopy: true, AudioCodec: `ac3" -f null; `}, {AudioTrack: 3}} {
			spec := sound
			spec.Path, spec.Grid, spec.Convert = path, g, &c
			// Nothing from these is ever written into a command.
			spec.VideoCodec, spec.Owner = `hevc -vf "movie=/etc/passwd"`, `ann; shutdown`
			for _, seg := range []int{0, 1, 2, 1000, len(g.Cuts) - 1} {
				a := args(spec, seg, 8, 60)
				paths := 0
				for i, x := range a {
					switch {
					case x == path:
						// One argument, exactly as given, right after -i.
						if paths++; a[i-1] != "-i" {
							t.Errorf("the path follows %q", a[i-1])
						}
					case !plain.MatchString(x):
						t.Errorf("start %d, chunk %d: the argument %q is not made of plain words and numbers", start, seg, x)
					case strings.Contains(x, "Films") || strings.Contains(x, "passwd") || strings.Contains(x, "shutdown") || strings.Contains(x, "null"):
						t.Errorf("start %d, chunk %d: the argument %q holds text from outside", start, seg, x)
					}
				}
				if paths != 1 {
					t.Errorf("start %d, chunk %d: the path is given %d times: %q", start, seg, paths, a)
				}
				joined := strings.Join(a, "\x00")
				for _, want := range []string{"-c:v\x00libx264", "-bf\x000", "-sc_threshold\x000", "-forced-idr\x001", "-fps_mode\x00vfr", "-copyts",
					"fps=30000/1001:start_time=" + clock(c.AnchorMs) + ",", ",scale=1280:534:flags=bicubic,setsar=1,format=yuv420p\x00"} {
					if !strings.Contains(joined, want) {
						t.Errorf("start %d, chunk %d: missing %q in %q", start, seg, strings.ReplaceAll(want, "\x00", " "), a)
					}
				}
				if got, want := strings.Contains(joined, "-ss\x00"), seg > 0 && g.Cuts[seg]-6500-(start-21) > 0; got != want {
					t.Errorf("start %d, chunk %d: told where to start reading: %v, want %v", start, seg, got, want)
				}
				if got := strings.Contains(joined, "trim=start=") && strings.Contains(joined, "-noaccurate_seek") == strings.Contains(joined, "-ss\x00"); got != (seg > 0) {
					t.Errorf("start %d, chunk %d: frames before the chunk dropped: %v", start, seg, got)
				}
			}
		}
	}
}

// Windows takes a command line of at most 32,767 characters, and the
// converter's carries the time of every cut still to come. The longest it
// gets is for a film of the most chunks there may be, read from the top,
// with a file name as long as Windows ordinarily allows.
func TestTheLongestCommandLineFitsOnWindows(t *testing.T) {
	path := `C:\` + strings.Repeat("a", 259-len(`C:\.mkv`)) + ".mkv"
	if len(path) != 259 {
		t.Fatalf("the path is %d characters", len(path))
	}
	longest := 0
	for _, c := range []struct {
		name           string
		startMs, lenMs int64
	}{
		{"3 hours 20 minutes, the longest film with chunks of 3 seconds", 0, 12000 * 1000},
		{"the same as a recording whose clock starts ten hours in", 36000 * 1000, 12000 * 1000},
		{"the same with a clock as far in as MPEG-TS can count", 95443 * 1000, 12000 * 1000},
		{"a whole day's recording", 3600 * 1000, 24 * 3600 * 1000},
		{"a clock that starts four months in", 10000000 * 1000, 30000 * 1000},
	} {
		g, conv := Lattice(c.startMs, 0, c.startMs+c.lenMs, 60000, 1001)
		conv.ContainerStartMs, conv.LeadMs, conv.W, conv.H, conv.Preset, conv.MaxRateK = c.startMs-21, 15000, 1920, 1080, SuperFast, 12000
		if err := conv.fits(g); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		a := args(Spec{Path: path, Grid: g, AudioTrack: 11, AudioCopy: true, AudioCodec: "aac", Convert: &conv}, 0, 8, 60)
		// As Windows is given it: the arguments with spaces between them,
		// and quotes round each (more than are needed).
		n := len(strings.Join(a, `" "`)) + 2 + len(`C:\Program Files\Local Media\ffmpeg.exe" `)
		t.Logf("%s: %d chunks, a command line of %d characters", c.name, len(g.Cuts), n)
		if n >= 30000 {
			t.Errorf("%s: the command line is %d characters long, want under 30,000", c.name, n)
		}
		longest = max(longest, len(g.Cuts))
	}
	if longest != maxLatticeChunks {
		t.Errorf("the longest film tried has %d chunks, and the most there may be is %d", longest, maxLatticeChunks)
	}
}

// Which chunk a fragment of a converted picture belongs to, and when it
// belongs to none.
func TestAConvertedFragmentMustBeginOnTheNextCut(t *testing.T) {
	g := Grid{EndMs: 14000, Cuts: []int64{0, 3003, 6006, 9009, 12012}}
	for _, c := range []struct {
		first, cur int
		ms         float64
		seg        int
		lead, bad  bool
		what       string
	}{
		{0, -1, 0, 0, false, false, "the first fragment of a run from the top"},
		{0, 0, 3003, 1, false, false, "the next one, on its cut"},
		{0, 0, 3003.4, 1, false, false, "a cut is a whole number of milliseconds and a frame's time seldom is"},
		{0, 0, 3001.1, 1, false, false, "just inside what is allowed"},
		{0, 0, 3005.1, 0, false, true, "just outside it"},
		{0, 0, 2961, 0, false, true, "a frame early"},
		{0, 0, 3045, 0, false, true, "a frame late"},
		{0, 0, 6006, 0, false, true, "a cut skipped"},
		{0, 1, 3003, 0, false, true, "the same cut twice"},
		{0, 0, 1500, 0, false, true, "a key frame nobody asked for"},
		{3, -1, 8884, 0, true, false, "what a run for chunk 3 makes before the chunk"},
		{3, -1, 5000, 0, true, false, "however long before"},
		{3, -1, 9009, 3, false, false, "then the chunk"},
		{3, -1, 9100, 0, false, true, "a run for chunk 3 whose first fragment begins after the cut"},
		{3, 3, 12012, 4, false, false, "the last chunk"},
		{3, 4, 13000, 4, false, false, "after the last cut everything belongs to the last chunk"},
		{4, -1, 11900, 0, true, false, "a run for the last chunk"},
		{4, -1, 12012, 4, false, false, ""},
		{0, -1, -5, 0, true, false, "before the film begins"},
	} {
		seg, lead, err := latticeChunk(g, c.first, c.cur, c.ms)
		if (err != nil) != c.bad || lead != c.lead || err == nil && !lead && seg != c.seg {
			t.Errorf("a fragment at %.1f ms after chunk %d in a run for chunk %d: chunk %d, lead-in %v, %v; want chunk %d, lead-in %v, refused %v (%s)",
				c.ms, c.cur, c.first, seg, lead, err, c.seg, c.lead, c.bad, c.what)
		}
		if err != nil && (!errors.Is(err, ErrLeftLattice) || !strings.Contains(err.Error(), "left the lattice")) {
			t.Errorf("the refusal does not say that the picture left the lattice: %v", err)
		}
	}
}

// A conversion that does not go with its list of cuts is a mistake in the
// program. ffmpeg is not started for it, and the list is not swapped for an
// even one, as an untouched picture's is: the converter would not cut there.
func TestAConversionThatDoesNotFitItsListIsNotStarted(t *testing.T) {
	ffmpeg, _ := tools(t)
	g, c := Lattice(0, 24983, 25025, 24000, 1001)
	c.W, c.H, c.MaxRateK, c.LeadMs = 320, 180, 2500, 2000
	if err := c.fits(g); err != nil {
		t.Fatalf("a lattice and what came with it: %v", err)
	}
	other, _ := Lattice(0, 24983, 25025, 25, 1)
	shorter := g
	shorter.Cuts = g.Cuts[:5]
	change := func(f func(*Convert)) *Convert {
		changed := c
		changed.Targets = append([]int64(nil), c.Targets...)
		f(&changed)
		return &changed
	}
	m := manager(t, ffmpeg)
	for what, spec := range map[string]Spec{
		"no list at all":                {Grid: Grid{}, Convert: &c},
		"a list of fewer cuts":          {Grid: shorter, Convert: &c},
		"a list for another frame rate": {Grid: other, Convert: &c},
		"another frame rate":            {Grid: g, Convert: change(func(c *Convert) { c.Num, c.Den = 25, 1 })},
		"another film's start":          {Grid: g, Convert: change(func(c *Convert) { c.AnchorMs = 500 })},
		"another film's seconds":        {Grid: g, Convert: change(func(c *Convert) { c.Targets[4]++ })},
		"a list found from key frames":  {Grid: Grid{EndMs: 25025, Cuts: []int64{0, 4170, 8341, 12512, 16683, 20854, 22000, 23000, 24000}}, Convert: &c},
		"an odd width":                  {Grid: g, Convert: change(func(c *Convert) { c.W = 321 })},
		"no height":                     {Grid: g, Convert: change(func(c *Convert) { c.H = 0 })},
		"no frame rate":                 {Grid: g, Convert: change(func(c *Convert) { c.Num = 0 })},
		"no bit rate":                   {Grid: g, Convert: change(func(c *Convert) { c.MaxRateK = 0 })},
		"a lead-in below zero":          {Grid: g, Convert: change(func(c *Convert) { c.LeadMs = -1 })},
		"times that go backwards":       {Grid: g, Convert: change(func(c *Convert) { c.Targets[4] = c.Targets[3] })},
	} {
		spec.Path, spec.AudioTrack = "no such film.mkv", -1
		token := m.Open(spec)
		_, err := m.Segment(context.Background(), token, 0)
		_, err2 := m.Init(context.Background(), token)
		m.mu.Lock()
		starts := m.sessions[token].starts
		m.mu.Unlock()
		if err == nil || err2 == nil || starts != 0 || errors.Is(err, ErrPastEnd) && len(spec.Grid.Cuts) > 0 {
			t.Errorf("%s: %v, %v, ffmpeg started %d times", what, err, err2, starts)
		}
	}
}

// A converted picture costs the PC real work, so the converter stays
// closer to its viewer than when it only repackages: so many seconds of
// film ahead, whatever the length of the chunks.
func TestTheConverterOfAPictureWaitsWhenFarAheadAndForgetsWhatWasWatched(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec, _ := convertSpec(t, ffprobe, convertFilm(t, ffmpeg, "film24"))
	m := manager(t, ffmpeg)
	if m.ConvertAhead != 120 || m.ConvertBehind != 60 {
		t.Errorf("a converted picture is made %d seconds ahead and kept %d behind, want 120 and 60", m.ConvertAhead, m.ConvertBehind)
	}
	m.ConvertAhead, m.ConvertBehind = 7, 4 // two chunks of three seconds, and one
	m.Ahead, m.Behind = 50, 50             // which are not what a converted picture goes by
	ctx := context.Background()
	token := m.Open(spec)
	if _, err := m.Segment(ctx, token, 0); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	s := m.sessions[token]
	if m.ahead(s) != 2 || m.behind(s) != 1 || m.reach(s) != 2 {
		t.Errorf("for chunks of 3 seconds: %d ahead, %d behind, reach %d; want 2, 1 and 2", m.ahead(s), m.behind(s), m.reach(s))
	}
	m.mu.Unlock()
	state := func() (low, next int) {
		m.mu.Lock()
		defer m.mu.Unlock()
		return s.run.low, s.run.next
	}
	// It makes the two chunks it may, and there it waits: the fourth chunk
	// is ready inside ffmpeg and is not taken from it.
	if !eventually(func() bool { _, next := state(); return next == 3 }) {
		_, next := state()
		t.Fatalf("with the viewer at chunk 0 the converter got to chunk %d, want 3", next)
	}
	time.Sleep(300 * time.Millisecond) // long enough to run on, if it were going to
	if _, next := state(); next != 3 || m.Running() != 1 {
		t.Fatalf("with the viewer at chunk 0 the converter ran on to chunk %d (%d running)", next, m.Running())
	}
	// The viewer watches on; the converter follows, and old chunks go.
	for k := 1; k < len(spec.Grid.Cuts); k++ {
		if _, err := m.Segment(ctx, token, k); err != nil {
			t.Fatalf("chunk %d: %v", k, err)
		}
	}
	// The last chunk was finished with the viewer two chunks before it.
	if low, _ := state(); low < len(spec.Grid.Cuts)-4 {
		t.Errorf("chunks from %d are still kept with the viewer at chunk %d", low, len(spec.Grid.Cuts)-1)
	}
	m.mu.Lock()
	starts := s.starts
	m.mu.Unlock()
	if starts != 1 {
		t.Errorf("watching straight through started ffmpeg %d times", starts)
	}
	// A chunk one past the one being made is waited for; one two past it
	// is quicker to start ffmpeg afresh for. Each on a session that has
	// made chunks 0 to 2 and is about to make chunk 3.
	for ask, want := range map[int]int{4: 1, 5: 2} {
		fresh := m.Open(spec)
		if _, err := m.Segment(ctx, fresh, 0); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		fs := m.sessions[fresh]
		m.mu.Unlock()
		if !eventually(func() bool { m.mu.Lock(); defer m.mu.Unlock(); return fs.run.next == 3 }) {
			t.Fatal("the converter did not get two chunks ahead of its viewer")
		}
		if _, err := m.Segment(ctx, fresh, ask); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		starts := fs.starts
		m.mu.Unlock()
		if starts != want {
			t.Errorf("asked for chunk %d while chunk 3 was the next to be made: ffmpeg has been started %d times, want %d", ask, starts, want)
		}
		m.Stop(fresh)
	}
}

// The pace: seconds of film per second of the clock, from one chunk being
// finished to the next, without the time the converter was held back.
func TestPaceIsFilmMadePerSecondWithoutTheWaiting(t *testing.T) {
	var p pacer
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	const chunk = 3 * time.Second
	if pace, n := p.pace(); pace != 0 || n != 0 {
		t.Errorf("before anything is made: %v over %d chunks", pace, n)
	}
	// A run begins. Its first chunk has nothing to be measured from: the
	// time it took includes ffmpeg starting and finding its place.
	p.restart()
	p.made(chunk, at(4000))
	if pace, n := p.pace(); pace != 0 || n != 0 {
		t.Errorf("after one chunk: %v over %d chunks", pace, n)
	}
	// Each of the next takes two seconds: 1.5 times as fast as it plays.
	p.made(chunk, at(6000))
	if pace, n := p.pace(); math.Abs(pace-1.5) > 1e-9 || n != 1 {
		t.Errorf("3 seconds of film in 2: %v over %d chunks, want 1.5 over 1", pace, n)
	}
	p.made(chunk, at(8000))
	p.made(chunk, at(10000))
	if pace, n := p.pace(); math.Abs(pace-1.5) > 1e-9 || n != 3 {
		t.Errorf("9 seconds of film in 6: %v over %d chunks, want 1.5 over 3", pace, n)
	}
	// The converter is then made to wait five seconds for its viewer, and
	// takes two more to make the chunk. The five are not its fault.
	p.waited(3 * time.Second)
	p.waited(2 * time.Second)
	p.made(chunk, at(17000))
	if pace, n := p.pace(); math.Abs(pace-1.5) > 1e-9 || n != 3 {
		t.Errorf("after a wait of 5 seconds: %v over %d chunks, want 1.5 over 3 still", pace, n)
	}
	// The PC gets busy: six seconds a chunk. The pace is over the last
	// three chunks, so it sinks as the slow ones replace the fast ones.
	p.made(chunk, at(23000))
	if pace, _ := p.pace(); math.Abs(pace-0.9) > 1e-9 {
		t.Errorf("two chunks in 2 seconds each and one in 6: %v, want 0.9", pace)
	}
	p.made(chunk, at(29000))
	p.made(chunk, at(35000))
	if pace, n := p.pace(); math.Abs(pace-0.5) > 1e-9 || n != 3 {
		t.Errorf("three chunks in 6 seconds each: %v over %d, want 0.5 over 3", pace, n)
	}
	// A jump: a new run, whose first chunk is again not measured. What was
	// measured before still stands until there is something newer.
	p.restart()
	p.made(chunk, at(50000))
	if pace, n := p.pace(); math.Abs(pace-0.5) > 1e-9 || n != 3 {
		t.Errorf("after a jump: %v over %d, want 0.5 over 3 still", pace, n)
	}
	p.made(chunk, at(51000))
	if pace, _ := p.pace(); math.Abs(pace-9.0/13) > 1e-9 {
		t.Errorf("two chunks in 6 seconds each and one in 1: %v, want %v", pace, 9.0/13)
	}
	// The film's last chunk, which holds whatever sound outlasts the
	// picture, is given as holding no film and is not measured; and a wait
	// before the first chunk of a run does not count against the second.
	p.made(0, at(90000))
	if _, n := p.pace(); n != 3 {
		t.Errorf("the last chunk was measured")
	}
	p.restart()
	p.waited(time.Minute)
	p.made(chunk, at(200000))
	p.made(chunk, at(203000))
	if pace, _ := p.pace(); math.Abs(pace-9.0/10) > 1e-9 {
		t.Errorf("chunks in 6, 1 and 3 seconds: %v, want 0.9", pace)
	}
	// Chunks that arrive in the same instant, or a clock that steps back,
	// do not make the pace endless or negative.
	p.made(chunk, at(203000))
	p.made(chunk, at(202000))
	if pace, _ := p.pace(); pace <= 0 || math.IsInf(pace, 0) || math.IsNaN(pace) {
		t.Errorf("with no time passing: %v", pace)
	}
}

// The same from the real converter: after a few chunks there is a pace, it
// is that of a PC that keeps up with a film this small, and having been
// made to wait for the viewer does not count against it.
func TestPaceOfARealConversion(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec, _ := convertSpec(t, ffprobe, convertFilm(t, ffmpeg, "film24"))
	m := manager(t, ffmpeg)
	m.ConvertAhead = 3 // one chunk ahead: it is held back after every chunk
	ctx := context.Background()
	token := m.Open(spec)
	if pace, n := m.Pace(token); pace != 0 || n != 0 {
		t.Errorf("before anything is asked for: %v over %d chunks", pace, n)
	}
	pause := 1500 * time.Millisecond
	for k := 0; k < 6; k++ {
		asked := time.Now()
		if _, err := m.Segment(ctx, token, k); err != nil {
			t.Fatalf("chunk %d: %v", k, err)
		}
		if k == 0 {
			// On a machine so busy that the first chunk took a while, the
			// pause is made long enough to stand out all the same.
			pause = max(pause, 5*time.Since(asked))
		}
		if k == 2 {
			// The viewer pauses, in effect: the converter makes the one
			// chunk it may and then has nothing to do for this long. (A
			// sleep, because time going by is the thing being tested.)
			m.mu.Lock()
			r := m.sessions[token].run
			m.mu.Unlock()
			if !eventually(func() bool { m.mu.Lock(); defer m.mu.Unlock(); return r.next == 4 }) {
				t.Fatal("the converter did not get one chunk ahead of its viewer")
			}
			time.Sleep(pause)
		}
	}
	// The chunk made after the pause, chunk 4, is among the last three
	// whichever they are by now: 3 to 5, or 4 to 6.
	if !eventually(func() bool { _, n := m.Pace(token); return n == 3 }) {
		t.Fatal("after six chunks there are not three to take the pace from")
	}
	pace, n := m.Pace(token)
	m.mu.Lock()
	recent := append([]paceSample(nil), m.sessions[token].pace.recent...)
	m.mu.Unlock()
	t.Logf("%.1f seconds of film a second over %d chunks: %v", pace, n, recent)
	// A film this small is converted many times faster than it plays. Had
	// the pause been counted, one of the three chunks would have taken as
	// long as the pause.
	if n != 3 || pace < 1 || math.IsInf(pace, 0) {
		t.Errorf("the pace is %v over %d chunks", pace, n)
	}
	for _, s := range recent {
		if s.film < 3*time.Second || s.film > 3100*time.Millisecond || s.wall <= 0 || s.wall >= pause {
			t.Errorf("a chunk of %v of film is counted as made in %v; the converter was held back for %v, which is not to count", s.film, s.wall, pause)
		}
	}
	// A picture that is not converted has no pace, nor has a session that
	// is not there.
	plain := m.Open(Spec{Path: spec.Path, Grid: Uniform(22000), VideoCodec: "h264", AudioTrack: 0, AudioCopy: true})
	if _, err := m.Segment(ctx, plain, 1); err != nil {
		t.Fatal(err)
	}
	if pace, n := m.Pace(plain); pace != 0 || n != 0 {
		t.Errorf("an untouched picture has a pace of %v over %d chunks", pace, n)
	}
	if pace, n := m.Pace("nope"); pace != 0 || n != 0 {
		t.Errorf("an unknown session has a pace of %v over %d chunks", pace, n)
	}
}

// In many films the sound stops before the picture does. A viewer who
// jumps into the silent end of a film whose picture is converted must get
// the picture, as with an untouched one.
func TestJumpingToWhereTheSoundHasEndedInAConvertedFilm(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec, _ := convertSpec(t, ffprobe, convertFilm(t, ffmpeg, "shortsound"))
	last := len(spec.Grid.Cuts) - 1
	if last != 7 {
		t.Fatalf("the film was cut into %d chunks, want 8", last+1)
	}
	ctx := context.Background()
	for _, sound := range []string{"ac3", ""} {
		spec.AudioCopy, spec.AudioCodec = sound != "", sound
		m := manager(t, ffmpeg)
		m.Log = func(string, ...any) {} // the run that finds no sound says so
		token := m.Open(spec)
		header, err := m.Init(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		// Straight to the last chunk, from 21 seconds: no sound there.
		tail, err := m.Segment(ctx, token, last)
		if err != nil {
			t.Fatalf("sound %q: jumping to the silent end: %v", sound, err)
		}
		whole := joined(t, header, tail)
		got := inspect(t, ffprobe, whole)
		if got["video"].Packets != "24" || math.Abs(f(got["video"].StartTime)-21-shift) > 0.002 {
			t.Errorf("sound %q: the silent end has %s frames from %s s, want 24 from %.3f", sound, got["video"].Packets, got["video"].StartTime, 21+shift)
		}
		decodes(t, ffmpeg, whole)
		m.mu.Lock()
		starts, quietFrom := m.sessions[token].starts, m.sessions[token].quietFrom
		m.mu.Unlock()
		if starts != 3 || quietFrom != last {
			t.Errorf("sound %q: ffmpeg was started %d times and the sound is known to be over from chunk %d; want 3 times (the header, the jump, and again without the sound) and chunk %d", sound, starts, quietFrom, last)
		}
		// Back to where there is sound: it is there.
		early, err := m.Segment(ctx, token, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := inspect(t, ffprobe, joined(t, header, early)); f(got["audio"].Packets) < 20 || got["video"].Packets != "72" {
			t.Errorf("sound %q: back at 3 seconds: %s frames of picture, %s of sound", sound, got["video"].Packets, got["audio"].Packets)
		}
		m.Close()
	}
}

// The same film played through. Once the sound has stopped, ffmpeg holds
// the picture back until it is ten seconds of film ahead, and then sends it
// on as it is made. It must not hold it back until the film is done: a
// viewer near the end of a long film would wait minutes for the next chunk,
// which is what telling ffmpeg to wait for ever (as it is told for a film
// whose sound runs on) does to this one.
func TestAFilmWhoseSoundStopsEarlyStillArrivesAsItIsMade(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	spec, _ := convertSpec(t, ffprobe, convertFilm(t, ffmpeg, "shortsound"))
	if spec.Convert.SoundRunsOn {
		t.Fatal("a film whose sound stops first is taken for one whose sound runs on")
	}
	for _, sound := range []string{"ac3", ""} {
		spec.AudioCopy, spec.AudioCodec = sound != "", sound
		m := manager(t, ffmpeg)
		// Read at four times the speed it plays, with no head start: the 22
		// seconds take five and a half, so that "before the film is done"
		// is a moment that can be told from "when it is done".
		m.ReadRate, m.Burst = 4, 1
		ctx := context.Background()
		token := m.Open(spec)
		// The sound stops at 4 seconds, inside the second chunk. That chunk
		// is whole once the picture has been made up to 16 seconds or so.
		for k := 0; k < 2; k++ {
			if _, err := m.Segment(ctx, token, k); err != nil {
				t.Fatalf("sound %q: chunk %d: %v", sound, k, err)
			}
		}
		m.mu.Lock()
		r := m.sessions[token].run
		alive, next := r.alive(), r.next
		m.mu.Unlock()
		if !alive || next > 6 {
			t.Errorf("sound %q: the chunk in which the sound stops arrived when the converter was done with the film (still running: %v, chunks made: %d of 8)", sound, alive, next)
		}
		m.Close()
	}
}
