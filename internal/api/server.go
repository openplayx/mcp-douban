// Package api serves the HTTP API, the MCP endpoint and the image proxy.
//
//	GET  /api/healthz                       liveness (no auth)
//	GET  /api/v1/charts                     chart catalogue
//	GET  /api/v1/charts/{chart}             ?filter=&start=&limit=
//	GET  /api/v1/search                     ?q=&start=&count=
//	GET  /api/v1/subjects/{id}              details
//	GET  /api/v1/subjects/{id}/credits      cast and crew
//	GET  /api/v1/images                     ?url=https://imgX.doubanio.com/... (no auth, for <img>)
//	POST /api/mcp                           MCP Streamable HTTP (JSON responses only)
//
// When tokens are configured every route except healthz and images needs
// "Authorization: Bearer <token>".
package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openplayx/mcp-douban/internal/douban"
	"github.com/openplayx/mcp-douban/internal/mcp"
)

const (
	maxMCPBody  = 64 << 10
	maxImage    = 15 << 20
	imagePath   = "/api/v1/images"
	imageMaxAge = 7 * 24 * time.Hour
)

type Server struct {
	client  *douban.Client
	mcp     *mcp.Server
	images  *http.Client
	tokens  [][32]byte
	log     *slog.Logger
	version string
	mux     *http.ServeMux
}

// ImageURL returns the proxy URL for a doubanio.com image. base is the
// service's public URL ("" for a path relative to the host).
func ImageURL(base string) func(string) string {
	base = strings.TrimSuffix(base, "/")
	return func(u string) string { return base + imagePath + "?url=" + url.QueryEscape(u) }
}

// New builds the handler. Empty tokens disable authentication.
func New(client *douban.Client, imageHTTP *http.Client, tokens []string, log *slog.Logger, version string) *Server {
	s := &Server{
		client: client, mcp: mcp.New(client, log, version), images: douban.ImageClient(imageHTTP),
		log: log, version: version, mux: http.NewServeMux(),
	}
	for _, t := range tokens {
		if t = strings.TrimSpace(t); t != "" {
			s.tokens = append(s.tokens, sha256.Sum256([]byte(t)))
		}
	}
	s.mux.HandleFunc("GET /api/healthz", s.healthz)
	s.mux.HandleFunc("GET "+imagePath, s.image)
	s.mux.HandleFunc("GET /api/v1/charts", s.auth(s.charts))
	s.mux.HandleFunc("GET /api/v1/charts/{chart}", s.auth(s.chart))
	s.mux.HandleFunc("GET /api/v1/search", s.auth(s.search))
	s.mux.HandleFunc("GET /api/v1/subjects/{id}", s.auth(s.subject))
	s.mux.HandleFunc("GET /api/v1/subjects/{id}/credits", s.auth(s.credits))
	s.mux.HandleFunc("POST /api/mcp", s.auth(s.mcpHTTP))
	s.mux.HandleFunc("GET /api/mcp", func(w http.ResponseWriter, r *http.Request) {
		// No server-initiated stream (spec: 405 when SSE is not offered).
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, &douban.Error{Status: http.StatusNotFound, Code: douban.CodeNotFound, Message: "no such route"})
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(sw, r)
	// Paths only: queries may carry search terms.
	s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.tokens) > 0 {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			sum := sha256.Sum256([]byte(token))
			if !ok || token == "" || !slices.ContainsFunc(s.tokens, func(t [32]byte) bool {
				return subtle.ConstantTimeCompare(t[:], sum[:]) == 1
			}) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mcp-douban"`)
				writeError(w, &douban.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "missing or invalid bearer token"})
				return
			}
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	var e *douban.Error
	if !errors.As(err, &e) {
		e = &douban.Error{Status: http.StatusInternalServerError, Code: "internal_error", Message: "internal error", Retryable: true}
	}
	writeJSON(w, e.Status, map[string]any{"error": e})
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		var e *douban.Error
		if !errors.As(err, &e) {
			s.log.Error("request failed", "path", r.URL.Path, "err", err)
		} else if e.Status >= 500 {
			s.log.Warn("upstream failure", "path", r.URL.Path, "code", e.Code, "detail", e.Detail)
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// intParam reads an optional integer query parameter.
func intParam(r *http.Request, name string, def int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, &douban.Error{Status: http.StatusBadRequest, Code: douban.CodeInvalidRequest, Message: "invalid " + name,
			Detail: map[string]any{"field": name}}
	}
	return n, nil
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version, "cache_entries": s.client.CacheLen()})
}

func (s *Server) charts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"charts": douban.Charts})
}

func (s *Server) chart(w http.ResponseWriter, r *http.Request) {
	start, err := intParam(r, "start", 0)
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := intParam(r, "limit", 20)
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := s.client.Chart(r.Context(), r.PathValue("chart"), r.URL.Query().Get("filter"), start, limit)
	s.reply(w, r, v, err)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	start, err := intParam(r, "start", 0)
	if err != nil {
		writeError(w, err)
		return
	}
	count, err := intParam(r, "count", 10)
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := s.client.Search(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")), start, count)
	s.reply(w, r, v, err)
}

func (s *Server) subject(w http.ResponseWriter, r *http.Request) {
	v, err := s.client.Subject(r.Context(), r.PathValue("id"))
	s.reply(w, r, v, err)
}

func (s *Server) credits(w http.ResponseWriter, r *http.Request) {
	v, err := s.client.Credits(r.Context(), r.PathValue("id"))
	s.reply(w, r, v, err)
}

// image proxies a doubanio.com image, adding the Referer Douban requires.
func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	resp, err := douban.FetchImage(r.Context(), s.images, r.URL.Query().Get("url"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxImage {
		writeError(w, &douban.Error{Status: http.StatusBadGateway, Code: douban.CodeUpstreamChanged, Message: "image too large"})
		return
	}
	h := w.Header()
	h.Set("Content-Type", resp.Header.Get("Content-Type"))
	if resp.ContentLength > 0 {
		h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		h.Set("Last-Modified", lm)
	}
	h.Set("Cache-Control", "public, max-age="+strconv.Itoa(int(imageMaxAge.Seconds())))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxImage))
}

// originAllowed is the DNS-rebinding guard for /api/mcp: browsers may only
// call from our own origin; native clients send no Origin.
func originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func (s *Server) mcpHTTP(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r) {
		writeError(w, &douban.Error{Status: http.StatusForbidden, Code: "origin_not_allowed", Message: "foreign Origin"})
		return
	}
	if a := r.Header.Get("Accept"); a != "" && !strings.Contains(a, "application/json") && !strings.Contains(a, "*/*") {
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(mcp.Versions, v) {
		writeJSON(w, http.StatusBadRequest, mcp.Reply(nil, nil, mcp.RPCError(mcp.InvalidRequest, "Unsupported protocol version.")))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, mcp.Reply(nil, nil, mcp.RPCError(mcp.InvalidRequest, "Message too large.")))
		return
	}
	reply, status := s.mcp.Handle(r.Context(), raw)
	if reply == nil {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, reply)
}
