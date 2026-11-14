package douban

import "strings"

// Chart is one ranking list backed by Douban's subject/recent_hot endpoint.
type Chart struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // movie | tv
	Filters []Filter `json:"filters"`

	path     string // movie | tv
	category string
}

// Filter narrows a chart (region for movies, genre/region for TV). The first
// filter of each chart is its default.
type Filter struct {
	Key   string `json:"key"`
	Label string `json:"label"`

	upstream string
}

var movieRegions = []Filter{
	{"all", "全部", "全部"},
	{"chinese", "华语", "华语"},
	{"western", "欧美", "欧美"},
	{"korean", "韩国", "韩国"},
	{"japanese", "日本", "日本"},
}

// Charts mirrors the tabs of movie.douban.com/explore and /tv. Upstream
// category and type values were verified against the live endpoint.
var Charts = []Chart{
	{ID: "movie_hot", Label: "热门电影", Kind: "movie", path: "movie", category: "热门", Filters: movieRegions},
	{ID: "movie_latest", Label: "最新电影", Kind: "movie", path: "movie", category: "最新", Filters: movieRegions},
	{ID: "movie_top", Label: "豆瓣高分", Kind: "movie", path: "movie", category: "豆瓣高分", Filters: movieRegions},
	{ID: "movie_underrated", Label: "冷门佳片", Kind: "movie", path: "movie", category: "冷门佳片", Filters: movieRegions},
	{ID: "tv", Label: "最近热门剧集", Kind: "tv", path: "tv", category: "tv", Filters: []Filter{
		{"all", "综合", "tv"},
		{"domestic", "国产剧", "tv_domestic"},
		{"american", "欧美剧", "tv_american"},
		{"japanese", "日剧", "tv_japanese"},
		{"korean", "韩剧", "tv_korean"},
		{"animation", "动画", "tv_animation"},
		{"documentary", "纪录片", "tv_documentary"},
	}},
	{ID: "variety", Label: "最近热门综艺", Kind: "tv", path: "tv", category: "show", Filters: []Filter{
		{"all", "综合", "show"},
		{"domestic", "国内", "show_domestic"},
		{"foreign", "国外", "show_foreign"},
	}},
}

// ChartIDs lists the chart ids, for schemas and error details.
func ChartIDs() []string {
	ids := make([]string, len(Charts))
	for i, c := range Charts {
		ids[i] = c.ID
	}
	return ids
}

// FindChart resolves a chart id and an optional filter (key or Chinese
// label, case-insensitive). An empty filter selects the chart's default.
func FindChart(id, filter string) (*Chart, *Filter, error) {
	for i := range Charts {
		c := &Charts[i]
		if c.ID != id {
			continue
		}
		if filter == "" {
			return c, &c.Filters[0], nil
		}
		for j := range c.Filters {
			f := &c.Filters[j]
			if strings.EqualFold(f.Key, filter) || f.Label == filter {
				return c, f, nil
			}
		}
		keys := make([]string, len(c.Filters))
		for j, f := range c.Filters {
			keys[j] = f.Key
		}
		return nil, nil, invalid("filter", map[string]any{"allowed": keys})
	}
	return nil, nil, invalid("chart", map[string]any{"allowed": ChartIDs()})
}
