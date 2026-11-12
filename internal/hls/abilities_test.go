package hls

import (
	"context"
	"strings"
	"testing"
)

// What "ffmpeg -filters" and "ffmpeg -encoders" print, recorded from
// ffmpeg 6.1 and cut down to the lines that matter and a few that do not.
const (
	listedFilters = `Filters:
  T.. = Timeline support
  .S. = Slice threading
  ..C = Command support
  A = Audio input/output
  V = Video input/output
  N = Dynamic number and/or type of input/output
  | = Source or sink filter
 ... abench            A->A       Benchmark part of a filtergraph.
 ... atrim             A->A       Pick one continuous section from the input, drop the rest.
 ... ass               V->V       Render ASS subtitles onto input video using the libass library.
 TS. bwdif             V->V       Deinterlace the input image.
 ... format            V->V       Convert the input video to one of the specified pixel formats.
 ... fps               V->V       Force constant framerate.
 TSC overlay           VV->V      Overlay a video source on top of the input.
 ... overlay_vaapi     VV->V      Overlay one video on top of another
 ..C scale             V->V       Scale the input video size and/or convert the image format.
 ... scale_vaapi       V->V       Scale to/from VAAPI surfaces.
 ... setsar            V->V       Set the pixel sample aspect ratio.
 ... subtitles         V->V       Render text subtitles onto input video using the libass library.
 .S. tonemap           V->V       Conversion to/from different dynamic ranges.
 ... tonemap_opencl    V->V       Perform HDR to SDR conversion with tonemapping.
 ... trim              V->V       Pick one continuous section from the input, drop the rest.
 .SC zscale            V->V       Apply resizing, colorspace and bit depth conversion.
 ... testsrc2          |->V       Generate another test pattern.
 ... nullsink          V->|       Do absolutely nothing with the input video.
`
	listedEncoders = `Encoders:
 V..... = Video
 A..... = Audio
 S..... = Subtitle
 .F.... = Frame-level multithreading
 ..S... = Slice-level multithreading
 ...X.. = Codec is experimental
 ....B. = Supports draw_horiz_band
 .....D = Supports direct rendering method 1
 ------
 V....D a64multi             Multicolor charset for Commodore 64 (codec a64_multi)
 V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)
 V....D libx264rgb           libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 RGB (codec h264)
 V....D h264_nvenc           NVIDIA NVENC H.264 encoder (codec h264)
 V..... h264_qsv             H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (Intel Quick Sync Video acceleration) (codec h264)
 V..... h264_v4l2m2m         V4L2 mem2mem H.264 encoder wrapper (codec h264)
 V....D h264_vaapi           H.264/AVC (VAAPI) (codec h264)
 V.S... mpeg2video           MPEG-2 video
 A....D aac                  AAC (Advanced Audio Coding)
`
)

func TestAbilitiesAreReadFromFfmpegsLists(t *testing.T) {
	a := ParseAbilities([]byte(listedFilters), []byte(listedEncoders))
	want := Abilities{X264: true, Basics: true, NVENC: true, QSV: true, Zscale: true, Tonemap: true, Bwdif: true, Subtitles: true, Overlay: true}
	if a != want || !a.CanConvert() {
		t.Errorf("read as %+v, want %+v", a, want)
	}
	// A filter whose name only begins or ends like one that is wanted does
	// not count: "tonemap_opencl" is not "tonemapx", "libx264rgb" is not
	// "libx264", and "ass" is not "subtitles".
	only := func(filters, encoders string) Abilities { return ParseAbilities([]byte(filters), []byte(encoders)) }
	if a := only(" ... tonemap_opencl    V->V       x\n ... ass   V->V  x\n ... overlay_vaapi  VV->V  x\n ... scale_vaapi V->V x\n",
		" ------\n V....D libx264rgb   x\n V....D h264_vaapi x\n A....D libx264  not a picture encoder\n"); a != (Abilities{}) {
		t.Errorf("look-alikes were read as %+v", a)
	}
	// The pinned build has one more filter, and AMD's encoder; a build with
	// a longer row of flags, as newer versions print, reads the same.
	a = only(strings.ReplaceAll(listedFilters, " ... trim ", " ... tonemapx          V->V       SIMD optimized HDR to SDR tonemapping\n ..... trim "),
		listedEncoders+" V....D h264_amf             AMD AMF H.264 Encoder (codec h264)\n")
	if !a.Tonemapx || !a.AMF || !a.Basics || !a.X264 {
		t.Errorf("with tonemapx and h264_amf listed: %+v", a)
	}
	// Each of the filters every conversion uses is needed.
	for _, name := range basicFilters {
		without := strings.ReplaceAll(listedFilters, " "+name+" ", " "+name+"_gone ")
		if a := only(without, listedEncoders); a.Basics || a.CanConvert() || !a.X264 {
			t.Errorf("without the %q filter: %+v", name, a)
		}
	}
	if a := only(listedFilters, strings.ReplaceAll(listedEncoders, " libx264 ", " libopenh264 ")); a.X264 || a.CanConvert() || !a.Basics {
		t.Errorf("without libx264: %+v", a)
	}
	// The legend is not a list of encoders, whatever it is worded like, and
	// nothing at all is not an ffmpeg that can convert.
	if a := only("", " V..... = Video\n V..... libx264 = the best\n V = libx264\nlibx264\n\n"); !a.X264 || a.NVENC {
		t.Errorf("a list without its usual legend: %+v", a)
	}
	if a := only("", " V..... = libx264\n libx264\n = \n"); a.X264 {
		t.Errorf("a legend that names libx264 was read as having it: %+v", a)
	}
	for _, junk := range []string{"", "\n\n", "Filters:\n", "ffmpeg: command not found", strings.Repeat("x ", 10000), "\x00\xff\xfe"} {
		if a := only(junk, junk); a != (Abilities{}) || a.CanConvert() {
			t.Errorf("%.20q was read as %+v", junk, a)
		}
	}
}

// The ffmpeg that is here, asked for real. Every build this program is
// tested with can convert a picture; what else it can do differs.
func TestAbilitiesOfTheRealFfmpeg(t *testing.T) {
	ffmpeg, _ := tools(t)
	a, err := ReadAbilities(context.Background(), ffmpeg)
	if err != nil || !a.CanConvert() || !a.Overlay {
		t.Errorf("this ffmpeg: %+v (%v)", a, err)
	}
	t.Logf("this ffmpeg: %+v", a)
	if _, err := ReadAbilities(context.Background(), ""); err == nil {
		t.Error("no ffmpeg, and yet abilities")
	}
	if _, err := ReadAbilities(context.Background(), ffmpeg+"-that-is-not-there"); err == nil {
		t.Error("an ffmpeg that is not there, and yet abilities")
	}
}
