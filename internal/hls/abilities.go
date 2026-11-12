package hls

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Abilities says what the ffmpeg on this PC was built to do, as far as
// converting a picture depends on it. Builds differ: the one shipped with
// the program has everything used here, one a Linux distribution provides
// may lack a filter or an encoder. It is asked once, at start (see
// ReadAbilities), and what to do is chosen from the answer, so that a
// command ffmpeg would refuse is never built.
//
// That a graphics-card encoder is listed says only that ffmpeg knows how to
// drive one. Whether this PC has such a card, and whether its driver works,
// is found by trying it.
type Abilities struct {
	// X264 is the encoder that runs on any PC, and Basics the handful of
	// filters every conversion uses. Without both, no picture is converted.
	X264   bool `json:"x264"`
	Basics bool `json:"basics"`
	// Graphics-card encoders: NVIDIA, Intel, AMD.
	NVENC bool `json:"nvenc"`
	QSV   bool `json:"qsv"`
	AMF   bool `json:"amf"`
	// Tonemapx turns HDR into normal colour in one step; Zscale and Tonemap
	// do it together where that one is missing.
	Tonemapx bool `json:"tonemapx"`
	Zscale   bool `json:"zscale"`
	Tonemap  bool `json:"tonemap"`
	// Bwdif turns TV's half-pictures into whole ones.
	Bwdif bool `json:"bwdif"`
	// Subtitles draws text subtitles into the picture, and Overlay lays
	// picture subtitles over it.
	Subtitles bool `json:"subtitles"`
	Overlay   bool `json:"overlay"`
}

// CanConvert reports whether this ffmpeg can convert a picture at all.
func (a Abilities) CanConvert() bool {
	return a.X264 && a.Basics
}

// basicFilters are the filters every conversion uses (see convertArgs).
var basicFilters = []string{"fps", "trim", "scale", "setsar", "format", "atrim"}

// ParseAbilities reads what "ffmpeg -filters" and "ffmpeg -encoders" print.
//
// Both are lists for people to read: a legend, then a line for each filter
// or encoder with a few letters of flags, its name and a description. Only
// the names are taken, from lines that have the shape of an entry, so that
// a legend worded differently in another version does no harm.
func ParseAbilities(filters, encoders []byte) Abilities {
	has := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(filters))
	for sc.Scan() {
		// " T.. bwdif   V->V   Deinterlace the input image."
		if f := strings.Fields(sc.Text()); len(f) >= 3 && strings.Contains(f[2], "->") {
			has[f[1]] = true
		}
	}
	a := Abilities{Basics: true,
		Tonemapx: has["tonemapx"], Zscale: has["zscale"], Tonemap: has["tonemap"],
		Bwdif: has["bwdif"], Subtitles: has["subtitles"], Overlay: has["overlay"]}
	for _, name := range basicFilters {
		a.Basics = a.Basics && has[name]
	}
	sc = bufio.NewScanner(bytes.NewReader(encoders))
	for sc.Scan() {
		// " V....D libx264   libx264 H.264 / AVC / MPEG-4 AVC (codec h264)"
		// The legend's lines have the same letters and then an equals sign.
		f := strings.Fields(sc.Text())
		if len(f) < 2 || f[1] == "=" || !strings.HasPrefix(f[0], "V") {
			continue
		}
		switch f[1] {
		case "libx264":
			a.X264 = true
		case "h264_nvenc":
			a.NVENC = true
		case "h264_qsv":
			a.QSV = true
		case "h264_amf":
			a.AMF = true
		}
	}
	return a
}

// ReadAbilities asks ffmpeg what it can do. It starts ffmpeg twice, each
// for a moment; nothing is converted.
func ReadAbilities(ctx context.Context, ffmpeg string) (Abilities, error) {
	if ffmpeg == "" {
		return Abilities{}, errors.New("ffmpeg is not available")
	}
	ask := func(what string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", what)
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("ffmpeg could not be asked what it can do: %w", err)
		}
		return out, nil
	}
	filters, err := ask("-filters")
	if err != nil {
		return Abilities{}, err
	}
	encoders, err := ask("-encoders")
	if err != nil {
		return Abilities{}, err
	}
	return ParseAbilities(filters, encoders), nil
}
