package metadata

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Matiaspnyhus/Local-media-project/internal/naming"
)

// Pick chooses the search hit that is the same film or show as the file,
// or reports that none is. It prefers to find nothing over finding the
// wrong thing: a film with the wrong poster is worse than one without.
//
// A hit must have the same title (ignoring case, accents and punctuation).
// With a year from the file, the hit's year must be within one of it, or
// the hit must be the only one with that title. Without a year, the first
// such hit wins; TMDB lists the best-known first.
func Pick(title string, year int, hits []Result) (Result, bool) {
	want := key(title)
	var same []Result
	for _, h := range hits {
		if key(h.Title) == want || key(h.OriginalTitle) == want {
			same = append(same, h)
		}
	}
	if len(same) == 0 {
		return Result{}, false
	}
	if year == 0 {
		return same[0], true
	}
	for _, h := range same {
		if h.Year >= year-1 && h.Year <= year+1 {
			return h, true
		}
	}
	if len(same) == 1 && same[0].Year == 0 {
		return same[0], true
	}
	return Result{}, false
}

// key folds a title for comparison: "Amélie" and "amelie", "Fast & Furious"
// and "Fast and Furious" are the same.
func key(s string) string {
	s = strings.NewReplacer("&", " and ").Replace(strings.ToLower(s))
	s = strings.Map(unaccent, s)
	return strings.ReplaceAll(naming.Fold(s), "and", "")
}

func unaccent(r rune) rune {
	const from = "àáâãäåçèéêëìíîïñòóôõöøùúûüýÿ"
	const to = "aaaaaaceeeeiiiinoooooouuuuyy"
	if i := strings.IndexRune(from, r); i >= 0 {
		return []rune(to)[len([]rune(from[:i]))]
	}
	return r
}

var hint = regexp.MustCompile(`(?i)[\[{(]tmdb(?:id)?[-= ](\d+)[\]})]`)

// HintID reads a "{tmdb-603}" tag from a path, the convention for telling
// a library exactly which film or show a folder is.
func HintID(rel string) int {
	if m := hint.FindStringSubmatch(rel); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}
