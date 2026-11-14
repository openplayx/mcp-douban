// Package douban reads public movie/TV data from the JSON endpoints that
// Douban's mobile web site (m.douban.com) uses itself ("rexxar" API).
//
// The endpoints need no login or signature; they only require a Referer on
// a douban.com page, and refuse requests without one (HTTP 400). Poster
// images on *.doubanio.com have the same hotlink rule (HTTP 418 without a
// Douban Referer), so the API layer proxies them.
//
// These are undocumented endpoints. Upstream 4xx answers are reported as
// upstream_rejected and unparseable bodies as upstream_changed, so a change
// on Douban's side shows up as a clear error rather than empty data.
package douban

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
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://m.douban.com/rexxar/api/v2"
	Referer        = "https://movie.douban.com/"
	UserAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

	maxBody = 4 << 20
)

// Cache lifetimes per kind of data.
var (
	ChartTTL   = 30 * time.Minute
	SearchTTL  = time.Hour
	SubjectTTL = 6 * time.Hour
)

// Options configure a Client. Zero values select the defaults.
type Options struct {
	BaseURL      string        // DefaultBaseURL
	HTTPClient   *http.Client  // 15 s timeout
	MinInterval  time.Duration // minimum spacing between upstream requests; 0 = 200 ms, <0 = none
	CacheEntries int           // 0 = 2000, <0 = no cache
	// ImageURL turns a doubanio.com image URL into one the caller can load
	// (e.g. this service's image proxy). nil leaves the proxied field empty.
	ImageURL func(string) string
}

// Client calls Douban with caching and request spacing.
type Client struct {
	base     string
	http     *http.Client
	cache    *cache
	imageURL func(string) string

	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

func New(o Options) *Client {
	c := &Client{base: o.BaseURL, http: o.HTTPClient, interval: o.MinInterval, imageURL: o.ImageURL}
	if c.base == "" {
		c.base = DefaultBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 15 * time.Second}
	}
	if c.interval == 0 {
		c.interval = 200 * time.Millisecond
	}
	n := o.CacheEntries
	if n == 0 {
		n = 2000
	}
	c.cache = newCache(max(n, 0))
	return c
}

// wait spaces upstream requests so bursts from many callers do not look
// like a crawler to Douban.
func (c *Client) wait(ctx context.Context) error {
	if c.interval <= 0 {
		return nil
	}
	c.mu.Lock()
	now := time.Now()
	at := c.next
	if at.Before(now) {
		at = now
	}
	c.next = at.Add(c.interval)
	c.mu.Unlock()
	if d := at.Sub(now); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

// get fetches base+path?query, through the cache, and decodes it into v.
func (c *Client) get(ctx context.Context, path string, q url.Values, ttl time.Duration, v any) (bool, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, cached, err := c.cache.do(u, ttl, func() ([]byte, error) {
		// Detached from the first caller's cancellation: others may share
		// this call. The HTTP client timeout still bounds it.
		return c.fetch(context.WithoutCancel(ctx), u)
	})
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return false, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamChanged, Message: "unexpected response from Douban"}
	}
	return cached, nil
}

func (c *Client) fetch(ctx context.Context, u string) ([]byte, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", Referer)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamUnavailable, Message: "Douban is not reachable", Retryable: true}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamUnavailable, Message: "reading Douban response failed", Retryable: true}
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		if !json.Valid(body) {
			return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamChanged, Message: "Douban did not answer with JSON"}
		}
		return body, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: "no such subject on Douban"}
	case resp.StatusCode >= 500:
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamUnavailable, Message: fmt.Sprintf("Douban answered HTTP %d", resp.StatusCode), Retryable: true}
	default:
		var e struct {
			Msg  string `json:"msg"`
			Code int    `json:"code"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, &Error{
			Status: http.StatusBadGateway, Code: CodeUpstreamRejected, Retryable: resp.StatusCode == http.StatusTooManyRequests,
			Message: fmt.Sprintf("Douban refused the request (HTTP %d)", resp.StatusCode),
			Detail:  map[string]any{"upstream_status": resp.StatusCode, "upstream_msg": e.Msg, "upstream_code": e.Code},
		}
	}
}

// ---------------------------------------------------------------- charts

// ChartResult is one page of a chart.
type ChartResult struct {
	Chart  string `json:"chart"`
	Label  string `json:"label"`
	Filter string `json:"filter"`
	Total  int    `json:"total"`
	Start  int    `json:"start"`
	Items  []Item `json:"items"`
	Cached bool   `json:"cached"`
}

// Chart returns items start..start+limit of a chart.
func (c *Client) Chart(ctx context.Context, chartID, filter string, start, limit int) (*ChartResult, error) {
	ch, f, err := FindChart(chartID, filter)
	if err != nil {
		return nil, err
	}
	if start < 0 || start > 1000 {
		return nil, invalid("start", map[string]any{"min": 0, "max": 1000})
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("limit", map[string]any{"min": 1, "max": 100})
	}
	q := url.Values{"category": {ch.category}, "type": {f.upstream}, "start": {strconv.Itoa(start)}, "limit": {strconv.Itoa(limit)}}
	var raw struct {
		Total *int      `json:"total"`
		Items []rawItem `json:"items"`
	}
	cached, err := c.get(ctx, "/subject/recent_hot/"+ch.path, q, ChartTTL, &raw)
	if err != nil {
		return nil, err
	}
	if raw.Total == nil || raw.Items == nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamChanged, Message: "chart response has no items"}
	}
	res := &ChartResult{Chart: ch.ID, Label: ch.Label, Filter: f.Key, Total: *raw.Total, Start: start, Items: []Item{}, Cached: cached}
	for _, it := range raw.Items {
		if it.ID == "" {
			continue
		}
		res.Items = append(res.Items, c.item(it.ID, it.Type, it.Title, yearOf(it.CardSubtitle), it.CardSubtitle, it.Rating, it.Pic.Large, it.EpisodesInfo, it.IsNew))
	}
	return res, nil
}

// ---------------------------------------------------------------- search

// SearchResult is one page of search hits (movies and TV only).
type SearchResult struct {
	Query  string `json:"query"`
	Total  int    `json:"total"`
	Start  int    `json:"start"`
	Items  []Item `json:"items"`
	Cached bool   `json:"cached"`
}

func (c *Client) Search(ctx context.Context, query string, start, count int) (*SearchResult, error) {
	if query == "" || len([]rune(query)) > 100 {
		return nil, invalid("query", map[string]any{"min_length": 1, "max_length": 100})
	}
	if start < 0 || start > 200 {
		return nil, invalid("start", map[string]any{"min": 0, "max": 200})
	}
	if count < 1 || count > 20 {
		return nil, invalid("count", map[string]any{"min": 1, "max": 20})
	}
	q := url.Values{"q": {query}, "start": {strconv.Itoa(start)}, "count": {strconv.Itoa(count)}}
	var raw struct {
		Total *int `json:"total"`
		Items []struct {
			Layout     string  `json:"layout"`
			TargetType string  `json:"target_type"`
			Target     rawItem `json:"target"`
		} `json:"items"`
	}
	cached, err := c.get(ctx, "/search/movie", q, SearchTTL, &raw)
	if err != nil {
		return nil, err
	}
	if raw.Total == nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamChanged, Message: "search response has no total"}
	}
	res := &SearchResult{Query: query, Total: *raw.Total, Start: start, Items: []Item{}, Cached: cached}
	for _, h := range raw.Items {
		t := h.Target
		if h.Layout != "subject" || t.ID == "" || (h.TargetType != "movie" && h.TargetType != "tv") {
			continue
		}
		res.Items = append(res.Items, c.item(t.ID, h.TargetType, t.Title, t.Year, t.CardSubtitle, t.Rating, t.CoverURL, "", false))
	}
	return res, nil
}

// ---------------------------------------------------------------- subjects

var idPattern = regexp.MustCompile(`^[0-9]{1,12}$`)

// CheckID validates a Douban subject id.
func CheckID(id string) error {
	if !idPattern.MatchString(id) {
		return invalid("id", map[string]any{"pattern": idPattern.String()})
	}
	return nil
}

// Subject returns details of a movie or TV subject. Douban serves both
// under /movie/{id}.
func (c *Client) Subject(ctx context.Context, id string) (*Subject, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	var raw rawSubject
	cached, err := c.get(ctx, "/movie/"+id, nil, SubjectTTL, &raw)
	if err != nil {
		return nil, err
	}
	if raw.ID == "" || raw.Title == "" {
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamChanged, Message: "subject response has no id/title"}
	}
	s := c.subject(&raw)
	s.Cached = cached
	return s, nil
}

// Credits returns the full cast and crew of a subject.
func (c *Client) Credits(ctx context.Context, id string) (*Credits, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	var raw struct {
		Directors []rawPerson `json:"directors"`
		Actors    []rawPerson `json:"actors"`
		Total     *int        `json:"total"`
	}
	cached, err := c.get(ctx, "/movie/"+id+"/celebrities", nil, SubjectTTL, &raw)
	if err != nil {
		return nil, err
	}
	if raw.Total == nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamChanged, Message: "credits response has no total"}
	}
	res := &Credits{ID: id, Directors: c.people(raw.Directors), Actors: c.people(raw.Actors), Cached: cached}
	return res, nil
}

// CacheLen reports the number of cached upstream responses.
func (c *Client) CacheLen() int { return c.cache.len() }

// IsCode reports whether err is a *Error with the given code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
