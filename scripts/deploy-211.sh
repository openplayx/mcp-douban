#!/usr/bin/env bash
# Build and (re)deploy a committed revision to the 211 test host.
#
#   scripts/deploy-211.sh [REF]          # default HEAD; must be committed
#   IMAGE_TAG=<tag> scripts/deploy-211.sh --rollback
#
# Independent Compose project "openplayx-douban" under ~/openplayx-douban on
# the host (releases/<tag>, deploy/); never touches other projects and never
# runs a global docker prune. The API token is generated on the host, written
# to deploy/.env (API_TOKENS) and deploy/app-token.txt (0600); it is never
# printed.
set -euo pipefail

HOST="${DEPLOY_HOST:-zyl@192.168.1.211}"
BIND="${BIND:-192.168.1.211}"
PORT="${PORT:-18810}"
REMOTE_DIR="${REMOTE_DIR:-openplayx-douban}"
PROJECT="openplayx-douban"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

remote() { ssh -o BatchMode=yes "$HOST" "$@"; }

if [[ "${1:-}" == "--rollback" ]]; then
  : "${IMAGE_TAG:?IMAGE_TAG=<previous tag> is required for rollback}"
  TAG="$IMAGE_TAG"
else
  REF="${1:-HEAD}"
  cd "$ROOT"
  if [[ -n "$(git status --porcelain --untracked-files=no)" && "$REF" == "HEAD" ]]; then
    echo "working tree has uncommitted changes; commit first" >&2
    exit 1
  fi
  COMMIT="$(git rev-parse --verify "$REF^{commit}")"
  TAG="${COMMIT:0:12}"
  ARCHIVE="$(mktemp -t mcp-douban.XXXXXX).tar.gz"
  trap 'rm -f "$ARCHIVE"' EXIT
  git archive --format=tar.gz -o "$ARCHIVE" "$COMMIT"
  remote "mkdir -p ~/$REMOTE_DIR/releases/$TAG ~/$REMOTE_DIR/deploy"
  scp -q "$ARCHIVE" "$HOST:$REMOTE_DIR/releases/$TAG/source.tar.gz"
  remote "cd ~/$REMOTE_DIR/releases/$TAG && tar xzf source.tar.gz && rm source.tar.gz \
    && sudo docker build -q --build-arg VERSION=$TAG \
       --label org.opencontainers.image.revision=$COMMIT \
       -t openplayx/mcp-douban:$TAG . \
    && cp deploy/compose.yaml ~/$REMOTE_DIR/deploy/compose.yaml"
fi

# First deployment: .env with a token generated on the host.
remote "set -e; cd ~/$REMOTE_DIR/deploy
  if [ ! -f .env ]; then
    umask 077
    token=\$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')
    printf '%s\n' \"\$token\" > app-token.txt
    printf 'API_TOKENS=%s\nPUBLIC_URL=http://$BIND:$PORT\nLOG_LEVEL=info\n' \"\$token\" > .env
  fi"

remote "cd ~/$REMOTE_DIR/deploy && sudo env MCP_DOUBAN_IMAGE=openplayx/mcp-douban:$TAG \
  BIND=$BIND PORT=$PORT docker compose -p $PROJECT -f compose.yaml up -d --no-build --remove-orphans \
  && echo $TAG > current-tag"

for _ in $(seq 1 30); do
  if curl -fsS "http://$BIND:$PORT/api/healthz" >/dev/null 2>&1; then
    echo "deployed tag=$TAG base_url=http://$BIND:$PORT mcp=http://$BIND:$PORT/api/mcp"
    exit 0
  fi
  sleep 1
done
echo "service did not become healthy; recent logs:" >&2
remote "sudo docker logs --tail 50 ${PROJECT}-mcp-douban-1" >&2
exit 1
