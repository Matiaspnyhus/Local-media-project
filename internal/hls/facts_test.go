package hls

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// What ffprobe answered, recorded, for a few kinds of file. The parser is
// tested on these without running anything.
const (
	// A film as most are: Matroska, 23.976 frames a second. Its sound starts
	// a few thousandths of a second before the picture, which is where the
	// file's own clock starts.
	probedFilm = `{"packets":[{"pts_time":"0.000000"}],"programs":[],"streams":[{"width":1920,"height":800,"sample_aspect_ratio":"1:1",
		"field_order":"progressive","color_space":"bt709","r_frame_rate":"24000/1001","avg_frame_rate":"24000/1001","start_time":"0.000000",
		"tags":{"DURATION":"01:52:25.025000000"}}],"format":{"format_name":"matroska,webm","start_time":"-0.005000","duration":"6745.030000"}}`
	// A TV recording: MPEG-TS lists the picture twice (once under its
	// programme), starts its clock anywhere, and attaches empty notes.
	probedRecording = `{"packets":[{"pts_time":"36001.440000","side_data_list":[{}]}],
		"programs":[{"streams":[{"width":720,"height":576,"sample_aspect_ratio":"64:45","field_order":"tt","r_frame_rate":"50/1","avg_frame_rate":"25/1",
		"start_time":"36001.440000","duration":"3600.000000","side_data_list":[{}]}]}],
		"streams":[{"width":720,"height":576,"sample_aspect_ratio":"64:45","field_order":"tt","r_frame_rate":"50/1","avg_frame_rate":"25/1",
		"start_time":"36001.440000","duration":"3600.000000","side_data_list":[{}]}],
		"format":{"format_name":"mpegts","start_time":"36001.429978","duration":"3600.010022"}}`
	// A phone's video: MP4, filmed upright so stored on its side, and at a
	// rate that is 30 frames a second on a good day.
	probedPhone = `{"packets":[{"pts_time":"0.000000"}],"programs":[],"streams":[{"width":1920,"height":1080,"sample_aspect_ratio":"1:1",
		"color_space":"bt709","field_order":"progressive","r_frame_rate":"30/1","avg_frame_rate":"163830/5987","start_time":"0.000000","duration":"59.870000",
		"tags":{},"side_data_list":[{"rotation":-90}]}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","start_time":"0.000000","duration":"59.904000"}}`
)

func TestPictureFactsAreReadFromWhatFfprobeSays(t *testing.T) {
	f, end, err := parsePicture([]byte(probedFilm))
	want := PictureFacts{StartMs: 0, ContainerStartMs: -5, EndMs: 6745025, RateNum: 24000, RateDen: 1001,
		Width: 1920, Height: 800, SarNum: 1, SarDen: 1, FieldOrder: "progressive", Matrix: "bt709", Format: "matroska,webm"}
	if err != nil || f != want || end != 6745025 {
		t.Errorf("a film:\n got %+v, picture ends %d (%v)\nwant %+v, picture ends 6745025", f, end, err, want)
	}
	if !f.Indexed() || f.LeadMs() != 2000 {
		t.Errorf("a Matroska film: lists its key frames %v, lead-in %d ms; want true and 2000", f.Indexed(), f.LeadMs())
	}

	f, end, err = parsePicture([]byte(probedRecording))
	want = PictureFacts{StartMs: 36001440, ContainerStartMs: 36001430, EndMs: 39601440, RateNum: 25, RateDen: 1,
		Width: 720, Height: 576, SarNum: 64, SarDen: 45, FieldOrder: "tt", Format: "mpegts"}
	if err != nil || f != want || end != 39601440 {
		t.Errorf("a recording:\n got %+v, picture ends %d (%v)\nwant %+v, picture ends 39601440", f, end, err, want)
	}
	// Where a jump does not land on a key frame, the lead-in goes by how
	// far apart they were found to be: at least 2 seconds, at most 15, and
	// 15 when nothing was found.
	for gap, lead := range map[int64]int64{0: 15000, 480: 2000, 1500: 2000, 1501: 2001, 6000: 6500, 14500: 15000, 30000: 15000} {
		f.KeyGapMs = gap
		if f.Indexed() || f.LeadMs() != lead {
			t.Errorf("a recording with key frames %d ms apart: lead-in %d ms, want %d", gap, f.LeadMs(), lead)
		}
	}

	f, end, err = parsePicture([]byte(probedPhone))
	want = PictureFacts{StartMs: 0, ContainerStartMs: 0, EndMs: 59904, RateNum: 30, RateDen: 1,
		Width: 1920, Height: 1080, SarNum: 1, SarDen: 1, Rotation: 270, FieldOrder: "progressive", Matrix: "bt709", Format: "mov,mp4,m4a,3gp,3g2,mj2"}
	if err != nil || f != want || end != 59870 {
		t.Errorf("a phone's video:\n got %+v, picture ends %d (%v)\nwant %+v, picture ends 59870", f, end, err, want)
	}
	// It is turned upright before it is scaled, so it comes out upright.
	if w, h := f.Size(1080); w != 608 || h != 1080 {
		t.Errorf("a phone's upright video comes out %d by %d, want 608 by 1080", w, h)
	}
	// The facts survive being stored and read back.
	stored, _ := json.Marshal(want)
	var back PictureFacts
	if err := json.Unmarshal(stored, &back); err != nil || back != want {
		t.Errorf("stored as %s and read back as %+v (%v)", stored, back, err)
	}
}

func TestPictureFactsFromOddAnswers(t *testing.T) {
	stream := func(fields string) string {
		return `{"streams":[{"width":640,"height":480,` + fields + `}],"format":{"format_name":"avi","start_time":"0.000000","duration":"10.000000"}}`
	}
	// Things a file may leave out, or say in a way that means nothing. None
	// of it stops the film from being converted.
	f, end, err := parsePicture([]byte(stream(`"sample_aspect_ratio":"N/A","field_order":"unknown","color_space":"unknown","r_frame_rate":"0/0","avg_frame_rate":"0/0","start_time":"0.080000","duration":"N/A"`)))
	if err != nil || f.SarNum != 1 || f.SarDen != 1 || f.FieldOrder != "" || f.Matrix != "" || f.RateNum != 25 || f.RateDen != 1 || f.StartMs != 80 || f.EndMs != 10000 || end != 0 || f.Rotation != 0 {
		t.Errorf("a file that says little: %+v, picture ends %d (%v)", f, end, err)
	}
	for sar, want := range map[string][2]int{"0:1": {1, 1}, "1:0": {1, 1}, "-4:3": {1, 1}, "8:9": {8, 9}, "16:15": {16, 15}, "4/3": {4, 3}, "": {1, 1}, "x:y": {1, 1}, "100000:1": {1, 1}} {
		f, _, err := parsePicture([]byte(stream(`"sample_aspect_ratio":"` + sar + `","start_time":"0"`)))
		if err != nil || f.SarNum != want[0] || f.SarDen != want[1] {
			t.Errorf("a pixel shape given as %q was read as %d:%d (%v), want %d:%d", sar, f.SarNum, f.SarDen, err, want[0], want[1])
		}
	}
	for turn, want := range map[string]int{"90": 90, "-90": 270, "180": 180, "-180": 180, "270": 90 + 180, "0": 0, "-0": 0, "89.6": 90, "360": 0, "1e9": 0} {
		f, _, err := parsePicture([]byte(stream(`"start_time":"0","side_data_list":[{},{"rotation":` + turn + `}]`)))
		if err != nil || f.Rotation != want {
			t.Errorf("a picture turned by %s was read as turned by %d (%v), want %d", turn, f.Rotation, err, want)
		}
	}
	// No start given for the picture: the first frame stored says it.
	f, _, err = parsePicture([]byte(`{"packets":[{"pts_time":"0.042000"}],"streams":[{"width":640,"height":480,"start_time":"N/A"}],"format":{"format_name":"avi"}}`))
	if err != nil || f.StartMs != 42 || f.ContainerStartMs != 42 || f.EndMs != 0 {
		t.Errorf("a file that does not say when its picture starts: %+v (%v)", f, err)
	}
	// A Matroska file notes each track's length as a time of day, counted
	// from zero. Only one that reads as such is believed.
	for tag, want := range map[string]int64{"00:00:15.000000000": 15000, "01:52:25.025000000": 6745025, "00:00:15": 15000, "15": 0, "00:61:00.0": 0, "a:b:c": 0, "-1:00:00.0": 0, "": 0} {
		_, end, err := parsePicture([]byte(stream(`"start_time":"0.5","tags":{"DURATION":"` + tag + `"}`)))
		if err != nil || end != want {
			t.Errorf("a picture track whose length is noted as %q is taken to end at %d ms (%v), want %d", tag, end, err, want)
		}
	}
	// Answers that are no use at all are refused, with a reason.
	for _, bad := range []string{``, `{`, `[]`, `null`, `{}`, `{"streams":[]}`, `{"streams":[{"width":0,"height":480,"start_time":"0"}]}`,
		`{"streams":[{"width":640,"height":-1,"start_time":"0"}]}`, `{"streams":[{"width":640000,"height":480,"start_time":"0"}]}`,
		`{"streams":[{"width":640,"height":480}]}`, `{"streams":[{"width":640,"height":480,"start_time":"NaN"}]}`,
		`{"streams":[{"width":"640","height":480,"start_time":"0"}]}`} {
		if f, _, err := parsePicture([]byte(bad)); err == nil {
			t.Errorf("the answer %q was read as %+v", bad, f)
		}
	}
}

func TestFrameRatesAreSnappedToTheUsualOnes(t *testing.T) {
	for _, c := range []struct {
		avg, base string
		num, den  int64
	}{
		{"24000/1001", "24000/1001", 24000, 1001},
		{"24/1", "24/1", 24, 1},
		{"25/1", "25/1", 25, 1},
		{"30000/1001", "30000/1001", 30000, 1001},
		{"30/1", "30/1", 30, 1},
		{"50/1", "50/1", 50, 1},
		{"60000/1001", "60000/1001", 60000, 1001},
		{"60/1", "60/1", 60, 1},
		// As files really state them: nearly, not exactly.
		{"2997/125", "24000/1001", 24000, 1001},
		{"19001/317", "60/1", 60000, 1001},
		{"359/12", "30/1", 30000, 1001},
		// TV with half-pictures: the average is frames, the base rate fields.
		{"25/1", "50/1", 25, 1},
		{"30000/1001", "60000/1001", 30000, 1001},
		// Uneven recordings average below their real rate.
		{"163830/5987", "30/1", 30, 1},
		{"24/1", "30/1", 24, 1}, // but an average that is itself a usual rate is believed
		{"2743/100", "90000/1", 30000, 1001},
		// Slow and fast ones keep an even pace: each frame shown twice, or
		// every second one shown.
		{"15/1", "15/1", 30, 1},
		{"25/2", "25/2", 25, 1},
		{"12/1", "12/1", 24, 1},
		{"10/1", "10/1", 30, 1},
		{"120/1", "120/1", 60, 1},
		{"100/1", "100/1", 50, 1},
		{"120000/1001", "120000/1001", 60000, 1001},
		{"48/1", "48/1", 24, 1},
		// Anything else: the nearest.
		{"17/1", "17/1", 24000, 1001},
		{"1000/1", "1000/1", 60, 1},
		{"1/1", "1/1", 24000, 1001},
		// Nothing stated, or nonsense.
		{"0/0", "25/1", 25, 1},
		{"0/0", "0/0", 25, 1},
		{"", "", 25, 1},
		{"x", "-30/1", 25, 1},
		{"1/0", "30/0", 25, 1},
	} {
		if num, den := snapRate(rate(c.avg), rate(c.base)); num != c.num || den != c.den {
			t.Errorf("a picture at %q frames a second (counted in %q) is converted at %d/%d, want %d/%d", c.avg, c.base, num, den, c.num, c.den)
		}
	}
}

// The size of the converted picture: with square pixels, never enlarged,
// inside the frame its height stands for, and even both ways.
func TestTheConvertedPictureIsNeverEnlargedAndAlwaysEven(t *testing.T) {
	for _, c := range []struct {
		w, h, sarNum, sarDen, height int
		wantW, wantH                 int
		what                         string
	}{
		{1920, 1080, 1, 1, 1080, 1920, 1080, "full HD stays as it is"},
		{1920, 1080, 1, 1, 720, 1280, 720, "and is made smaller when asked"},
		{1920, 1080, 1, 1, 480, 854, 480, ""},
		{3840, 2160, 1, 1, 1080, 1920, 1080, "4K"},
		{3840, 1600, 1, 1, 1080, 1920, 800, "a wide 4K film fits the frame by its width"},
		{1920, 800, 1, 1, 1080, 1920, 800, "a wide film is not made higher"},
		{1920, 800, 1, 1, 720, 1280, 534, ""},
		{1280, 720, 1, 1, 1080, 1280, 720, "a small picture is not enlarged"},
		{640, 360, 1, 1, 1080, 640, 360, ""},
		{720, 576, 64, 45, 1080, 1024, 576, "a widescreen DVD is as wide as it is shown"},
		{720, 576, 16, 15, 1080, 768, 576, "a DVD in the old shape"},
		{720, 480, 32, 27, 1080, 854, 480, "an American widescreen DVD"},
		{720, 480, 8, 9, 1080, 640, 480, "and one in the old shape, whose pixels are narrow"},
		{1440, 1080, 4, 3, 1080, 1920, 1080, "HDV"},
		{1440, 1080, 4, 3, 720, 1280, 720, ""},
		{853, 480, 1, 1, 1080, 852, 480, "an odd width is rounded down, not up"},
		{1921, 1081, 1, 1, 1080, 1920, 1080, ""},
		{639, 479, 1, 1, 1080, 638, 478, ""},
		{1080, 1920, 1, 1, 1080, 608, 1080, "an upright picture"},
		{1080, 1920, 1, 1, 720, 406, 720, ""},
		{4096, 2160, 1, 1, 1080, 1920, 1012, "cinema 4K"},
		{320, 180, 1, 1, 1080, 320, 180, ""},
		{3, 3, 1, 1, 1080, 2, 2, "nothing comes out smaller than the encoder takes"},
		{1920, 1080, 0, 0, 1080, 1920, 1080, "a pixel shape that says nothing is square"},
		{0, 1080, 1, 1, 1080, 0, 0, "no picture, no size"},
		{1920, 1080, 1, 1, 0, 0, 0, ""},
	} {
		w, h := fit(c.w, c.h, c.sarNum, c.sarDen, c.height)
		if w != c.wantW || h != c.wantH {
			t.Errorf("%d by %d with %d:%d pixels, for %dp: %d by %d, want %d by %d %s", c.w, c.h, c.sarNum, c.sarDen, c.height, w, h, c.wantW, c.wantH, c.what)
		}
	}
	// And for any picture at all.
	for w := 16; w <= 4100; w += 37 {
		for h := 16; h <= 2200; h += 29 {
			for _, sar := range [][2]int{{1, 1}, {64, 45}, {8, 9}, {4, 3}} {
				for _, height := range []int{1080, 720, 480} {
					gw, gh := fit(w, h, sar[0], sar[1], height)
					shown := float64(w) * float64(sar[0]) / float64(sar[1])
					if gw < 2 || gh < 2 || gw%2 != 0 || gh%2 != 0 || gh > h || gh > height || float64(gw) >= shown+1 || gw > height*16/9+2 {
						t.Fatalf("%d by %d with %d:%d pixels, for %dp, comes out %d by %d", w, h, sar[0], sar[1], height, gw, gh)
					}
					// The shape is kept, as nearly as whole even numbers can.
					if got, want := float64(gw)/float64(gh), shown/float64(h); gw > 64 && gh > 64 && (got/want > 1.04 || want/got > 1.04) {
						t.Fatalf("%d by %d with %d:%d pixels, for %dp, comes out %d by %d: %.3f wide for its height, where the picture is %.3f", w, h, sar[0], sar[1], height, gw, gh, got, want)
					}
				}
			}
		}
	}
}

func TestKeyFrameGapsAndTheLastFrame(t *testing.T) {
	// A list of packets as ffprobe gives it: not in the order they are
	// shown, a key frame every so often, and the odd line that says nothing.
	times, keys, err := parsePackets([]byte(`{"packets":[{"pts_time":"1.480000","flags":"K__","side_data_list":[{}]},{"pts_time":"1.600000","flags":"___"},
		{"pts_time":"1.520000","flags":"__D"},{"pts_time":"N/A","flags":"___"},{"flags":"K__"},{"pts_time":"7.480000","flags":"K_"},{"pts_time":"7.440000","flags":"___"},
		{"pts_time":"10.480000","flags":"K__"},{"pts_time":"12.000000","flags":"___"}]}`))
	if err != nil || fmt.Sprint(times) != "[1480 1600 1520 7480 7440 10480 12000]" || fmt.Sprint(keys) != "[true false false true false true false]" {
		t.Fatalf("the packets were read as %v %v (%v)", times, keys, err)
	}
	if gap := keyGap(1480, times, keys); gap != 6000 {
		t.Errorf("the longest wait for a key frame is %d ms, want 6000", gap)
	}
	// The wait for the first key frame counts, and so does the time after
	// the last one seen.
	if gap := keyGap(0, []int64{0, 4000, 5000, 9000}, []bool{false, true, true, false}); gap != 4000 {
		t.Errorf("no key frame until 4 s, then a second apart, then none for 4 s: %d ms, want 4000", gap)
	}
	if gap := keyGap(0, []int64{0, 1000, 2000, 50000}, []bool{true, true, true, false}); gap != 48000 {
		t.Errorf("no key frame from 2 s to 50 s: %d ms, want 48000", gap)
	}
	// Only the first minute is looked at, and nothing from before the start.
	if gap := keyGap(10000, []int64{500, 10000, 12000, 69000, 71000, 99000}, []bool{true, true, true, true, true, true}); gap != 57000 {
		t.Errorf("key frames at 10, 12 and 69 s, then outside the minute: %d ms, want 57000", gap)
	}
	if gap := keyGap(0, nil, nil); gap != 0 {
		t.Errorf("no packets: %d", gap)
	}
	if _, _, err := parsePackets([]byte(`not json`)); err == nil {
		t.Error("junk was read as a list of packets")
	}
}

// The same facts from the real ffprobe, for the films the converter is
// tested with.
func TestPictureFactsOfRealFiles(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	ctx := context.Background()
	for name, want := range map[string]PictureFacts{
		"film24":    {StartMs: 0, LastMs: 22564, RateNum: 24000, RateDen: 1001},
		"tv25":      {StartMs: 1440, LastMs: 17400, RateNum: 25, RateDen: 1, KeyGapMs: 600},
		"phone":     {StartMs: 0, LastMs: 15967, RateNum: 30, RateDen: 1},
		"frozen":    {StartMs: 0, LastMs: 18967, RateNum: 30, RateDen: 1},
		"longsound": {StartMs: 0, LastMs: 9792, RateNum: 24, RateDen: 1},
		"longgop":   {StartMs: 1480, LastMs: 20440, RateNum: 25, RateDen: 1, KeyGapMs: 6000},
	} {
		src := convertFilm(t, ffmpeg, name)
		f, err := ProbePicture(ctx, ffprobe, src)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		// The two MPEG-TS films start where ffmpeg's writer of such files
		// chooses to start them, which has been 1.4 seconds in for years
		// but is not ours to rely on: they are judged from their own start.
		if strings.Contains(f.Format, "mpegts") {
			want.LastMs += f.StartMs - want.StartMs
			want.StartMs = f.StartMs
			if f.StartMs%1000 == 0 || f.Indexed() {
				t.Errorf("%s: the recording starts at %d ms and counts as listing its key frames: %v", name, f.StartMs, f.Indexed())
			}
		}
		if f.StartMs != want.StartMs || f.LastMs != want.LastMs || f.RateNum != want.RateNum || f.RateDen != want.RateDen || f.KeyGapMs != want.KeyGapMs {
			t.Errorf("%s: starts %d, last frame %d, rate %d/%d, key frames %d ms apart;\nwant starts %d, last frame %d, rate %d/%d, key frames %d ms apart",
				name, f.StartMs, f.LastMs, f.RateNum, f.RateDen, f.KeyGapMs, want.StartMs, want.LastMs, want.RateNum, want.RateDen, want.KeyGapMs)
		}
		if f.Width != 320 || f.Height != 180 || f.SarNum != 1 || f.SarDen != 1 || f.Rotation != 0 || f.FieldOrder != "progressive" || f.EndMs <= f.LastMs || f.ContainerStartMs > f.StartMs {
			t.Errorf("%s: %+v", name, f)
		}
	}
	// A file that is not there, and no ffprobe to ask.
	if _, err := ProbePicture(ctx, ffprobe, convertFilm(t, ffmpeg, "film24")+".gone"); err == nil {
		t.Error("a file that is not there has a picture")
	}
	if _, err := ProbePicture(ctx, "", "x"); err == nil {
		t.Error("without ffprobe a picture was found")
	}
}
