package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openplayx/mcp-douban/internal/douban"
)

func testServer(t *testing.T, tokens ...string) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/1292052":
			_, _ = io.WriteString(w, `{"id":"1292052","title":"肖申克的救赎","type":"movie","year":"1994",
				"rating":{"value":9.7,"count":10},"pic":{"large":"https://img3.doubanio.com/view/photo/p1.jpg"}}`)
		case "/subject/recent_hot/tv":
			_, _ = io.WriteString(w, `{"total":1,"items":[{"id":"1","title":"剧","type":"tv","card_subtitle":"2026 / 韩国","rating":null,"pic":{}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"msg":"traversal_error","code":404}`)
		}
	}))
	t.Cleanup(up.Close)
	client := douban.New(douban.Options{BaseURL: up.URL, MinInterval: -1, ImageURL: ImageURL("https://douban.example")})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(client, http.DefaultClient, tokens, log, "test"))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url, token, body string, hdr ...string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&v)
	return resp, v
}

func TestAuth(t *testing.T) {
	srv := testServer(t, "secret", "other")
	if r, _ := do(t, "GET", srv.URL+"/api/v1/charts", "", ""); r.StatusCode != 401 || r.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", srv.URL+"/api/v1/charts", "wrong", ""); r.StatusCode != 401 {
		t.Fatalf("wrong token: %d", r.StatusCode)
	}
	for _, tok := range []string{"secret", "other"} {
		if r, _ := do(t, "GET", srv.URL+"/api/v1/charts", tok, ""); r.StatusCode != 200 {
			t.Fatalf("token %s: %d", tok, r.StatusCode)
		}
	}
	if r, _ := do(t, "POST", srv.URL+"/api/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); r.StatusCode != 401 {
		t.Fatalf("mcp without token: %d", r.StatusCode)
	}
	if r, _ := do(t, "GET", srv.URL+"/api/healthz", "", ""); r.StatusCode != 200 {
		t.Fatalf("healthz: %d", r.StatusCode)
	}
}

func TestHTTPRoutes(t *testing.T) {
	srv := testServer(t)
	r, v := do(t, "GET", srv.URL+"/api/v1/subjects/1292052", "", "")
	if r.StatusCode != 200 || v["title"] != "肖申克的救赎" {
		t.Fatalf("subject: %d %v", r.StatusCode, v)
	}
	if v["poster_proxy"] != "https://douban.example/api/v1/images?url=https%3A%2F%2Fimg3.doubanio.com%2Fview%2Fphoto%2Fp1.jpg" {
		t.Fatalf("poster_proxy %v", v["poster_proxy"])
	}
	r, v = do(t, "GET", srv.URL+"/api/v1/charts/tv?filter=korean&limit=5", "", "")
	if r.StatusCode != 200 || v["total"].(float64) != 1 {
		t.Fatalf("chart: %d %v", r.StatusCode, v)
	}
	if items := v["items"].([]any); items[0].(map[string]any)["rating"] != nil {
		t.Fatalf("null rating not kept: %v", items[0])
	}
	if r, v = do(t, "GET", srv.URL+"/api/v1/subjects/42", "", ""); r.StatusCode != 404 || v["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("missing: %d %v", r.StatusCode, v)
	}
	if r, _ = do(t, "GET", srv.URL+"/api/v1/charts/tv?limit=abc", "", ""); r.StatusCode != 400 {
		t.Fatalf("bad limit: %d", r.StatusCode)
	}
	if r, _ = do(t, "GET", srv.URL+"/api/v1/images?url=https://evil.example/x.jpg", "", ""); r.StatusCode != 400 {
		t.Fatalf("image host not checked: %d", r.StatusCode)
	}
	if r, _ = do(t, "GET", srv.URL+"/nope", "", ""); r.StatusCode != 404 {
		t.Fatalf("unknown route: %d", r.StatusCode)
	}
}

func rpc(t *testing.T, srv *httptest.Server, body string, hdr ...string) (int, map[string]any) {
	r, v := do(t, "POST", srv.URL+"/api/mcp", "", body, append([]string{"Accept", "application/json, text/event-stream"}, hdr...)...)
	return r.StatusCode, v
}

func TestMCP(t *testing.T) {
	srv := testServer(t)
	st, v := rpc(t, srv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	res := v["result"].(map[string]any)
	if st != 200 || res["protocolVersion"] != "2025-06-18" || res["serverInfo"].(map[string]any)["name"] != "mcp-douban" {
		t.Fatalf("initialize: %d %v", st, v)
	}
	if st, _ = rpc(t, srv, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); st != 202 {
		t.Fatalf("notification: %d", st)
	}
	_, v = rpc(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var names []string
	for _, tl := range v["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "list_charts,get_chart,search,get_subject,get_credits" {
		t.Fatalf("tools: %v", names)
	}

	_, v = rpc(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_subject","arguments":{"id":"1292052"}}}`)
	res = v["result"].(map[string]any)
	if res["isError"] != false || res["structuredContent"].(map[string]any)["year"] != "1994" {
		t.Fatalf("get_subject: %v", v)
	}
	if text := res["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "肖申克的救赎") {
		t.Fatalf("text content: %s", text)
	}

	_, v = rpc(t, srv, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_subject","arguments":{"id":"42"}}}`)
	res = v["result"].(map[string]any)
	if res["isError"] != true || res["structuredContent"].(map[string]any)["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("tool error: %v", v)
	}
	_, v = rpc(t, srv, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_chart","arguments":{"chart":"tv","bogus":1}}}`)
	if v["result"].(map[string]any)["isError"] != true {
		t.Fatalf("unknown argument accepted: %v", v)
	}
	_, v = rpc(t, srv, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope"}}`)
	if v["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("unknown tool: %v", v)
	}
	_, v = rpc(t, srv, `{"jsonrpc":"2.0","id":7,"method":"resources/list"}`)
	if v["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", v)
	}
	if st, _ = rpc(t, srv, `[{"jsonrpc":"2.0","id":8,"method":"ping"}]`); st != 400 {
		t.Fatalf("batch: %d", st)
	}
	if st, _ = rpc(t, srv, `{"jsonrpc":"2.0","id":9,"method":"ping"}`, "Origin", "http://evil.example"); st != 403 {
		t.Fatalf("foreign origin: %d", st)
	}
	if st, _ = rpc(t, srv, `{"jsonrpc":"2.0","id":10,"method":"ping"}`, "MCP-Protocol-Version", "1999-01-01"); st != 400 {
		t.Fatalf("bad version: %d", st)
	}
	if r, _ := do(t, "GET", srv.URL+"/api/mcp", "", ""); r.StatusCode != 405 {
		t.Fatalf("GET /api/mcp: %d", r.StatusCode)
	}
}
