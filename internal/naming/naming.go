// Package naming works out what a video file is from its path: a film with a
// title and year, or an episode of a show with a season and episode number.
// It is a pure function over the path; it never touches the disk.
package naming

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Kind says what a file was recognised as.
type Kind string

const (
	Movie   Kind = "movie"
	Episode Kind = "episode"
)

// Info is what the path told us.
type Info struct {
	Kind       Kind
	Title      string // film title, or show title for an episode
	Year       int    // 0 when unknown
	Season     int
	Episode    int
	EpisodeEnd int // last episode when one file holds several; 0 otherwise
}

var mediaExt = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true, ".ts": true,
	".m2ts": true, ".wmv": true, ".mpg": true, ".mpeg": true, ".srt": true, ".ass": true, ".ssa": true,
	".sub": true, ".idx": true, ".vtt": true, ".nfo": true, ".flv": true, ".divx": true, ".ogm": true, ".iso": true,
}

var (
	// Markers that say "this is an episode", most specific first. Each has
	// the season in group 1 and the episode in group 2.
	episodeMarkers = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,4})[ ._-]*(?:e|ep|x)[ ._-]?(\d{1,4})(?:(?:[ ._-]*(?:-|e|ep)[ ._-]?)(\d{1,4}))?`),
		regexp.MustCompile(`(?i)(?:^|[^a-z0-9])season[ ._-]*(\d{1,2})[ ._,-]*(?:episode|ep)[ ._-]*(\d{1,3})`),
		regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[ ._-]*\(?(?:episode|ep)[ ._-]*(\d{1,3})\)?`),
		regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:season|saison|staffel|temporada|stagione)[ ._-]*(\d{1,2})\b.*?(?:episode|episodio|épisode|folge|ep|e)[ ._(-]*(\d{1,3})`),
		regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})(?:[x×]| [x×] )(\d{1,3})(?:[^a-z0-9+]|$)`),
		regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})-(\d{1,3})(?:[^a-z0-9]|$)`),
		regexp.MustCompile(`(?i)\[cap\.(\d{1,2})(\d{2})(?:[_\]. ])`),
		regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})[ ._-]*(?:e|ep)[ ._-]?(\d{1,3})(?:[^a-z0-9]|$)`),
	}
	episodeOnly  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:episode|ep|e)[ ._-]*(\d{1,3})(?:[^a-z0-9]|$)`)
	seasonFolder = regexp.MustCompile(`(?i)^(?:.*[ ._-])?(?:season|series|saison|staffel|temporada|stagione|s)[ ._-]*(\d{1,2})(?:[ ._-]+[^0-9]*)?$`)
	seasonInName = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:(?:season|saison|staffel|temporada|stagione|tem)[ ._-]*(\d{1,2})|(\d{1,2})(?:st|nd|rd|th)[ ._-]season)(?:[^a-z0-9]|$)`)
	dashNumber   = regexp.MustCompile(`(?i)[ ._]-[ ._](\d{1,3})(?:v\d)?(?:[ ._]*(?:[\[(]|-[ ._]|$))`)
	compact      = regexp.MustCompile(`(?:^|[^a-z0-9])([1-9])(\d{2})(?:[^a-z0-9]|$)`)
	yearRe       = regexp.MustCompile(`(?:^|[^a-z0-9])((?:19|20)\d{2})(?:[^a-z0-9]|$)`)
	bracketed    = regexp.MustCompile(`^\s*(?:\[[^\]]*\]|\{[^}]*\})\s*`)
	sitePrefix   = regexp.MustCompile(`(?i)^\s*(?:\[\s*\.?)?www\.[a-z0-9-]+\.[a-z]{2,6}\b[\s\]_.-]*`)
	siteWord     = regexp.MustCompile(`(?i)^\s*(?:www[ .][a-z0-9-]+[ .][a-z]{2,6}\b|[a-z0-9-]+\.(?:com|org|net)\b)[\s\]_.-]*`)
	providerTag  = regexp.MustCompile(`(?i)[\[{(](?:tmdb|tvdb|imdb)(?:id)?[-= ][a-z0-9]+[\]})]`)
	spaces       = regexp.MustCompile(`\s+`)
	groupPrefix  = regexp.MustCompile(`^[a-z0-9]{2,6}-`)
	strongTag    = regexp.MustCompile(`(?i)^(?:` + strings.Join([]string{
		`\d{3,4}[pi]`, `\d{3,4}x\d{3,4}`, `[248]k`, `uhd`, `hdr(?:10)?(?:\+)?`,
		`blu-?ray`, `b[dr]-?rip(?:mux)?`, `b[dr]mux`, `bdremux`, `remux`, `dvd(?:-?rip|scr|r\d?|9|5|ivx)?`, `hd-?dvd`, `hd-?tv`, `pdtv`, `sdtv`, `dsr(?:ip)?`,
		`web-?dl(?:rip)?`, `web-?rip`, `hd-?rip`, `hd-?cam`, `cam-?rip`, `telesync`, `telecine`, `vhs-?rip`, `screener`, `ppv-?rip`, `vod-?rip`, `tv-?rip`,
		`[xh]\.?26[45]`, `hevc`, `xvid`, `divx`, `10-?bit`, `aac(?:\d\.?\d)?`, `ac-?3`, `e-?ac-?3`, `ddp?\+?(?:\d\.?\d)?`, `dts(?:-?hd|-?ma|-?x|es)?`, `truehd`, `atmos`,
		`nfofix`, `readnfo`, `repack`, `rerip`, `vostfr`, `truefrench`, `multisubs?`, `swesub`, `korsub`, `tc\d{3,4}p`,
	}, "|") + `)$`)
	// editionTag words end a title even right before the year ("Aliens SE
	// 1986"); the other weak words do not ("Madame Web 2024").
	editionTag = regexp.MustCompile(`(?i)^(?:se|dc|unrated|extended|uncut|remastered|limited|proper|imax|theatrical|internal|stv|retail|festival|custom|hq|edition|integral)$`)
	weakTag    = regexp.MustCompile(`(?i)^(?:` + strings.Join([]string{
		`web`, `cam`, `ts`, `tc`, `scr`, `r5`, `ppv`, `vod`, `vhs`, `dv`, `sdr`, `avc`, `av1`, `flac`, `mp3`, `opus`, `mpeg-?[24]`, `[57]\.1`, `3d`,
		`multi`, `dual`, `dubbed`, `subbed`, `subs?`, `vff?`, `vo`, `french`, `german`, `spanish`, `italian`, `ita`, `eng`, `english`, `hindi`, `tamil`, `telugu`, `nl`, `nordic`, `pal`, `ntsc`,
		`proper`, `real`, `internal`, `limited`, `unrated`, `extended`, `uncut`, `remastered`, `dc`, `se`, `stv`, `theatrical`, `imax`, `festival`, `complete`, `retail`, `hc`, `dubs?`, `convert`, `doku`, `xxx`,
		`amzn`, `nf`, `dsnp`, `hmax`, `atvp`, `hulu`, `cd\d(?:of\d)?`, `\dcd`,
	}, "|") + `)$`)
	tokenRe  = regexp.MustCompile(`[^\s._]+`)
	spacedRe = regexp.MustCompile(`[^\s_]+`)
	yearTok  = regexp.MustCompile(`^[(\[]?((?:19|20)\d{2})[)\]]?$`)
	groupRe  = regexp.MustCompile(`\s*[(\[{][^)\]}]*[)\]}]`)
	letters  = regexp.MustCompile(`(?:^| )((?:[A-Za-z] ){2,}[A-Za-z])(?: |$)`)
)

// Parse works out what the file at rel (a slash-separated path inside a
// library) is.
func Parse(rel string) Info {
	rel = strings.ReplaceAll(rel, `\`, "/")
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	file := parts[len(parts)-1]
	if ext := strings.ToLower(path.Ext(file)); mediaExt[ext] {
		file = file[:len(file)-len(ext)]
	}
	dirs := parts[:len(parts)-1]
	name := clean(file)

	if info, ok := parseEpisode(name, dirs); ok {
		return info
	}
	return parseMovie(name, dirs)
}

// clean removes the noise that can sit in front of a name.
func clean(s string) string {
	s = providerTag.ReplaceAllString(s, " ")
	for {
		t := sitePrefix.ReplaceAllString(s, "")
		t = siteWord.ReplaceAllString(t, "")
		if rest := bracketed.ReplaceAllString(t, ""); strings.TrimSpace(rest) != "" {
			t = rest
		}
		t = strings.TrimLeft(t, " -_.")
		if t == s {
			return s
		}
		s = t
	}
}

func parseEpisode(name string, dirs []string) (Info, bool) {
	for _, re := range episodeMarkers {
		m := re.FindStringSubmatchIndex(name)
		if m == nil {
			continue
		}
		info := Info{Kind: Episode, Season: atoi(name[m[2]:m[3]]), Episode: atoi(name[m[4]:m[5]])}
		if info.Episode == 264 || info.Episode == 265 {
			continue // "5 1 x265" is sound and a video format, not an episode
		}
		if len(m) >= 8 && m[6] >= 0 {
			if end := atoi(name[m[6]:m[7]]); end > info.Episode && end-info.Episode < 30 {
				info.EpisodeEnd = end
			}
		}
		info.Title, info.Year = showTitle(name[:m[0]], dirs)
		if info.Title == "" {
			return Info{}, false
		}
		return info, true
	}
	// A meaningless file name inside "Show.S02E05.720p-GRP/".
	for i := len(dirs) - 1; i >= 0; i-- {
		d := clean(dirs[i])
		for _, re := range episodeMarkers[:2] {
			if m := re.FindStringSubmatchIndex(d); m != nil {
				if title, year := titleAndYear(d[:m[0]], true); title != "" {
					return Info{Kind: Episode, Title: title, Year: year, Season: atoi(d[m[2]:m[3]]), Episode: atoi(d[m[4]:m[5]])}, true
				}
			}
		}
	}
	// "Show/Season 2/Episode 5.mkv", "Show/Season 2/05 - Name.mkv" and
	// "Show Season 2 Episode 5": the season comes from elsewhere.
	season := -1
	for i := len(dirs) - 1; i >= 0; i-- {
		if m := seasonFolder.FindStringSubmatch(strings.TrimSpace(dirs[i])); m != nil {
			season = atoi(m[1])
			break
		}
	}
	if m := seasonInName.FindStringSubmatch(name); m != nil && season < 0 {
		season = atoi(m[1] + m[2])
	}
	if season < 0 {
		// "[Group] Show - 08 [1080p]": numbered straight through, season 1.
		if m := dashNumber.FindStringSubmatchIndex(name); m != nil && (strings.Contains(name, "[") || hasStrong(name)) {
			if title, year := showTitle(name[:m[0]], dirs); title != "" {
				return Info{Kind: Episode, Title: title, Year: year, Season: 1, Episode: atoi(name[m[2]:m[3]])}, true
			}
		}
		// "Show.103.hdtv": season 1, episode 03, but only when followed
		// by release tags, so that a film called "300" stays a film.
		if m := compact.FindStringSubmatchIndex(strings.ToLower(name)); m != nil && m[0] > 0 && hasStrong(name[m[1]-1:]) && !yearRe.MatchString(name) {
			title, year := showTitle(name[:m[0]], dirs)
			if title != "" {
				return Info{Kind: Episode, Title: title, Year: year, Season: atoi(name[m[2]:m[3]]), Episode: atoi(name[m[4]:m[5]])}, true
			}
		}
		return Info{}, false
	}
	ep := -1
	cut := len(name)
	if m := episodeOnly.FindStringSubmatchIndex(name); m != nil {
		ep, cut = atoi(name[m[2]:m[3]]), m[0]
	} else if m := dashNumber.FindStringSubmatchIndex(name); m != nil {
		ep, cut = atoi(name[m[2]:m[3]]), m[0]
	} else if m := regexp.MustCompile(`^\s*(\d{1,3})(?:[^0-9a-z]|$)`).FindStringSubmatchIndex(name); m != nil {
		ep, cut = atoi(name[m[2]:m[3]]), 0
	}
	if ep < 0 {
		return Info{}, false
	}
	if m := seasonInName.FindStringIndex(name); m != nil && m[0] < cut {
		cut = m[0]
	}
	title, year := showTitle(name[:cut], dirs)
	if title == "" {
		return Info{}, false
	}
	return Info{Kind: Episode, Title: title, Year: year, Season: season, Episode: ep}, true
}

// showTitle takes the text in front of an episode marker; when that is
// empty it falls back on the show's folder.
func showTitle(before string, dirs []string) (string, int) {
	if m := seasonInName.FindStringIndex(before); m != nil && m[0] > 0 {
		before = before[:m[0]]
	}
	title, year := titleAndYear(strings.TrimRight(before, " ._-"), true)
	if title != "" {
		return title, year
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		d := strings.TrimSpace(dirs[i])
		if d == "" {
			continue
		}
		if m := seasonFolder.FindStringSubmatchIndex(d); m != nil {
			// "Season 2" has no show name in it; "Friends S03" does.
			if d = strings.TrimRight(d[:m[2]], " ._-sS"); len(d) < 2 || regexp.MustCompile(`(?i)^(season|series|saison|staffel|temporada|stagione)$`).MatchString(strings.TrimSpace(d)) {
				continue
			}
		}
		d = clean(d)
		for _, re := range episodeMarkers {
			if m := re.FindStringIndex(d); m != nil {
				d = d[:m[0]]
			}
		}
		if m := seasonInName.FindStringIndex(d); m != nil {
			d = d[:m[0]]
		}
		if t, y := titleAndYear(d, true); t != "" {
			return t, y
		}
		break
	}
	return "", 0
}

func parseMovie(name string, dirs []string) Info {
	// "grp-some.movie.2016.1080p.bluray": a lowercase group prefix.
	if name == strings.ToLower(name) && groupPrefix.MatchString(name) && (yearRe.MatchString(name) || hasStrong(name)) {
		name = groupPrefix.ReplaceAllString(name, "")
	}
	title, year := titleAndYear(name, false)
	if len(dirs) > 0 {
		dir := clean(dirs[len(dirs)-1])
		dt, dy := titleAndYear(dir, false)
		described := func(s string, y int) bool { return y != 0 || hasStrong(s) }
		switch {
		case dt == "":
		case title == "":
			title, year = dt, dy
		// A folder named "Title (1999)" or a full release name is a more
		// deliberate label than a file called "abc-xyz.avi" inside it.
		case described(dir, dy) && !described(name, year):
			title, year = dt, dy
		case year == 0 && dy != 0 && sameTitle(dt, title):
			year = dy
		// "Blade Runner 2049 (2017)/Blade Runner 2049.avi": the number in
		// the file name is part of the title, as the folder shows.
		case dy != 0 && year != dy && strings.HasPrefix(Fold(dt), Fold(title)):
			title, year = dt, dy
		}
	}
	if title == "" {
		title = strings.TrimSpace(name)
	}
	return Info{Kind: Movie, Title: title, Year: year}
}

// bare strips the brackets and the release-group suffix from a token, so
// "[x264-GRP]" is tested as "x264".
func bare(tok string) string {
	tok = strings.Trim(tok, "()[]{},;")
	if i := strings.Index(tok, "-"); i > 0 && !strongTag.MatchString(tok) && !weakTag.MatchString(tok) {
		if strongTag.MatchString(tok[:i]) {
			return tok[:i]
		}
	}
	return tok
}

func hasStrong(s string) bool {
	for _, t := range tokenRe.FindAllString(s, -1) {
		if strongTag.MatchString(bare(t)) {
			return true
		}
	}
	return false
}

// titleAndYear splits "Some.Movie.1999.1080p.BluRay" into ("Some Movie",
// 1999). The title ends at the release year or at the first technical word,
// whichever comes first. A year that is the first word belongs to the title
// ("2001 A Space Odyssey"), and so does a year followed by another year
// ("Blade Runner 2049 2017").
func titleAndYear(s string, show bool) (string, int) {
	_ = show
	re := tokenRe
	if n := strings.Count(strings.TrimSpace(s), " "); n > 0 && n >= strings.Count(s, ".") {
		re = spacedRe // words are separated by spaces: "Mr. Robot" keeps its dot
	}
	toks := re.FindAllString(s, -1)
	if len(toks) == 1 && strings.Count(toks[0], "-") >= 2 {
		toks = strings.FieldsFunc(toks[0], func(r rune) bool { return r == '-' }) // "Title-2014-French-1080p"
	}
	if len(toks) == 0 {
		return "", 0
	}
	isTag := func(i int) bool {
		b := bare(toks[i])
		return strongTag.MatchString(b) || weakTag.MatchString(b) || yearTok.MatchString(toks[i])
	}
	cut, year := len(toks), 0
	for i := len(toks) - 1; i >= 1; i-- {
		if m := yearTok.FindStringSubmatch(toks[i]); m != nil {
			year, cut = atoi(m[1]), i
			break
		}
	}
	for i := 1; i < cut; i++ {
		b := bare(toks[i])
		if strongTag.MatchString(b) {
			cut = i
			break
		}
		// An ambiguous word ("French", "Web", "DC") ends the title only
		// when what follows is also technical, so "The Italian Job" and
		// "Madame Web" survive.
		if weakTag.MatchString(b) && (i+1 == len(toks) || isTag(i+1)) {
			if i+1 == cut && cut < len(toks) && !editionTag.MatchString(b) {
				continue // the next word is the year: "Saw 3D 2010"
			}
			cut = i
			break
		}
	}
	return tidy(strings.Join(toks[:cut], " ")), year
}

// tidy removes bracketed asides and stray punctuation from a title.
func tidy(s string) string {
	if i := strings.IndexAny(s, "([{"); i > 0 {
		s = s[:i] + groupRe.ReplaceAllString(s[i:], "")
	}
	if i := strings.LastIndexAny(s, "([{"); i > 0 && !strings.ContainsAny(s[i:], ")]}") {
		s = s[:i]
	}
	s = spaces.ReplaceAllString(strings.ReplaceAll(s, "_", " "), " ")
	// "S H I E L D" came from "S.H.I.E.L.D": put the dots back.
	s = letters.ReplaceAllStringFunc(s, func(m string) string {
		lead, trail := "", ""
		if strings.HasPrefix(m, " ") {
			lead = " "
		}
		if strings.HasSuffix(m, " ") {
			trail = " "
		}
		return lead + strings.ReplaceAll(strings.TrimSpace(m), " ", ".") + "." + trail
	})
	return strings.Trim(s, " -,:;([{")
}

func sameTitle(a, b string) bool { return Fold(a) == Fold(b) }

// Fold reduces a title to lowercase letters and digits, for comparing two
// spellings of the same title.
func Fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
