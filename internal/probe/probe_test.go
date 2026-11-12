package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	out := []byte(`{"streams":[
	 {"index":0,"codec_name":"hevc","profile":"Main 10","codec_type":"video","width":3840,"height":2160,"pix_fmt":"yuv420p10le","level":153,"color_transfer":"smpte2084","color_primaries":"bt2020","avg_frame_rate":"24000/1001","disposition":{"default":1,"attached_pic":0},"side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":8}]},
	 {"index":1,"codec_name":"truehd","codec_type":"audio","channels":8,"channel_layout":"7.1","sample_rate":"48000","disposition":{"default":1},"tags":{"language":"eng","title":"Atmos"}},
	 {"index":2,"codec_name":"ac3","codec_type":"audio","channels":2,"sample_rate":"48000","bit_rate":"192000","disposition":{"default":0},"tags":{"LANGUAGE":"ENG","title":"Director's Commentary"}},
	 {"index":3,"codec_name":"hdmv_pgs_subtitle","codec_type":"subtitle","disposition":{"forced":1},"tags":{"language":"und"}},
	 {"index":4,"codec_name":"mjpeg","codec_type":"video","disposition":{"attached_pic":1}},
	 {"index":5,"codec_name":"ttf","codec_type":"attachment"}
	],"format":{"format_name":"matroska,webm","duration":"7265.504000","bit_rate":"52000000"}}`)
	r, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Container != "matroska" || r.DurationMs != 7265504 || len(r.Streams) != 4 {
		t.Fatalf("%+v", r)
	}
	v, a, c, s := r.Streams[0], r.Streams[1], r.Streams[2], r.Streams[3]
	if v.Codec != "hevc" || v.BitDepth != 10 || v.ColorTransfer != "smpte2084" || v.SideData != "dovi:8" || v.Height != 2160 {
		t.Errorf("video %+v", v)
	}
	if a.Codec != "truehd" || a.Channels != 8 || a.Language != "eng" || !a.Default || a.Commentary {
		t.Errorf("audio %+v", a)
	}
	if !c.Commentary || c.Language != "eng" || c.BitRate != 192000 {
		t.Errorf("commentary %+v", c)
	}
	if s.Kind != "subtitle" || !s.Forced || s.Language != "" {
		t.Errorf("subtitle %+v", s)
	}
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("garbage accepted")
	}
}

// A file is WebM by what is inside it, not by its name. Screen recorders,
// and Chrome's own recorder, write H.264 into files named .webm; Firefox and
// Safari play only VP8, VP9 and AV1 from a WebM, so such a file called WebM
// would be sent to them as it is and not play.
func TestAFileNamedWebMIsJudgedByWhatIsInside(t *testing.T) {
	ffprobe, ffmpeg := Find(), FindFFmpeg()
	if ffprobe == "" || ffmpeg == "" {
		if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg and ffprobe were not found, and LM_REQUIRE_FFMPEG says they must be")
		}
		t.Skip("ffmpeg and ffprobe are not installed here")
	}
	dir := t.TempDir()
	film := func(name string, codecs ...string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		args := append([]string{"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=12:duration=2", "-f", "lavfi", "-i", "sine=frequency=440:duration=2"}, codecs...)
		if out, err := exec.Command(ffmpeg, append(args, path)...).CombinedOutput(); err != nil {
			if os.Getenv("LM_REQUIRE_FFMPEG") != "" {
				t.Fatalf("ffmpeg could not make %s: %v %s", name, err, out)
			}
			t.Skipf("this ffmpeg cannot make %s: %v %s", name, err, out)
		}
		return path
	}
	webm := []string{"-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "8", "-b:v", "100k", "-c:a", "libopus", "-b:a", "24k"}
	for _, c := range []struct {
		why, path, want string
	}{
		{"H.264 and Opus in a file named .webm, as a recorder writes it",
			film("Recorder Clip (2018).webm", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "libopus", "-b:a", "24k", "-f", "matroska"), "matroska"},
		{"a real WebM", film("Real Clip (2018).webm", append(webm, "-f", "webm")...), "webm"},
		{"a real WebM named .mkv", film("Real Clip (2019).mkv", append(webm, "-f", "webm")...), "webm"},
	} {
		r, err := Run(context.Background(), ffprobe, c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.why, err)
		}
		if r.Container != c.want {
			t.Errorf("%s: stored as %q, want %q", c.why, r.Container, c.want)
		}
	}
}

// ffprobe gives MKV and WebM one name ("matroska,webm"). Browsers do not
// treat them as one: Firefox and Safari play WebM and not MKV. So the two
// are told apart here, by what is inside the file.
func TestWebMIsToldApartFromMKV(t *testing.T) {
	file := func(name string, streams ...string) []byte {
		list := ""
		for i, s := range streams {
			kind := "audio"
			switch s {
			case "vp8", "vp9", "av1", "h264", "hevc":
				kind = "video"
			case "webvtt", "subrip", "ass":
				kind = "subtitle"
			}
			if i > 0 {
				list += ","
			}
			list += `{"index":` + string(rune('0'+i)) + `,"codec_name":"` + s + `","codec_type":"` + kind + `"}`
		}
		return []byte(`{"streams":[` + list + `],"format":{"filename":"/films/` + name + `","format_name":"matroska,webm","duration":"10.0"}}`)
	}
	for _, c := range []struct {
		why  string
		out  []byte
		want string
	}{
		{"named .webm", file("Clip.webm", "vp9", "opus"), "webm"},
		{"named .WEBM", file("CLIP.WEBM", "vp8", "vorbis"), "webm"},
		{"named .webm, but not WebM inside", file("Odd.webm", "h264", "aac"), "matroska"},
		{"named .webm, with H.264 as a browser's recorder writes it", file("Recorder.webm", "h264", "opus"), "matroska"},
		{"an .mkv that holds only what WebM holds", file("Clip.mkv", "av1", "opus"), "webm"},
		{"the same with WebM's own kind of subtitles", file("Clip.mkv", "vp9", "opus", "webvtt"), "webm"},
		{"an ordinary .mkv", file("Film.mkv", "h264", "ac3"), "matroska"},
		{"VP9 picture, but sound WebM cannot hold", file("Film.mkv", "vp9", "aac"), "matroska"},
		{"VP9 and Opus, but subtitles WebM cannot hold", file("Film.mkv", "vp9", "opus", "subrip"), "matroska"},
		{"sound only", file("Song.mka", "opus"), "matroska"},
	} {
		r, err := Parse(c.out)
		if err != nil {
			t.Fatal(err)
		}
		if r.Container != c.want {
			t.Errorf("%s: stored as %q, want %q", c.why, r.Container, c.want)
		}
	}
	// A file that is not Matroska at all is not renamed by its extension.
	r, _ := Parse([]byte(`{"streams":[{"index":0,"codec_name":"vp9","codec_type":"video"}],"format":{"filename":"x.webm","format_name":"mov,mp4,m4a,3gp,3g2,mj2"}}`))
	if r.Container != "mp4" {
		t.Errorf("an MP4 named .webm is stored as %q", r.Container)
	}
}

// The label a file gives its picture format matters for HEVC in MP4:
// "hvc1" plays on Apple devices, "hev1" does not.
func TestTheCodecLabelIsKept(t *testing.T) {
	r, err := Parse([]byte(`{"streams":[
	 {"index":0,"codec_name":"hevc","codec_type":"video","codec_tag_string":"hev1","pix_fmt":"yuv420p"},
	 {"index":1,"codec_name":"aac","codec_type":"audio","codec_tag_string":"mp4a"},
	 {"index":2,"codec_name":"subrip","codec_type":"subtitle","codec_tag_string":"[0][0][0][0]"},
	 {"index":3,"codec_name":"h264","codec_type":"video","codec_tag_string":"[27][0][0][0]"}
	],"format":{"filename":"a.mp4","format_name":"mov,mp4,m4a,3gp,3g2,mj2"}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{r.Streams[0].CodecTag, r.Streams[1].CodecTag, r.Streams[2].CodecTag, r.Streams[3].CodecTag}
	if got[0] != "hev1" || got[1] != "mp4a" || got[2] != "" || got[3] != "" {
		t.Errorf("labels %q, want hev1, mp4a and two without", got)
	}
}

func TestRunOnRealFiles(t *testing.T) {
	ff := Find()
	if ff == "" {
		t.Skip("ffprobe is not installed here")
	}
	r, err := Run(context.Background(), ff, filepath.Join("..", "..", "e2e", "fixtures", "Dolby.Sound.Test.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Container != "matroska" || len(r.Streams) != 2 || r.Streams[0].Codec != "h264" || r.Streams[1].Codec != "ac3" || r.DurationMs < 7000 {
		t.Fatalf("%+v", r)
	}
	if _, err := Run(context.Background(), ff, filepath.Join("..", "..", "go.mod")); err == nil {
		t.Error("a text file was reported as media")
	}
}
