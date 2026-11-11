package naming

import (
	"fmt"
	"strings"
	"testing"
)

// These are the ways people who look after a library actually name files:
// the folder layouts recommended by Plex and Jellyfin, the names Radarr and
// Sonarr write, and plain release names. Every title is run through every
// layout, so the right answer is known by construction.

var movieTitles = []struct {
	title string
	year  int
}{
	{"The Matrix", 1999}, {"2001: A Space Odyssey", 1968}, {"Blade Runner 2049", 2017}, {"1917", 2019},
	{"Se7en", 1995}, {"WALL·E", 2008}, {"Amélie", 2001}, {"The Lord of the Rings: The Return of the King", 2003},
	{"Spider-Man: No Way Home", 2021}, {"300", 2006}, {"10 Things I Hate About You", 1999}, {"Mr. & Mrs. Smith", 2005},
	{"9 to 5", 1980}, {"Fast & Furious 6", 2013}, {"Ocean's Eleven", 2001}, {"Dr. Strangelove", 1964},
	{"Monsters, Inc.", 2001}, {"The Italian Job", 1969}, {"Madame Web", 2024}, {"Saw 3D", 2010},
	{"Wonder Woman 1984", 2020}, {"The Girl in the Spider's Web", 2018}, {"How to Be Single", 2016}, {"Ghost in the Shell", 1995},
	{"A Complete Unknown", 2024}, {"The Dark Knight", 2008}, {"Interstellar", 2014}, {"Up", 2009},
	{"Her", 2013}, {"It", 2017}, {"Us", 2019}, {"Nope", 2022},
	{"E.T. the Extra-Terrestrial", 1982}, {"Léon: The Professional", 1994}, {"Crouching Tiger, Hidden Dragon", 2000}, {"Y Tu Mamá También", 2001},
	{"The Good, the Bad and the Ugly", 1966}, {"12 Angry Men", 1957}, {"District 9", 2009}, {"Apollo 13", 1995},
	{"The Hateful Eight", 2015}, {"Kill Bill: Vol. 1", 2003}, {"Star Wars: Episode IV - A New Hope", 1977}, {"Mission: Impossible - Dead Reckoning Part One", 2023},
	{"The French Dispatch", 2021}, {"The Spanish Prisoner", 1997}, {"An American Werewolf in London", 1981}, {"The Real Thing", 2019},
	{"Limited Partners", 2011}, {"The Proper Way", 2015}, {"Web of Lies", 2018}, {"Opus", 2025},
}

var showTitles = []struct {
	title string
	year  int
}{
	{"Breaking Bad", 2008}, {"The Office", 2005}, {"Mr. Robot", 2015}, {"The 100", 2014},
	{"24", 2001}, {"9-1-1", 2018}, {"House of the Dragon", 2022}, {"Doctor Who", 2005},
	{"S.W.A.T.", 2017}, {"Marvel's Agents of S.H.I.E.L.D.", 2013}, {"Brooklyn Nine-Nine", 2013}, {"Stranger Things", 2016},
	{"1883", 2021}, {"Battlestar Galactica", 2004}, {"Game of Thrones", 2011}, {"The Simpsons", 1989},
	{"Friends", 1994}, {"Better Call Saul", 2015}, {"The Mandalorian", 2019}, {"Star Trek: The Next Generation", 1987},
	{"Law & Order: Special Victims Unit", 1999}, {"It's Always Sunny in Philadelphia", 2005}, {"The X-Files", 1993}, {"Twin Peaks", 1990},
	{"Fargo", 2014}, {"Severance", 2022}, {"The Bear", 2022}, {"Bluey", 2018},
	{"Attack on Titan", 2013}, {"One Piece", 1999}, {"Planet Earth II", 2016}, {"Cosmos: A Spacetime Odyssey", 2014},
}

// fileSafe is what a title becomes once the characters Windows forbids are
// gone, which is how it appears in a real folder.
func fileSafe(s string) string {
	s = strings.NewReplacer(": ", " - ", ":", "", "?", "", "*", "", "\"", "", "/", " ", "\\", " ", "|", "", "<", "", ">", "").Replace(s)
	return strings.TrimRight(strings.Join(strings.Fields(s), " "), ". ")
}

// dotted is the release-name spelling: "Spider-Man.No.Way.Home".
func dotted(s string) string {
	s = strings.NewReplacer(":", "", ",", "", "'", "", "&", "and", "·", ".", "?", "", "!", "").Replace(s)
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, " - ", " ")), ".")
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	return strings.Trim(s, ".")
}

// same compares titles the way a lookup would: letters and digits only, and
// "and" for "&".
func same(got, want string) bool {
	norm := func(s string) string {
		return strings.ReplaceAll(Fold(strings.ReplaceAll(s, "&", "and")), "and", "")
	}
	return norm(got) == norm(want)
}

func TestConventionalNames(t *testing.T) {
	total, wrong := 0, 0
	fail := func(path, why string) {
		wrong++
		if wrong <= 40 {
			t.Logf("%s\n      %s", path, why)
		}
	}
	for _, m := range movieTitles {
		T, D, Y := fileSafe(m.title), dotted(m.title), m.year
		paths := []string{
			fmt.Sprintf("Movies/%s (%d)/%s (%d).mkv", T, Y, T, Y),
			fmt.Sprintf("%s (%d).mp4", T, Y),
			fmt.Sprintf("Movies/%s (%d)/%s (%d) - 2160p.mkv", T, Y, T, Y),
			fmt.Sprintf("Movies/%s (%d) {tmdb-603}/%s (%d) {tmdb-603}.mkv", T, Y, T, Y),
			fmt.Sprintf("Movies/%s (%d)/%s (%d) [Bluray-1080p][DTS 5.1][x264]-GRP.mkv", T, Y, T, Y),
			fmt.Sprintf("%s.%d.1080p.BluRay.x264-GRP.mkv", D, Y),
			fmt.Sprintf("Films/%s.%d.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1.Atmos-GRP/%s.%d.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1.Atmos-GRP.mkv", D, Y, D, Y),
			fmt.Sprintf("%s.%d.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv", D, Y),
			fmt.Sprintf("%s %d 720p WEBRip x265 AAC.mp4", fileSafe(strings.ReplaceAll(m.title, ":", "")), Y),
			fmt.Sprintf("Movies/%s (%d)/%s.avi", T, Y, T),
		}
		for _, p := range paths {
			total++
			got := Parse(p)
			switch {
			case got.Kind != Movie:
				fail(p, fmt.Sprintf("recognised as %s S%dE%d, want a film", got.Kind, got.Season, got.Episode))
			case !same(got.Title, m.title):
				fail(p, fmt.Sprintf("title %q, want %q", got.Title, m.title))
			case got.Year != Y:
				fail(p, fmt.Sprintf("year %d, want %d", got.Year, Y))
			}
		}
	}
	for i, s := range showTitles {
		T, D, Y := fileSafe(s.title), dotted(s.title), s.year
		n, e := 1+i%9, 1+(i*7)%23
		paths := []string{
			fmt.Sprintf("TV/%s/Season %02d/%s - S%02dE%02d - The Long Night.mkv", T, n, T, n, e),
			fmt.Sprintf("TV/%s/Season %d/%s S%02dE%02d.mkv", T, n, T, n, e),
			fmt.Sprintf("TV/%s/Season %02d/S%02dE%02d.mkv", T, n, n, e),
			fmt.Sprintf("TV/%s (%d)/Season %02d/%s (%d) - S%02dE%02d - Pilot [WEBDL-1080p][EAC3 5.1][h264]-GRP.mkv", T, Y, n, T, Y, n, e),
			fmt.Sprintf("TV/%s/Season %02d/%s.S%02dE%02d.The.Long.Night.1080p.WEB.h264-GRP.mkv", T, n, D, n, e),
			fmt.Sprintf("%s.S%02dE%02d.720p.HDTV.x264-GRP.mkv", D, n, e),
			fmt.Sprintf("%s.S%02dE%02d.Part.2.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-GRP.mkv", D, n, e),
			fmt.Sprintf("TV/%s/Season %02d/%s - %dx%02d - Pilot.mkv", T, n, T, n, e),
			fmt.Sprintf("TV/%s/Season %02d/%s - s%02de%02d.mp4", T, n, strings.ToLower(T), n, e),
			fmt.Sprintf("TV/%s/Specials/%s - S00E%02d - Behind the Scenes.mkv", T, T, e),
			fmt.Sprintf("TV/%s/Season %02d/%s - S%02dE%02d-E%02d - Double Bill.mkv", T, n, T, n, e, e+1),
		}
		for j, p := range paths {
			total++
			wantSeason := n
			if j == 9 {
				wantSeason = 0
			}
			got := Parse(p)
			switch {
			case got.Kind != Episode:
				fail(p, fmt.Sprintf("recognised as a film %q, want an episode", got.Title))
			case !same(got.Title, s.title):
				fail(p, fmt.Sprintf("show %q, want %q", got.Title, s.title))
			case got.Season != wantSeason || got.Episode != e:
				fail(p, fmt.Sprintf("S%dE%d, want S%dE%d", got.Season, got.Episode, wantSeason, e))
			case j == 10 && got.EpisodeEnd != e+1:
				fail(p, fmt.Sprintf("double episode ends at %d, want %d", got.EpisodeEnd, e+1))
			}
		}
	}
	acc := float64(total-wrong) / float64(total)
	t.Logf("conventional names: %d of %d right (%.1f%%)", total-wrong, total, acc*100)
	if acc < 0.97 {
		t.Fatalf("accuracy %.3f on conventional names is below the plan's 0.97", acc)
	}
}
