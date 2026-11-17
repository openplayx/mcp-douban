package douban

import (
	"regexp"
	"strings"
)

// Rating is Douban's 10-point score. It is null for subjects Douban does
// not rate (too few votes, not released).
type Rating struct {
	Value float64 `json:"value"`
	Count int     `json:"count"`
}

// Item is one entry of a chart or search result.
type Item struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"` // movie | tv
	Title        string  `json:"title"`
	Year         string  `json:"year,omitempty"`
	Subtitle     string  `json:"subtitle,omitempty"` // "年份 / 地区 / 类型 / 导演 / 主演"
	Rating       *Rating `json:"rating"`
	Poster       string  `json:"poster,omitempty"`       // doubanio.com URL, needs a Douban Referer
	PosterProxy  string  `json:"poster_proxy,omitempty"` // loadable without a Referer
	EpisodesInfo string  `json:"episodes_info,omitempty"`
	IsNew        bool    `json:"is_new,omitempty"`
	URL          string  `json:"url"`
}

// Subject is a movie or TV subject's details.
type Subject struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"` // movie | tv
	IsShow        bool     `json:"is_show"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Aka           []string `json:"aka"`
	Year          string   `json:"year,omitempty"`
	Genres        []string `json:"genres"`
	Countries     []string `json:"countries"`
	Languages     []string `json:"languages"`
	Durations     []string `json:"durations"`
	Pubdates      []string `json:"pubdates"`
	EpisodesCount int      `json:"episodes_count,omitempty"`
	Rating        *Rating  `json:"rating"`
	Directors     []string `json:"directors"`
	Actors        []string `json:"actors"`
	Tags          []string `json:"tags"`
	Honors        []Honor  `json:"honors"`
	Intro         string   `json:"intro,omitempty"`
	Poster        string   `json:"poster,omitempty"`
	PosterProxy   string   `json:"poster_proxy,omitempty"`
	URL           string   `json:"url"`
	Cached        bool     `json:"cached"`
}

// Honor is a ranking a subject is on, e.g. {"豆瓣电影Top250", 1}.
type Honor struct {
	Title string `json:"title"`
	Rank  int    `json:"rank,omitempty"`
}

// Credits is a subject's cast and crew.
type Credits struct {
	ID        string   `json:"id"`
	Directors []Person `json:"directors"`
	Actors    []Person `json:"actors"`
	Cached    bool     `json:"cached"`
}

type Person struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	LatinName   string   `json:"latin_name,omitempty"`
	Character   string   `json:"character,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	Avatar      string   `json:"avatar,omitempty"`
	AvatarProxy string   `json:"avatar_proxy,omitempty"`
	URL         string   `json:"url,omitempty"`
}

// ------------------------------------------------------- upstream shapes

type rawRating struct {
	Value float64 `json:"value"`
	Count int     `json:"count"`
}

type rawPic struct {
	Large  string `json:"large"`
	Normal string `json:"normal"`
}

type rawItem struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Type         string     `json:"type"`
	Year         string     `json:"year"`
	CardSubtitle string     `json:"card_subtitle"`
	Rating       *rawRating `json:"rating"`
	Pic          rawPic     `json:"pic"`
	CoverURL     string     `json:"cover_url"`
	EpisodesInfo string     `json:"episodes_info"`
	IsNew        bool       `json:"is_new"`
}

type rawName struct {
	Name string `json:"name"`
}

type rawSubject struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	IsTV          bool       `json:"is_tv"`
	IsShow        bool       `json:"is_show"`
	Title         string     `json:"title"`
	OriginalTitle string     `json:"original_title"`
	Aka           []string   `json:"aka"`
	Year          string     `json:"year"`
	Genres        []string   `json:"genres"`
	Countries     []string   `json:"countries"`
	Languages     []string   `json:"languages"`
	Durations     []string   `json:"durations"`
	Pubdate       []string   `json:"pubdate"`
	EpisodesCount int        `json:"episodes_count"`
	Rating        *rawRating `json:"rating"`
	Directors     []rawName  `json:"directors"`
	Actors        []rawName  `json:"actors"`
	Tags          []rawName  `json:"tags"`
	HonorInfos    []struct {
		Title string `json:"title"`
		Rank  int    `json:"rank"`
	} `json:"honor_infos"`
	Intro    string `json:"intro"`
	Pic      rawPic `json:"pic"`
	CoverURL string `json:"cover_url"`
	URL      string `json:"url"`
}

type rawPerson struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	LatinName string   `json:"latin_name"`
	Character string   `json:"character"`
	Roles     []string `json:"roles"`
	Avatar    rawPic   `json:"avatar"`
	URL       string   `json:"url"`
}

// ------------------------------------------------------- normalisation

func subjectURL(id string) string { return "https://movie.douban.com/subject/" + id + "/" }

func rating(r *rawRating) *Rating {
	// Douban sends {"value":0,"count":0} or null for unrated subjects.
	if r == nil || r.Value <= 0 {
		return nil
	}
	return &Rating{Value: r.Value, Count: r.Count}
}

var yearPrefix = regexp.MustCompile(`^(\d{4})\b`)

// yearOf takes the year from a card subtitle ("2026 / 韩国 / ...").
func yearOf(subtitle string) string {
	if m := yearPrefix.FindStringSubmatch(subtitle); m != nil {
		return m[1]
	}
	return ""
}

func (c *Client) proxied(u string) string {
	if u == "" || c.imageURL == nil {
		return ""
	}
	return c.imageURL(u)
}

func (c *Client) item(id, typ, title, year, subtitle string, r *rawRating, poster, episodes string, isNew bool) Item {
	if typ != "tv" {
		typ = "movie"
	}
	return Item{
		ID: id, Type: typ, Title: title, Year: year, Subtitle: subtitle, Rating: rating(r),
		Poster: poster, PosterProxy: c.proxied(poster), EpisodesInfo: episodes, IsNew: isNew, URL: subjectURL(id),
	}
}

func names(in []rawName) []string {
	out := make([]string, 0, len(in))
	for _, n := range in {
		if s := strings.TrimSpace(n.Name); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (c *Client) subject(r *rawSubject) *Subject {
	typ := "movie"
	if r.IsTV || r.Type == "tv" {
		typ = "tv"
	}
	poster := r.Pic.Large
	if poster == "" {
		poster = r.CoverURL
	}
	s := &Subject{
		ID: r.ID, Type: typ, IsShow: r.IsShow, Title: r.Title, OriginalTitle: r.OriginalTitle,
		Aka: nonNil(r.Aka), Year: r.Year, Genres: nonNil(r.Genres), Countries: nonNil(r.Countries),
		Languages: nonNil(r.Languages), Durations: nonNil(r.Durations), Pubdates: nonNil(r.Pubdate),
		EpisodesCount: r.EpisodesCount, Rating: rating(r.Rating), Directors: names(r.Directors),
		Actors: names(r.Actors), Tags: names(r.Tags), Honors: []Honor{}, Intro: strings.TrimSpace(r.Intro),
		Poster: poster, PosterProxy: c.proxied(poster), URL: subjectURL(r.ID),
	}
	if s.OriginalTitle == s.Title {
		s.OriginalTitle = ""
	}
	for _, h := range r.HonorInfos {
		if h.Title != "" {
			s.Honors = append(s.Honors, Honor{Title: h.Title, Rank: h.Rank})
		}
	}
	return s
}

func (c *Client) people(in []rawPerson) []Person {
	out := make([]Person, 0, len(in))
	for _, p := range in {
		if p.Name == "" {
			continue
		}
		avatar := p.Avatar.Large
		out = append(out, Person{
			ID: p.ID, Name: p.Name, LatinName: p.LatinName, Character: p.Character, Roles: p.Roles,
			Avatar: avatar, AvatarProxy: c.proxied(avatar), URL: p.URL,
		})
	}
	return out
}
