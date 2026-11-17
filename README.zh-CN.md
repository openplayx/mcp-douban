# mcp-douban

[English](README.md) | 简体中文

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

把豆瓣公开的电影和剧集数据封装成 HTTP API 和 MCP 服务。单个静态 Go 二进制，只依赖标准库。

- **榜单**：电影的热门 / 最新 / 豆瓣高分 / 冷门佳片 × 全部 / 华语 / 欧美 / 韩国 / 日本；近期热门剧集（综合、国产剧、欧美剧、日剧、韩剧、动画、纪录片）和综艺（综合、国内、国外）。
- **搜索**：按片名或别名搜索电影和剧集，返回豆瓣 id、年份、类型、评分。
- **条目详情**：片名和别名、年份、类型、国家/地区、语言、片长、上映日期、集数、评分、导演、演员、标签、荣誉（如 Top250 排名）、简介。
- **完整演职员表**，带外文名和角色名。
- **海报代理**：豆瓣图片不带豆瓣 Referer 的请求会被拒绝。

## 工作原理

豆瓣移动版网站（`m.douban.com`）的数据来自 `https://m.douban.com/rexxar/api/v2/` 下的 JSON 接口。这些接口不需要登录、key 或签名，只要求 `Referer` 是 douban.com 的页面，否则返回 HTTP 400。`*.doubanio.com` 上的图片也是同样的规则（返回 HTTP 418）。

| 数据 | 上游接口 |
|---|---|
| 电影榜单 | `subject/recent_hot/movie?category={热门,最新,豆瓣高分,冷门佳片}&type={全部,华语,欧美,韩国,日本}` |
| 剧集 / 综艺榜单 | `subject/recent_hot/tv?category={tv,show}&type={tv,tv_domestic,…,show_foreign}` |
| 搜索 | `search/movie?q=…`（通用的 `search` 需要登录） |
| 详情 | `movie/{id}`（剧集条目也走这个接口） |
| 演职员 | `movie/{id}/celebrities`（`credits` 最多只返回 8 条） |

服务带上 Referer 和浏览器 User-Agent 发出这些请求，把响应规范化，并额外提供：

- 内存 TTL 缓存（榜单 30 分钟、搜索 1 小时、条目 6 小时），并合并相同的并发请求，只向上游发一次；
- 上游请求之间保持间隔（≥ 200 ms），避免突发请求看起来像爬虫；
- 豆瓣接口变化时明确报错：4xx → `upstream_rejected`（附豆瓣自己的 `msg`），JSON 无法解析或结构变了 → `upstream_changed`，网络错误/5xx → `upstream_unavailable`。接口坏掉时不会表现为空列表。

这些接口没有公开文档，随时可能变化。可以运行 `scripts/smoke.sh` 对真实网站做检查。数据归豆瓣所有，请在豆瓣的条款范围内使用。

本项目是 [OpenPlayX](https://github.com/openplayx/openplayx) 的一部分，也可以在任何 MCP 客户端中使用。

## 快速开始

依赖：Go 1.26+（或 Docker）。

```bash
go run ./src/cmd/mcp-douban                       # :8800，不鉴权
API_TOKENS=s3cret PUBLIC_URL=https://douban.example.com go run ./src/cmd/mcp-douban
```

Docker：

```bash
cp deploy/env.example deploy/.env             # 设置 API_TOKENS、PUBLIC_URL
docker compose -f deploy/compose.yaml up -d --build
TOKEN=s3cret scripts/smoke.sh http://127.0.0.1:8800
```

| 变量 | 默认值 | 含义 |
|---|---|---|
| `LISTEN` | `:8800` | 监听地址（`-listen` 参数优先） |
| `API_TOKENS` | 空 | 逗号分隔的 Bearer token；为空则不鉴权 |
| `PUBLIC_URL` | 空 | `poster_proxy` 链接的基础 URL；为空则只返回路径 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `DOUBAN_BASE_URL` | 豆瓣 | 覆盖上游地址，用于测试 |

## HTTP API

设置了 token 后，除 `/api/healthz` 和 `/api/v1/images` 以外的所有路由都需要 `Authorization: Bearer <token>`。图片路由保持公开，方便 `<img>` 标签直接使用；它只会请求 `https://*.doubanio.com`。

| 路由 | 用途 |
|---|---|
| `GET /api/healthz` | 存活检查、版本、缓存大小 |
| `GET /api/v1/charts` | 榜单 id 和筛选项 |
| `GET /api/v1/charts/{chart}?filter=&start=0&limit=20` | 榜单的一页（`limit` ≤ 100） |
| `GET /api/v1/search?q=&start=0&count=10` | 搜索（`count` ≤ 20） |
| `GET /api/v1/subjects/{id}` | 详情 |
| `GET /api/v1/subjects/{id}/credits` | 导演和演员 |
| `GET /api/v1/images?url=` | 海报/头像代理，客户端缓存 7 天 |
| `POST /api/mcp` | MCP（见下文） |

榜单 id：`movie_hot`、`movie_latest`、`movie_top`、`movie_underrated`、`tv`、`variety`。筛选项可以用 key（`korean`、`animation` 等），也可以用中文名（`韩国`、`动画` 等）。

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

豆瓣没有评分时 `rating` 为 `null`。错误格式：

```json
{"error": {"status": 404, "code": "not_found", "message": "no such subject on Douban", "retryable": false}}
```

| 错误码 | HTTP | 含义 |
|---|---|---|
| `invalid_request` | 400 | 参数错误；`detail.field` 指出字段，`detail.allowed` 列出可选值 |
| `unauthorized` | 401 | 缺少 token 或 token 错误 |
| `not_found` | 404 | 豆瓣没有这个条目 |
| `upstream_rejected` | 502 | 豆瓣拒绝了请求（访问规则变化、IP 被封、限流） |
| `upstream_changed` | 502 | 豆瓣的响应结构和预期不符 |
| `upstream_unavailable` | 502 | 网络错误、超时或豆瓣 5xx；`retryable: true` |

## MCP

工具（全部只读）：`list_charts`、`get_chart`、`search`、`get_subject`、`get_credits`。结果以 `structuredContent` 返回，同时附带相同内容的 JSON 文本；失败时返回 `isError: true` 的工具结果，内容是上面的错误对象。

**Streamable HTTP**：`POST /api/mcp`，单条 JSON-RPC 2.0 消息，以 `application/json` 应答（不用 SSE；`GET` → 405），无状态，支持协议版本 2025-11-25、2025-06-18 和 2025-03-26。与 HTTP API 使用同一个 Bearer token；来自外部 `Origin` 的请求会被拒绝。

**stdio**，用于桌面 MCP 客户端：

```json
{
  "mcpServers": {
    "douban": { "command": "/usr/local/bin/mcp-douban", "args": ["stdio"] }
  }
}
```

stdio 模式没有图片代理，所以不返回 `poster_proxy`。

## 开发

```bash
go test -race ./...            # 离线：使用 src/internal/douban/testdata 里录制的豆瓣响应
scripts/smoke.sh               # 在线：对运行中的实例调用真实豆瓣
scripts/build.sh linux/amd64   # 静态二进制输出到 dist/
```

```text
src/cmd/mcp-douban/   入口：HTTP 服务、`stdio`、`version`
src/internal/douban/  上游客户端：榜单目录、缓存、请求间隔、规范化、图片获取
src/internal/mcp/     MCP 工具和 JSON-RPC 处理（与传输方式无关）
src/internal/api/     HTTP 路由、鉴权、图片代理、/api/mcp
deploy/               compose 文件和环境变量模板
scripts/              构建和在线冒烟测试
```

## 参与贡献

欢迎提 issue 和 PR，见 [CONTRIBUTING.md](CONTRIBUTING.md)（英文）。安全问题请按 [SECURITY.md](SECURITY.md)（英文）私下报告。

## 声明

本项目是非官方项目，与豆瓣没有关联，也未获豆瓣认可。它使用的是未公开的接口，随时可能变化或失效。所有数据和图片归豆瓣及其权利人所有；请仅用于个人、非商业用途，遵守豆瓣的服务条款，并控制请求频率。

## 许可证

[MIT](LICENSE)
