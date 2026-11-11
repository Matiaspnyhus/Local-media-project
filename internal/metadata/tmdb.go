// Package metadata looks films and shows up at TMDB (themoviedb.org) for
// their proper titles, descriptions and posters. It is optional: without a
// key, or with lookups switched off, nothing here runs. Only titles and
// years are sent.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrBadKey means TMDB refused the key.
var ErrBadKey = errors.New("TMDB did not accept that key")

// Client talks to TMDB and to nothing else: every address it fetches is
// built from the two fixed bases below, and redirects are refused.
type Client struct {
	key       string
	apiBase   string
	imageBase string
	http      *http.Client
	mu        sync.Mutex
	last      time.Time
	gap       time.Duration
}

// New returns a client for the given key. Either of TMDB's two credentials
// works: the short "API Key" or the long "API Read Access Token".
func New(key string) *Client {
	return &Client{
		key: strings.TrimSpace(key), apiBase: "https://api.themoviedb.org/3", imageBase: "https://image.tmdb.org/t/p",
		gap: 100 * time.Millisecond, // at most 10 requests a second
		http: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects are not followed")
			},
		},
	}
}

// WithBases points the client at other servers; tests use it.
func (c *Client) WithBases(api, image string) *Client {
	c.apiBase, c.imageBase, c.gap = api, image, 0
	return c
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	next := c.last.Add(c.gap)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	c.last = next
	c.mu.Unlock()
	select {
	case <-time.After(time.Until(next)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if c.key == "" {
		return ErrBadKey
	}
	if err := c.wait(ctx); err != nil {
		return err
	}
	if q == nil {
		q = url.Values{}
	}
	long := strings.HasPrefix(c.key, "eyJ")
	if !long {
		q.Set("api_key", c.key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if long {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		// Never let the address, which may carry the key, into an error.
		return errors.New("TMDB could not be reached")
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return ErrBadKey
	case res.StatusCode == http.StatusNotFound:
		return errNotFound
	case res.StatusCode == http.StatusTooManyRequests:
		return errors.New("TMDB asked us to slow down")
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("TMDB answered %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out)
}

var errNotFound = errors.New("not found at TMDB")

// Check asks TMDB whether the key is good.
func (c *Client) Check(ctx context.Context) error {
	var out struct {
		Success bool `json:"success"`
	}
	if err := c.get(ctx, "/authentication", nil, &out); err != nil {
		return err
	}
	if !out.Success {
		return ErrBadKey
	}
	return nil
}

// Result is one search hit, film or show.
type Result struct {
	ID            int
	Title         string
	OriginalTitle string
	Year          int
	Overview      string
	PosterPath    string
}

type rawResult struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	Name          string `json:"name"`
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	Overview      string `json:"overview"`
	PosterPath    string `json:"poster_path"`
}

func (r rawResult) result() Result {
	out := Result{ID: r.ID, Title: r.Title + r.Name, OriginalTitle: r.OriginalTitle + r.OriginalName, Overview: r.Overview, PosterPath: r.PosterPath}
	if date := r.ReleaseDate + r.FirstAirDate; len(date) >= 4 {
		out.Year, _ = strconv.Atoi(date[:4])
	}
	return out
}

func (c *Client) search(ctx context.Context, kind, title string, year int) ([]Result, error) {
	q := url.Values{"query": {title}, "include_adult": {"false"}}
	if year > 0 {
		if kind == "movie" {
			q.Set("year", strconv.Itoa(year))
		} else {
			q.Set("first_air_date_year", strconv.Itoa(year))
		}
	}
	var out struct {
		Results []rawResult `json:"results"`
	}
	if err := c.get(ctx, "/search/"+kind, q, &out); err != nil {
		return nil, err
	}
	res := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		res = append(res, r.result())
	}
	return res, nil
}

// SearchMovie searches films by title and, when known, year.
func (c *Client) SearchMovie(ctx context.Context, title string, year int) ([]Result, error) {
	return c.search(ctx, "movie", title, year)
}

// SearchTV searches shows by title and, when known, first year.
func (c *Client) SearchTV(ctx context.Context, title string, year int) ([]Result, error) {
	return c.search(ctx, "tv", title, year)
}

// ByID fetches one film ("movie") or show ("tv") by its TMDB number.
func (c *Client) ByID(ctx context.Context, kind string, id int) (Result, error) {
	var raw rawResult
	if err := c.get(ctx, fmt.Sprintf("/%s/%d", kind, id), nil, &raw); err != nil {
		return Result{}, err
	}
	return raw.result(), nil
}

// EpisodeInfo is one episode's name and description.
type EpisodeInfo struct {
	Number   int    `json:"episode_number"`
	Name     string `json:"name"`
	Overview string `json:"overview"`
}

// Season fetches the episodes of one season of a show.
func (c *Client) Season(ctx context.Context, showID, season int) ([]EpisodeInfo, error) {
	var out struct {
		Episodes []EpisodeInfo `json:"episodes"`
	}
	err := c.get(ctx, fmt.Sprintf("/tv/%d/season/%d", showID, season), nil, &out)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return out.Episodes, err
}

var posterPath = regexp.MustCompile(`^/[A-Za-z0-9_-]+\.(jpg|jpeg|png)$`)

// Poster downloads a poster. The path comes from TMDB's own answer and is
// still checked, so it cannot be used to fetch anything else.
func (c *Client) Poster(ctx context.Context, path string) ([]byte, error) {
	if !posterPath.MatchString(path) {
		return nil, errors.New("not a poster path")
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.imageBase+"/w342"+path, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("the poster could not be fetched")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "image/") {
		return nil, errors.New("the poster could not be fetched")
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 5<<20+1))
	if err != nil || len(b) > 5<<20 {
		return nil, errors.New("the poster is too large")
	}
	return b, nil
}
