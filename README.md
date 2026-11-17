# mcp-douban

English | [简体中文](README.zh-CN.md)

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Public Douban (豆瓣) movie and TV data as an HTTP API and an MCP server, in
one static Go binary with no dependencies outside the standard library.

- **Charts**: 热门 / 最新 / 豆瓣高分 / 冷门佳片 movies × 全部 / 华语 / 欧美 / 韩国 / 日本;
  recent hot TV series (综合, 国产剧, 欧美剧, 日剧, 韩剧, 动画, 纪录片) and variety
  shows (综合, 国内, 国外).
- **Search** movies and TV by title or alias → Douban id, year, type, rating.
- **Subject details**: titles and aliases, year, genres, countries, languages,
  durations, release dates, episode count, rating, directors, actors, tags,
  honours (e.g. Top250 rank), synopsis.
- **Full cast and crew** with Latin names and characters.
- **Poster proxy**, since Douban images refuse requests without a Douban Referer.

## How it works

Douban's mobile web site (`m.douban.com`) loads its data from JSON endpoints
under `https://m.douban.com/rexxar/api/v2/`. They need no login, key or
signature, only a `Referer` on a douban.com page; without it they answer
HTTP 400. Images on `*.doubanio.com` apply the same rule (HTTP 418).

| Data | Upstream endpoint |
|---|---|
| Movie charts | `subject/recent_hot/movie?category={热门,最新,豆瓣高分,冷门佳片}&type={全部,华语,欧美,韩国,日本}` |
| TV / variety charts | `subject/recent_hot/tv?category={tv,show}&type={tv,tv_domestic,…,show_foreign}` |
| Search | `search/movie?q=…` (the generic `search` needs login) |
| Details | `movie/{id}` (also serves TV subjects) |
| Cast | `movie/{id}/celebrities` (`credits` is capped at 8 entries) |

The service sends those requests with the Referer and a browser User-Agent,
normalises the responses, and adds:

- an in-memory TTL cache (charts 30 min, search 1 h, subjects 6 h) with
  request coalescing, so concurrent identical requests make one upstream call;
- spacing between upstream requests (≥ 200 ms) so bursts do not look like a crawler;
- clear failures when Douban changes: 4xx → `upstream_rejected` (with
  Douban's own `msg`), unparseable or reshaped JSON → `upstream_changed`,
  network/5xx → `upstream_unavailable`. A broken endpoint never shows up as
  an empty list.

These endpoints are undocumented and can change at any time. Run
`scripts/smoke.sh` to check them against the live site. Data is Douban's;
use it within Douban's terms.

Part of [OpenPlayX](https://github.com/openplayx/openplayx), but usable from
any MCP client.

## Quick start

Requirements: Go 1.26+ (or Docker).

```bash
go run ./src/cmd/mcp-douban                       # :8800, no auth
API_TOKENS=s3cret PUBLIC_URL=https://douban.example.com go run ./src/cmd/mcp-douban
```

Docker:

```bash
cp deploy/env.example deploy/.env             # set API_TOKENS, PUBLIC_URL
docker compose -f deploy/compose.yaml up -d --build
TOKEN=s3cret scripts/smoke.sh http://127.0.0.1:8800
```

| Variable | Default | Meaning |
|---|---|---|
| `LISTEN` | `:8800` | listen address (`-listen` flag wins) |
| `API_TOKENS` | empty | comma-separated bearer tokens; empty disables auth |
| `PUBLIC_URL` | empty | base URL for `poster_proxy` links; empty = path only |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DOUBAN_BASE_URL` | Douban | upstream override, for tests |

## HTTP API

With tokens set, every route except `/api/healthz` and `/api/v1/images` needs
`Authorization: Bearer <token>`. The image route stays open so `<img>` tags
work; it only fetches `https://*.doubanio.com`.

| Route | Purpose |
|---|---|
| `GET /api/healthz` | liveness, version, cache size |
| `GET /api/v1/charts` | chart ids and filters |
| `GET /api/v1/charts/{chart}?filter=&start=0&limit=20` | one page of a chart (`limit` ≤ 100) |
| `GET /api/v1/search?q=&start=0&count=10` | search (`count` ≤ 20) |
| `GET /api/v1/subjects/{id}` | details |
| `GET /api/v1/subjects/{id}/credits` | directors and actors |
| `GET /api/v1/images?url=` | poster/avatar proxy, cached by clients for 7 days |
| `POST /api/mcp` | MCP (below) |

Chart ids: `movie_hot`, `movie_latest`, `movie_top`, `movie_underrated`, `tv`,
`variety`. Filters take the key (`korean`, `animation`, …) or the Chinese
label (`韩国`, `动画`, …).

```bash
curl -s 'http://127.0.0.1:8800/api/v1/charts/movie_top?filter=chinese&limit=3'
```

```json
{
  "chart": "movie_top", "label": "豆瓣高分", "filter": "chinese", "total": 117, "start": 0,
  "items": [{
    "id": "36882191", "type": "movie", "title": "八仙！", "year": "2026",
    "subtitle": "2026 / 中国大陆 / 喜剧 动画 奇幻 冒险 / 牟正洋 / 陈浩 李绍哲",
    "rating": {"value": 8.2, "count": 489992},
    "poster": "https://img9.doubanio.com/view/photo/m_ratio_poster/public/p2934074566.jpg",
    "poster_proxy": "/api/v1/images?url=https%3A%2F%2Fimg9.doubanio.com%2Fview%2Fphoto%2Fm_ratio_poster%2Fpublic%2Fp2934074566.jpg",
    "url": "https://movie.douban.com/subject/36882191/"
  }],
  "cached": false
}
```

`rating` is `null` when Douban has no score. Errors:

```json
{"error": {"status": 404, "code": "not_found", "message": "no such subject on Douban", "retryable": false}}
```

| Code | HTTP | Meaning |
|---|---|---|
| `invalid_request` | 400 | bad parameter; `detail.field` names it, `detail.allowed` lists choices |
| `unauthorized` | 401 | missing or wrong token |
| `not_found` | 404 | Douban has no such subject |
| `upstream_rejected` | 502 | Douban refused (access rules changed, IP blocked, rate limited) |
| `upstream_changed` | 502 | Douban's response no longer has the expected shape |
| `upstream_unavailable` | 502 | network error, timeout or Douban 5xx; `retryable: true` |

## MCP

Tools (all read-only): `list_charts`, `get_chart`, `search`, `get_subject`,
`get_credits`. Results are returned as `structuredContent` plus the same JSON
as text; failures are tool results with `isError: true` and the error object
above.

**Streamable HTTP** at `POST /api/mcp`: single JSON-RPC 2.0 messages answered
with `application/json` (no SSE; `GET` → 405), stateless, protocol versions
2025-11-25, 2025-06-18 and 2025-03-26. Same bearer token as the HTTP API;
requests with a foreign `Origin` are refused.

**stdio** for desktop MCP clients:

```json
{
  "mcpServers": {
    "douban": { "command": "/usr/local/bin/mcp-douban", "args": ["stdio"] }
  }
}
```

Over stdio there is no image proxy, so `poster_proxy` is omitted.

## Develop

```bash
go test -race ./...            # offline: recorded Douban responses in src/internal/douban/testdata
scripts/smoke.sh               # live: a running instance against real Douban
scripts/build.sh linux/amd64   # static binaries in dist/
```

```text
src/cmd/mcp-douban/   entry point: HTTP server, `stdio`, `version`
src/internal/douban/  upstream client: charts catalogue, cache, request spacing, normalisation, image fetch
src/internal/mcp/     MCP tools and JSON-RPC handling (transport-agnostic)
src/internal/api/     HTTP routes, auth, image proxy, /api/mcp
deploy/               compose file and env template
scripts/              build and live smoke test
```

## Contributing

Issues and pull requests are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md).
Report security problems privately as described in [SECURITY.md](SECURITY.md).

## Disclaimer

This is an unofficial project, not affiliated with or endorsed by Douban. It
uses undocumented endpoints that may change or stop working at any time. All
data and images belong to Douban and their owners; use this service for
personal, non-commercial purposes, respect Douban's terms of service, and keep
request rates low.

## License

[MIT](LICENSE)
