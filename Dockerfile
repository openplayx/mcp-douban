# Multi-stage build: static binary (no cgo) on a small Alpine base.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/mcp-douban ./cmd/mcp-douban

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata tini && adduser -D -H -u 10001 app
COPY --from=build /out/mcp-douban /usr/local/bin/mcp-douban
USER app
ENV LISTEN=:8800
EXPOSE 8800
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8800/api/healthz >/dev/null || exit 1
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/mcp-douban"]
