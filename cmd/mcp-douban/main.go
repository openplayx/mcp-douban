// Command mcp-douban serves public Douban movie/TV data over HTTP and MCP.
//
//	mcp-douban [-listen :8800]   HTTP API + POST /api/mcp + image proxy
//	mcp-douban stdio             MCP over stdin/stdout (for desktop MCP clients)
//	mcp-douban version
//
// Environment:
//
//	LISTEN            listen address (default :8800; -listen wins)
//	API_TOKENS        comma-separated bearer tokens; empty = no auth
//	PUBLIC_URL        external base URL used in poster_proxy links (default: relative)
//	DOUBAN_BASE_URL   override the upstream API base (tests)
//	LOG_LEVEL         debug | info | warn | error (default info)
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/openplayx/mcp-douban/internal/api"
	"github.com/openplayx/mcp-douban/internal/douban"
	"github.com/openplayx/mcp-douban/internal/mcp"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version)
			return
		case "stdio":
			log := newLogger()
			if err := stdio(log); err != nil {
				log.Error("fatal", "err", err)
				os.Exit(1)
			}
			return
		}
	}
	listen := flag.String("listen", env("LISTEN", ":8800"), "listen address")
	flag.Parse()
	log := newLogger()
	if err := serve(*listen, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// newLogger writes JSON to stderr; stdout stays clean for the stdio transport.
func newLogger() *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func newClient(imageURL func(string) string) *douban.Client {
	return douban.New(douban.Options{BaseURL: os.Getenv("DOUBAN_BASE_URL"), ImageURL: imageURL})
}

func serve(listen string, log *slog.Logger) error {
	publicURL := os.Getenv("PUBLIC_URL")
	client := newClient(api.ImageURL(publicURL))
	var tokens []string
	if t := os.Getenv("API_TOKENS"); t != "" {
		tokens = strings.Split(t, ",")
	}
	if len(tokens) == 0 {
		log.Warn("API_TOKENS is empty: API and MCP endpoints accept unauthenticated requests")
	}
	srv := &http.Server{
		Addr:              listen,
		Handler:           api.New(client, &http.Client{Timeout: 30 * time.Second}, tokens, log, version),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", listen, "version", version, "auth", len(tokens) > 0)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// stdio runs the MCP stdio transport: newline-delimited JSON-RPC messages
// on stdin, replies on stdout. Poster proxy links are omitted (no server).
func stdio(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := mcp.New(newClient(nil), log, version)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 1<<20)
	out := json.NewEncoder(os.Stdout)
	out.SetEscapeHTML(false)
	for in.Scan() {
		line := in.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		reply, _ := srv.Handle(ctx, line)
		if reply == nil {
			continue
		}
		if err := out.Encode(reply); err != nil {
			return err
		}
	}
	return in.Err()
}
