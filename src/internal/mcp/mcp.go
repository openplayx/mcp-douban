// Package mcp implements the Model Context Protocol tool server over the
// douban client. It is transport-agnostic: Handle takes one JSON-RPC 2.0
// message and returns the reply; internal/api serves it over Streamable
// HTTP (POST /api/mcp) and cmd/mcp-douban over stdio.
//
// Supported subset: single messages (no batches), no server-initiated
// messages, stateless (no Mcp-Session-Id); initialize, ping, tools/list,
// tools/call and notifications. Business failures are tool results with
// isError=true carrying the same error object as the HTTP API.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/openplayx/mcp-douban/src/internal/douban"
)

// Versions are the protocol versions this server speaks, newest first.
var Versions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
)

const instructions = "Read-only access to public Douban (豆瓣) movie and TV data: ranking charts " +
	"(热门/最新/高分/冷门佳片 movies, recent hot TV series and variety shows), search, subject " +
	"details and full cast. Use list_charts to see chart ids and filters, search to turn a title " +
	"into a Douban id, then get_subject / get_credits. Ratings are out of 10 and null when Douban " +
	"has none. Poster URLs need a Douban Referer; poster_proxy (when present) loads anywhere."

// Server answers MCP messages using a douban client.
type Server struct {
	client  *douban.Client
	log     *slog.Logger
	version string
}

func New(client *douban.Client, log *slog.Logger, version string) *Server {
	return &Server{client: client, log: log, version: version}
}

type tool struct {
	Name, Title, Description string
	Schema                   map[string]any
	Run                      func(ctx context.Context, c *douban.Client, args json.RawMessage) (any, error)
}

func object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func integer(desc string, def, min, max int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "default": def, "minimum": min, "maximum": max}
}

var idProp = map[string]any{"type": "string", "pattern": "^[0-9]{1,12}$", "description": "Douban subject id, e.g. \"1292052\" (from search or a chart)."}

// decode is strict: unknown fields are an error, so typos surface.
func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		return &douban.Error{Status: http.StatusBadRequest, Code: douban.CodeInvalidRequest, Message: "invalid arguments: " + errText(err)}
	}
	return nil
}

func errText(err error) string {
	if err == nil {
		return "trailing data"
	}
	return err.Error()
}

var tools = []tool{
	{
		Name: "list_charts", Title: "List Douban charts",
		Description: "Lists the available ranking charts with their ids and filters (region or genre). " +
			"Pass a chart id and filter key to get_chart.",
		Schema: object(map[string]any{}),
		Run: func(ctx context.Context, c *douban.Client, raw json.RawMessage) (any, error) {
			if err := decode(raw, &struct{}{}); err != nil {
				return nil, err
			}
			return map[string]any{"charts": douban.Charts}, nil
		},
	},
	{
		Name: "get_chart", Title: "Get a Douban chart",
		Description: "One page of a ranking chart, in Douban's order: movie_hot (热门), movie_latest (最新), " +
			"movie_top (豆瓣高分), movie_underrated (冷门佳片), tv (热门剧集), variety (热门综艺).",
		Schema: object(map[string]any{
			"chart":  map[string]any{"type": "string", "enum": douban.ChartIDs()},
			"filter": map[string]any{"type": "string", "description": "Filter key from list_charts (e.g. all, chinese, korean; tv: domestic, american, animation…) or its Chinese label. Default: the chart's first filter."},
			"start":  integer("Offset into the chart.", 0, 0, 1000),
			"limit":  integer("Number of items.", 20, 1, 100),
		}, "chart"),
		Run: func(ctx context.Context, c *douban.Client, raw json.RawMessage) (any, error) {
			a := struct {
				Chart  string `json:"chart"`
				Filter string `json:"filter"`
				Start  int    `json:"start"`
				Limit  int    `json:"limit"`
			}{Limit: 20}
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return c.Chart(ctx, a.Chart, a.Filter, a.Start, a.Limit)
		},
	},
	{
		Name: "search", Title: "Search Douban",
		Description: "Searches movies and TV series by title (Chinese, original or alias) and returns Douban ids, " +
			"year, type and rating.",
		Schema: object(map[string]any{
			"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 100},
			"start": integer("Offset into the results.", 0, 0, 200),
			"count": integer("Number of results.", 10, 1, 20),
		}, "query"),
		Run: func(ctx context.Context, c *douban.Client, raw json.RawMessage) (any, error) {
			a := struct {
				Query string `json:"query"`
				Start int    `json:"start"`
				Count int    `json:"count"`
			}{Count: 10}
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return c.Search(ctx, a.Query, a.Start, a.Count)
		},
	},
	{
		Name: "get_subject", Title: "Get Douban subject",
		Description: "Details of a movie or TV subject: titles and aliases, year, genres, countries, languages, " +
			"durations, release dates, episode count, rating, directors, main actors, tags, honours and synopsis.",
		Schema: object(map[string]any{"id": idProp}, "id"),
		Run: func(ctx context.Context, c *douban.Client, raw json.RawMessage) (any, error) {
			var a struct {
				ID string `json:"id"`
			}
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return c.Subject(ctx, a.ID)
		},
	},
	{
		Name: "get_credits", Title: "Get Douban cast and crew",
		Description: "Full cast and directors of a subject, with Latin names and the characters actors play.",
		Schema:      object(map[string]any{"id": idProp}, "id"),
		Run: func(ctx context.Context, c *douban.Client, raw json.RawMessage) (any, error) {
			var a struct {
				ID string `json:"id"`
			}
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return c.Credits(ctx, a.ID)
		},
	},
}

func describe(t tool) map[string]any {
	return map[string]any{
		"name": t.Name, "title": t.Title, "description": t.Description, "inputSchema": t.Schema,
		"annotations": map[string]any{
			"title": t.Title, "readOnlyHint": true, "destructiveHint": false,
			"idempotentHint": true, "openWorldHint": true,
		},
	}
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Reply builds a JSON-RPC response. A nil id becomes JSON null.
func Reply(id json.RawMessage, result any, rpcErr map[string]any) map[string]any {
	body := map[string]any{"jsonrpc": "2.0"}
	if len(id) == 0 {
		body["id"] = nil
	} else {
		body["id"] = id
	}
	if rpcErr != nil {
		body["error"] = rpcErr
	} else {
		body["result"] = result
	}
	return body
}

func RPCError(code int, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}

func toolResult(payload any, isError bool) map[string]any {
	text, _ := json.Marshal(payload)
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(text)}},
		"structuredContent": payload,
		"isError":           isError,
	}
}

func (s *Server) toolError(name string, err error) map[string]any {
	var e *douban.Error
	if !errors.As(err, &e) {
		s.log.Error("tool failed", "tool", name, "err", err)
		e = &douban.Error{Status: http.StatusInternalServerError, Code: "internal_error", Message: "internal error", Retryable: true}
	} else if e.Status >= 500 {
		s.log.Warn("tool failed", "tool", name, "code", e.Code, "detail", e.Detail)
	}
	return toolResult(map[string]any{"error": e}, true)
}

// Handle answers one JSON-RPC message. It returns nil for notifications and
// client responses, which get no reply. status is the HTTP status to use.
func (s *Server) Handle(ctx context.Context, raw []byte) (reply map[string]any, status int) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '{' {
		return Reply(nil, nil, RPCError(InvalidRequest, "Expected one JSON-RPC 2.0 message.")), http.StatusBadRequest
	}
	var msg message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Reply(nil, nil, RPCError(ParseError, "Parse error.")), http.StatusBadRequest
	}
	if msg.JSONRPC != "2.0" {
		return Reply(msg.ID, nil, RPCError(InvalidRequest, "Expected one JSON-RPC 2.0 message.")), http.StatusBadRequest
	}
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		return nil, http.StatusAccepted
	}
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		version := Versions[0]
		if slices.Contains(Versions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return Reply(msg.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "mcp-douban", "title": "Douban movies & TV", "version": s.version},
			"instructions":    instructions,
		}, nil), http.StatusOK
	case "ping":
		return Reply(msg.ID, map[string]any{}, nil), http.StatusOK
	case "tools/list":
		list := make([]any, len(tools))
		for i, t := range tools {
			list[i] = describe(t)
		}
		return Reply(msg.ID, map[string]any{"tools": list}, nil), http.StatusOK
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || p.Name == "" {
			return Reply(msg.ID, nil, RPCError(InvalidParams, "params.name is required.")), http.StatusOK
		}
		i := slices.IndexFunc(tools, func(t tool) bool { return t.Name == p.Name })
		if i < 0 {
			return Reply(msg.ID, nil, RPCError(InvalidParams, "Unknown tool.")), http.StatusOK
		}
		v, err := tools[i].Run(ctx, s.client, p.Arguments)
		if err != nil {
			return Reply(msg.ID, s.toolError(p.Name, err), nil), http.StatusOK
		}
		return Reply(msg.ID, toolResult(v, false), nil), http.StatusOK
	default:
		return Reply(msg.ID, nil, RPCError(MethodNotFound, "Method not found.")), http.StatusOK
	}
}
