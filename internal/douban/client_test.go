package douban

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDouban serves recorded responses (testdata/) and, like the real
// endpoint, refuses requests without a douban.com Referer.
func fakeDouban(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	fixture := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	mux := http.NewServeMux()
	send := func(w http.ResponseWriter, status int, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	mux.HandleFunc("/subject/recent_hot/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category") != "热门" || r.URL.Query().Get("type") != "全部" {
			send(w, 200, []byte(`{"total":0,"items":[]}`))
			return
		}
		send(w, 200, fixture("recent_hot_movie"))
	})
	mux.HandleFunc("/subject/recent_hot/tv", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category") != "show" || r.URL.Query().Get("type") != "show_foreign" {
			t.Errorf("unexpected tv query %v", r.URL.Query())
		}
		send(w, 200, fixture("recent_hot_show"))
	})
	mux.HandleFunc("/search/movie", func(w http.ResponseWriter, r *http.Request) { send(w, 200, fixture("search_movie")) })
	mux.HandleFunc("/movie/1292052", func(w http.ResponseWriter, r *http.Request) { send(w, 200, fixture("movie_detail")) })
	mux.HandleFunc("/movie/37806882", func(w http.ResponseWriter, r *http.Request) { send(w, 200, fixture("tv_detail")) })
	mux.HandleFunc("/movie/1292052/celebrities", func(w http.ResponseWriter, r *http.Request) { send(w, 200, fixture("celebrities")) })
	mux.HandleFunc("/movie/1", func(w http.ResponseWriter, r *http.Request) { send(w, 200, []byte("<html>captcha</html>")) })
	mux.HandleFunc("/movie/2", func(w http.ResponseWriter, r *http.Request) {
		send(w, 403, []byte(`{"msg":"need_login","code":103}`))
	})
	mux.HandleFunc("/movie/3", func(w http.ResponseWriter, r *http.Request) { send(w, 502, nil) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { send(w, 404, fixture("notfound")) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Referer") != Referer {
			send(w, 400, []byte(`{"msg":"invalid_request","code":400}`))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func testClient(t *testing.T) (*Client, *atomic.Int64) {
	srv, hits := fakeDouban(t)
	return New(Options{BaseURL: srv.URL, MinInterval: -1, ImageURL: func(u string) string { return "/img?u=" + url.QueryEscape(u) }}), hits
}

func code(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func TestChart(t *testing.T) {
	c, _ := testClient(t)
	res, err := c.Chart(context.Background(), "movie_hot", "", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filter != "all" || res.Total != 315 || len(res.Items) != 3 {
		t.Fatalf("got %+v", res)
	}
	it := res.Items[0]
	if it.ID != "36847425" || it.Title != "可能的爱" || it.Type != "movie" || it.Year != "2026" ||
		it.Rating == nil || it.Rating.Value != 8.7 || it.Rating.Count != 51053 ||
		it.URL != "https://movie.douban.com/subject/36847425/" || it.PosterProxy == "" || it.Poster == "" {
		t.Fatalf("item %+v", it)
	}

	res, err = c.Chart(context.Background(), "variety", "国外", 0, 3)
	if err != nil || res.Filter != "foreign" || len(res.Items) == 0 || res.Items[0].Type != "tv" {
		t.Fatalf("variety: %+v %v", res, err)
	}
}

func TestChartValidation(t *testing.T) {
	c, hits := testClient(t)
	for _, tc := range []struct {
		chart, filter string
		start, limit  int
	}{
		{"nope", "", 0, 10}, {"tv", "bogus", 0, 10}, {"tv", "", -1, 10}, {"tv", "", 0, 0}, {"tv", "", 0, 101},
	} {
		if _, err := c.Chart(context.Background(), tc.chart, tc.filter, tc.start, tc.limit); code(err) != CodeInvalidRequest {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("invalid requests reached upstream")
	}
}

func TestFindChartAliases(t *testing.T) {
	for _, f := range []string{"korean", "KOREAN", "韩国"} {
		if _, got, err := FindChart("movie_top", f); err != nil || got.upstream != "韩国" {
			t.Errorf("%q: %v %v", f, got, err)
		}
	}
	if _, got, _ := FindChart("tv", "动画"); got.upstream != "tv_animation" {
		t.Errorf("tv 动画 -> %v", got)
	}
}

func TestSearch(t *testing.T) {
	c, _ := testClient(t)
	res, err := c.Search(context.Background(), "肖申克", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 7 || len(res.Items) == 0 {
		t.Fatalf("got %+v", res)
	}
	it := res.Items[0]
	if it.ID != "1292052" || it.Title != "肖申克的救赎" || it.Year != "1994" || it.Type != "movie" || it.Rating.Value != 9.7 {
		t.Fatalf("item %+v", it)
	}
	if _, err := c.Search(context.Background(), "", 0, 5); code(err) != CodeInvalidRequest {
		t.Fatal("empty query accepted")
	}
}

func TestSubjectMovieAndTV(t *testing.T) {
	c, _ := testClient(t)
	m, err := c.Subject(context.Background(), "1292052")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != "movie" || m.Title != "肖申克的救赎" || m.OriginalTitle != "The Shawshank Redemption" || m.Year != "1994" ||
		len(m.Directors) != 1 || len(m.Actors) == 0 || len(m.Genres) != 2 || m.Rating.Value != 9.7 || m.Intro == "" ||
		len(m.Honors) == 0 || m.Honors[0].Rank != 1 || m.PosterProxy == "" {
		t.Fatalf("movie %+v", m)
	}
	tv, err := c.Subject(context.Background(), "37806882")
	if err != nil {
		t.Fatal(err)
	}
	if tv.Type != "tv" || tv.EpisodesCount != 12 || tv.OriginalTitle != "나의 유죄인간" {
		t.Fatalf("tv %+v", tv)
	}
}

func TestCredits(t *testing.T) {
	c, _ := testClient(t)
	cr, err := c.Credits(context.Background(), "1292052")
	if err != nil {
		t.Fatal(err)
	}
	if len(cr.Directors) != 1 || len(cr.Actors) != 47 {
		t.Fatalf("got %d directors %d actors", len(cr.Directors), len(cr.Actors))
	}
	a := cr.Actors[0]
	if a.Name != "蒂姆·罗宾斯" || a.LatinName != "Tim Robbins" || a.Character == "" || a.AvatarProxy == "" {
		t.Fatalf("actor %+v", a)
	}
}

func TestUpstreamErrors(t *testing.T) {
	c, _ := testClient(t)
	for id, want := range map[string]string{
		"99999":  CodeNotFound,
		"1":      CodeUpstreamChanged,
		"2":      CodeUpstreamRejected,
		"3":      CodeUpstreamUnavailable,
		"abc":    CodeInvalidRequest,
		"../etc": CodeInvalidRequest,
	} {
		if _, err := c.Subject(context.Background(), id); code(err) != want {
			t.Errorf("%s: got %v, want %s", id, err, want)
		}
	}
	// Douban's rejection detail is passed through for diagnosis.
	_, err := c.Subject(context.Background(), "2")
	if e := err.(*Error); e.Detail["upstream_msg"] != "need_login" || e.Status != http.StatusBadGateway {
		t.Fatalf("detail %+v", e)
	}
	// A change in Douban's chart shape is an error, not an empty list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"subjects":[]}`)) }))
	defer srv.Close()
	c2 := New(Options{BaseURL: srv.URL, MinInterval: -1})
	if _, err := c2.Chart(context.Background(), "tv", "", 0, 5); code(err) != CodeUpstreamChanged {
		t.Fatalf("shape change: %v", err)
	}
}

func TestRefererIsSent(t *testing.T) {
	// The fake answers 400 without the Referer, so any success proves it.
	c, _ := testClient(t)
	if _, err := c.Subject(context.Background(), "1292052"); err != nil {
		t.Fatal(err)
	}
}

func TestCacheAndCoalescing(t *testing.T) {
	c, hits := testClient(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := c.Subject(context.Background(), "1292052"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Fatalf("20 concurrent calls made %d upstream requests", n)
	}
	s, _ := c.Subject(context.Background(), "1292052")
	if !s.Cached {
		t.Fatal("second call not served from cache")
	}
	// Failures are not cached.
	c.Subject(context.Background(), "3")
	c.Subject(context.Background(), "3")
	if n := hits.Load(); n != 3 {
		t.Fatalf("errors cached: %d upstream requests", n)
	}
}

func TestCacheExpiryAndCap(t *testing.T) {
	cc := newCache(2)
	now := time.Unix(0, 0)
	cc.now = func() time.Time { return now }
	calls := 0
	fetch := func() ([]byte, error) { calls++; return []byte("x"), nil }
	cc.do("a", time.Minute, fetch)
	cc.do("a", time.Minute, fetch)
	now = now.Add(2 * time.Minute)
	cc.do("a", time.Minute, fetch)
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	cc.do("b", time.Minute, fetch)
	cc.do("c", time.Minute, fetch)
	if cc.len() > 2 {
		t.Fatalf("cache grew to %d", cc.len())
	}
}

func TestRequestSpacing(t *testing.T) {
	srv, _ := fakeDouban(t)
	c := New(Options{BaseURL: srv.URL, MinInterval: 50 * time.Millisecond, CacheEntries: -1})
	start := time.Now()
	for range 3 {
		c.Subject(context.Background(), "1292052")
	}
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatalf("3 requests took %v, want >= 100ms", d)
	}
}

func TestImageHostAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://img3.doubanio.com/view/photo/p1.jpg":         true,
		"https://qnmob3-sign.doubanio.com/view/photo/p1.jpg":  true,
		"http://img3.doubanio.com/p.jpg":                      false,
		"https://img3.doubanio.com:8443/p.jpg":                false,
		"https://evil.com/?x=.doubanio.com":                   false,
		"https://doubanio.com.evil.com/p.jpg":                 false,
		"https://user@img3.doubanio.com/p.jpg":                false,
		"https://127.0.0.1/p.jpg":                             false,
		"https://img3.DOUBANIO.com/view/photo/p1.jpg":         true,
		"https://img3.doubanio.com.evil.com/view/photo/p.jpg": false,
	} {
		u, _ := url.Parse(raw)
		if got := ImageHostAllowed(u); got != want {
			t.Errorf("%s: got %v", raw, got)
		}
	}
}
