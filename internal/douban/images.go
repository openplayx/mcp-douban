package douban

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ImageHostAllowed reports whether u is an https URL on doubanio.com, the
// only host the image proxy fetches from.
func ImageHostAllowed(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && u.Port() == "" && u.User == nil &&
		(h == "doubanio.com" || strings.HasSuffix(h, ".doubanio.com"))
}

// ImageClient fetches doubanio.com images with a Douban Referer. Redirects
// are followed only within doubanio.com.
func ImageClient(base *http.Client) *http.Client {
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !ImageHostAllowed(req.URL) {
			return errors.New("redirect not allowed")
		}
		req.Header.Set("Referer", Referer)
		return nil
	}
	return &c
}

// FetchImage starts a GET for an allowed image URL. The caller closes the
// body. Non-200 answers and non-image content types are errors.
func FetchImage(ctx context.Context, hc *http.Client, raw string) (*http.Response, error) {
	u, err := url.Parse(raw)
	if err != nil || !ImageHostAllowed(u) {
		return nil, invalid("url", map[string]any{"allowed_hosts": "*.doubanio.com (https)"})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, invalid("url", nil)
	}
	req.Header.Set("Referer", Referer)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamUnavailable, Message: "image host is not reachable", Retryable: true}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: "no such image"}
		}
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamRejected, Message: "image host refused the request",
			Detail: map[string]any{"upstream_status": resp.StatusCode}}
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		resp.Body.Close()
		return nil, &Error{Status: http.StatusBadGateway, Code: CodeUpstreamChanged, Message: "image host did not return an image"}
	}
	return resp, nil
}
