package probe

import (
	"context"
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

// ffprobe gives MKV and WebM one name ("matroska,webm"). Browsers do not
// treat them as one: Firefox and Safari play WebM and not MKV. So the two
// are told apart here, by the file's name or by what is inside.
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
		{"named .webm, whatever is inside", file("Odd.webm", "h264", "aac"), "webm"},
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
