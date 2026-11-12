// Package probe asks ffprobe what is inside a media file. ffprobe is a
// separate program; this package only runs it and reads its answer.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Stream is one video, audio or subtitle track.
type Stream struct {
	Index           int
	Kind            string // video, audio, subtitle
	Codec           string
	Profile         string
	Level           int
	Width, Height   int
	PixFmt          string
	BitDepth        int
	ColorTransfer   string
	ColorPrimaries  string
	FrameRate       string
	Channels        int
	ChannelLayout   string
	SampleRate      int
	BitRate         int64
	Language        string
	Title           string
	Default         bool
	Forced          bool
	Commentary      bool
	HearingImpaired bool
	SideData        string
}

// Result is what ffprobe reported for one file.
type Result struct {
	DurationMs int64
	Container  string
	BitRate    int64
	Streams    []Stream
}

// Find locates ffprobe: the LOCALMEDIA_FFPROBE variable, then beside the
// program, then on the PATH. It returns "" when there is none.
func Find() string { return find("ffprobe", "LOCALMEDIA_FFPROBE") }

// FindFFmpeg locates ffmpeg, the converter, the same way.
func FindFFmpeg() string { return find("ffmpeg", "LOCALMEDIA_FFMPEG") }

func find(name, env string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p := os.Getenv(env); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if exe, err := os.Executable(); err == nil {
		for _, dir := range []string{filepath.Dir(exe), filepath.Join(filepath.Dir(exe), "ffmpeg")} {
			if p := filepath.Join(dir, name); fileExists(p) {
				return p
			}
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// Run inspects one file. The path is passed as a single argument after the
// options; nothing from a client ever reaches this command line.
func Run(ctx context.Context, ffprobe, path string) (Result, error) {
	if ffprobe == "" {
		return Result{}, errors.New("ffprobe is not available")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", "-i", path)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return Result{}, err
	}
	return Parse(out)
}

type rawProbe struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		Index          int               `json:"index"`
		CodecType      string            `json:"codec_type"`
		CodecName      string            `json:"codec_name"`
		Profile        string            `json:"profile"`
		Level          int               `json:"level"`
		Width          int               `json:"width"`
		Height         int               `json:"height"`
		PixFmt         string            `json:"pix_fmt"`
		BitsPerRaw     string            `json:"bits_per_raw_sample"`
		ColorTransfer  string            `json:"color_transfer"`
		ColorPrimaries string            `json:"color_primaries"`
		AvgFrameRate   string            `json:"avg_frame_rate"`
		Channels       int               `json:"channels"`
		ChannelLayout  string            `json:"channel_layout"`
		SampleRate     string            `json:"sample_rate"`
		BitRate        string            `json:"bit_rate"`
		Tags           map[string]string `json:"tags"`
		Disposition    map[string]int    `json:"disposition"`
		SideData       []struct {
			Type      string `json:"side_data_type"`
			DVProfile int    `json:"dv_profile"`
		} `json:"side_data_list"`
	} `json:"streams"`
}

// Parse reads ffprobe's JSON output.
func Parse(out []byte) (Result, error) {
	var raw rawProbe
	if err := json.Unmarshal(out, &raw); err != nil {
		return Result{}, err
	}
	res := Result{Container: container(raw.Format.FormatName), BitRate: atoi64(raw.Format.BitRate)}
	if d, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil {
		res.DurationMs = int64(d * 1000)
	}
	for _, s := range raw.Streams {
		kind := s.CodecType
		if kind != "video" && kind != "audio" && kind != "subtitle" {
			continue
		}
		// Cover art is stored as a video stream; it is not the film.
		if kind == "video" && s.Disposition["attached_pic"] == 1 {
			continue
		}
		st := Stream{
			Index: s.Index, Kind: kind, Codec: s.CodecName, Profile: s.Profile, Level: s.Level,
			Width: s.Width, Height: s.Height, PixFmt: s.PixFmt, ColorTransfer: s.ColorTransfer,
			ColorPrimaries: s.ColorPrimaries, FrameRate: s.AvgFrameRate, Channels: s.Channels,
			ChannelLayout: s.ChannelLayout, SampleRate: int(atoi64(s.SampleRate)), BitRate: atoi64(s.BitRate),
			Language: lower(s.Tags, "language"), Title: tag(s.Tags, "title"),
			Default: s.Disposition["default"] == 1, Forced: s.Disposition["forced"] == 1,
			Commentary: s.Disposition["comment"] == 1, HearingImpaired: s.Disposition["hearing_impaired"] == 1,
		}
		st.BitDepth = int(atoi64(s.BitsPerRaw))
		if st.BitDepth == 0 && kind == "video" {
			st.BitDepth = 8
			if strings.Contains(s.PixFmt, "10") {
				st.BitDepth = 10
			} else if strings.Contains(s.PixFmt, "12") {
				st.BitDepth = 12
			}
		}
		if t := strings.ToLower(st.Title); strings.Contains(t, "commentary") {
			st.Commentary = true
		}
		for _, sd := range s.SideData {
			if strings.Contains(sd.Type, "DOVI") {
				st.SideData = "dovi:" + strconv.Itoa(sd.DVProfile)
			}
		}
		res.Streams = append(res.Streams, st)
	}
	return res, nil
}

// container names the file format in one word; ffprobe lists every name a
// demuxer answers to ("mov,mp4,m4a,3gp,3g2,mj2", "matroska,webm").
func container(formatName string) string {
	switch {
	case strings.Contains(formatName, "matroska"):
		return "matroska"
	case strings.Contains(formatName, "mp4"):
		return "mp4"
	}
	if i := strings.Index(formatName, ","); i > 0 {
		return formatName[:i]
	}
	return formatName
}

func tag(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func lower(tags map[string]string, key string) string {
	v := strings.ToLower(tag(tags, key))
	if v == "und" {
		return ""
	}
	return v
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
