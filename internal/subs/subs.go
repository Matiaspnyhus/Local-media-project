// Package subs gets a film's subtitles ready to be used.
//
// A film can carry three kinds of subtitle track:
//
//   - text: words with times, which every device shows by itself;
//   - styled: words with their own fonts, colours, places and movement
//     (the ASS and SSA formats), which have to be drawn by a program that
//     knows the format, with the fonts the film brings along;
//   - picture: every caption is a small picture, as on discs and in TV
//     broadcasts, which can only be laid over the film's picture.
//
// Getting at any of them means reading the whole film once, so it is done
// once: Extract has ffmpeg read the film through and write every track and
// every font into a folder, under names this package chooses, and notes
// what it found in an index there. Load reads that index back.
//
// Besides that the package has what later steps need to work with what was
// taken out:
//
//   - Flatten turns a styled track into plain captions for a device that
//     cannot be given the styled form;
//   - ReadPicture says what size of picture a picture track was made for,
//     and when its first caption comes.
//
// Like internal/hls, this package builds its ffmpeg command line from typed
// values only. What a film says about itself (titles, languages, the names
// of its fonts) is kept as text to show, and never becomes part of a
// command or of a file name.
package subs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// Kind says what a subtitle track is made of.
type Kind string

const (
	Text    Kind = "text"
	Styled  Kind = "styled"
	Picture Kind = "picture"
	// Other is a track this program can do nothing with (teletext, the
	// captions hidden in a TV picture, and a few rarer ones).
	Other Kind = ""
)

// form is how a track is written out, which follows from its codec.
type form int

const (
	formNone form = iota
	formASS       // a styled track, copied as it is
	formVTT       // a text track, converted to the form browsers show
	formSUP       // disc pictures (PGS), copied into the file form made for them
	formMKS       // DVD and broadcast pictures, copied into a Matroska file
)

// formOf says how a track with this codec, as ffprobe names it, is written.
func formOf(codec string) form {
	switch codec {
	case "ass", "ssa":
		return formASS
	case "subrip", "srt", "webvtt", "vtt", "mov_text", "text":
		return formVTT
	case "hdmv_pgs_subtitle", "pgssub":
		return formSUP
	case "dvd_subtitle", "dvdsub", "dvb_subtitle", "dvbsub":
		return formMKS
	}
	return formNone
}

// KindOf says what kind of track a subtitle codec, as ffprobe names it,
// makes.
func KindOf(codec string) Kind { return formOf(codec).kind() }

func (f form) kind() Kind {
	switch f {
	case formASS:
		return Styled
	case formVTT:
		return Text
	case formSUP, formMKS:
		return Picture
	}
	return Other
}

// file is the name a track is written under. n is the track's place among
// the film's subtitle tracks, counted from 0, which is also how ffmpeg is
// told which track to read ("0:s:n") and how the rest of the program
// numbers them.
//
// Text tracks are written as WebVTT, not in the film's own text format.
// WebVTT is the one form browsers show, so the file can be sent as it is;
// one converter setting covers every text format a film can hold, where
// copying only works for some of them; and it is what the program has
// served for these tracks since it first showed subtitles.
func (f form) file(n int) string {
	switch f {
	case formASS:
		return "s" + strconv.Itoa(n) + ".ass"
	case formVTT:
		return "t" + strconv.Itoa(n) + ".vtt"
	case formSUP:
		return "p" + strconv.Itoa(n) + ".sup"
	case formMKS:
		return "p" + strconv.Itoa(n) + ".mks"
	}
	return ""
}

// fontFile is where attachment k of the film is written. Fonts go in a
// folder of their own, so that the program that draws styled subtitles can
// be pointed at it and finds fonts there and nothing else.
func fontFile(k int) string { return "fonts/f" + strconv.Itoa(k) }

// Track is one subtitle track of a film, and what became of it.
type Track struct {
	// N is its place among the film's subtitle tracks, counted from 0.
	N int `json:"n"`
	// Kind is what it is made of, and Codec the format's name as ffprobe
	// gives it.
	Kind  Kind   `json:"kind"`
	Codec string `json:"codec"`
	// Language, Title and the marks are what the film says about the
	// track. They are for showing and choosing, nothing else.
	Language        string `json:"language,omitempty"`
	Title           string `json:"title,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearingImpaired,omitempty"`
	Commentary      bool   `json:"commentary,omitempty"`
	// File is the track's file in the folder ("s0.ass", "t1.vtt",
	// "p2.sup", "p3.mks"), and Size its length in bytes. Both are empty for
	// a track that was not taken out.
	File string `json:"file,omitempty"`
	Size int64  `json:"size,omitempty"`
	// FirstMs is when the track's first caption is shown, or -1 if it has
	// none. Like every time in the files, it is on the film's own clock:
	// the one the converter cuts by, which need not start at zero (see
	// Result.StartMs).
	FirstMs int64 `json:"firstMs"`
	// Width and Height are the size of picture a picture track was made
	// for: its captions are placed, and sized, for a picture that big.
	// Both are 0 for other kinds.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Done says the whole track was written. It is false for a kind that
	// cannot be taken out, and for the tracks beyond the most this package
	// takes from one film.
	Done bool `json:"done"`
}

// JumpBaseMs is the moment ffmpeg counts from when it is told where to
// start reading ("-ss") in this track's file. It is not the same for the
// two kinds of file a picture track is kept in: in a ".sup" file ffmpeg
// counts from the first caption, in a ".mks" file from zero. To start
// reading at a moment of the film, give it that moment less this.
func (t Track) JumpBaseMs() int64 {
	if strings.HasSuffix(t.File, ".sup") && t.FirstMs > 0 {
		return t.FirstMs
	}
	return 0
}

// Font is one of the fonts a film brings along for its styled subtitles.
type Font struct {
	// K is its place among the film's attachments, counted from 0, as
	// ffmpeg counts them: a cover picture is not one of them to ffmpeg,
	// which shows it as a still picture instead.
	K int `json:"k"`
	// File is where it was written ("fonts/f0"), and Size its length.
	File string `json:"file"`
	Size int64  `json:"size"`
	// Name and Mime are what the film calls it and says it is. The name is
	// for showing only: it is text from the film, and never a file name
	// here.
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
}

// Result is what one film's folder holds.
type Result struct {
	// DurationMs is how long the film is, and StartMs where its own clock
	// starts. Every time in the files is on that clock; a player that
	// counts from the film's start has to take StartMs off.
	DurationMs int64 `json:"durationMs"`
	StartMs    int64 `json:"startMs"`
	// Tracks has every subtitle track of the film, in the film's order,
	// taken out or not.
	Tracks []Track `json:"tracks"`
	// Fonts has every font that was written.
	Fonts []Font `json:"fonts"`
}

// Track returns subtitle track n of the film.
func (r Result) Track(n int) (Track, bool) {
	if n < 0 || n >= len(r.Tracks) {
		return Track{}, false
	}
	return r.Tracks[n], true
}

// The most that is taken from one film. Real films stay far below both: a
// disc copy with every language has some thirty tracks, and a heavily
// styled show some dozens of fonts. The limits keep the command line within
// what Windows accepts whatever a file claims to hold.
const (
	maxTracks = 100
	maxFonts  = 300
)

// attachment is something a film carries that is neither picture, sound
// nor subtitle. Fonts are the ones that matter; cover pictures are common.
type attachment struct {
	name, mime string
}

// claimsFont says the film calls this attachment a font, by its name or by
// its stated type.
func (a attachment) claimsFont() bool {
	switch strings.ToLower(path.Ext(a.name)) {
	case ".ttf", ".otf", ".ttc", ".otc", ".woff", ".woff2", ".pfb", ".pfa":
		return true
	}
	m := strings.ToLower(a.mime)
	return strings.Contains(m, "font") || strings.Contains(m, "truetype") || strings.Contains(m, "opentype")
}

// mayBeFont says the attachment is worth writing out to look at: it claims
// to be a font, or does not say what it is.
func (a attachment) mayBeFont() bool {
	if a.claimsFont() {
		return true
	}
	m := strings.ToLower(strings.TrimSpace(a.mime))
	return m == "" || m == "application/octet-stream" || m == "binary/octet-stream"
}

// looksLikeFont says whether a file begins the way font files do.
func looksLikeFont(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	switch string(head[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "ttcf", "true", "typ1", "wOFF", "wOF2":
		return true
	}
	return false
}

// clock is the track whose times say how far through the film ffmpeg has
// read. The sound is preferred: it runs evenly from the start of a film to
// its end and is a small part of the file.
type clock int

const (
	clockNone clock = iota
	clockSound
	clockPicture
)

// inventory is what is known about a file before it is read through.
type inventory struct {
	durationMs, startMs int64
	clock               clock
	tracks              []Track
	attachments         []attachment
}

// inspect asks ffprobe what subtitle tracks and attachments a file has.
// The path is one argument, after fixed options.
func inspect(ctx context.Context, ffprobe, file string) (inventory, error) {
	if ffprobe == "" {
		return inventory{}, errors.New("ffprobe is not available")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-print_format", "json",
		"-show_entries", "format=start_time,duration:stream=codec_type,codec_name:stream_tags:stream_disposition", "-i", file)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return inventory{}, ctx.Err()
		}
		return inventory{}, fmt.Errorf("the film could not be inspected: %w", err)
	}
	return parseInspection(out)
}

// parseInspection reads ffprobe's answer.
func parseInspection(out []byte) (inventory, error) {
	var raw struct {
		Format struct {
			StartTime string `json:"start_time"`
			Duration  string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return inventory{}, fmt.Errorf("the film could not be inspected: %w", err)
	}
	f := inventory{durationMs: seconds(raw.Format.Duration), startMs: seconds(raw.Format.StartTime)}
	sound, picture := false, false
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "audio":
			sound = true
		case "video":
			// A cover picture is kept as a "video" of one frame; it has no
			// times to go by.
			if s.Disposition["attached_pic"] != 1 {
				picture = true
			}
		case "subtitle":
			lang := strings.ToLower(tag(s.Tags, "language"))
			if lang == "und" {
				lang = ""
			}
			title := tag(s.Tags, "title")
			f.tracks = append(f.tracks, Track{
				N: len(f.tracks), Kind: KindOf(s.CodecName), Codec: s.CodecName,
				Language: clip(lang), Title: clip(title),
				Default: s.Disposition["default"] == 1, Forced: s.Disposition["forced"] == 1,
				HearingImpaired: s.Disposition["hearing_impaired"] == 1,
				Commentary:      s.Disposition["comment"] == 1 || strings.Contains(strings.ToLower(title), "commentary"),
				FirstMs:         -1,
			})
		case "attachment":
			f.attachments = append(f.attachments, attachment{name: tag(s.Tags, "filename"), mime: tag(s.Tags, "mimetype")})
		}
	}
	switch {
	case sound:
		f.clock = clockSound
	case picture:
		f.clock = clockPicture
	}
	return f, nil
}

// seconds reads a time as ffprobe prints it ("6.023000") as milliseconds.
func seconds(s string) int64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v != v || v > 1e12 || v < -1e12 {
		return 0
	}
	if v < 0 {
		return int64(v*1000 - 0.5)
	}
	return int64(v*1000 + 0.5)
}

func tag(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}
