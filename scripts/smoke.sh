#!/usr/bin/env bash
# Live check against real Douban through a running instance.
# Usage: scripts/smoke.sh [base_url]   (TOKEN=... if API_TOKENS is set)
# Fails when Douban changed its endpoints, refuses this host, or returns nothing.
set -euo pipefail
base="${1:-http://127.0.0.1:8800}"
auth=()
[ -n "${TOKEN:-}" ] && auth=(-H "Authorization: Bearer ${TOKEN}")
fail=0
check() { # name path python-expression-on-d
  local body
  if ! body="$(curl -fsS -m 30 "${auth[@]}" "$base$2")"; then
    echo "FAIL $1 (HTTP error)"; fail=1; return
  fi
  if python3 -c "import json,sys; d=json.loads(sys.argv[1]); sys.exit(0 if ($3) else 1)" "$body"; then
    echo "ok   $1"
  else
    echo "FAIL $1: ${body:0:300}"; fail=1
  fi
}
check healthz      /api/healthz                                   "d['status']=='ok'"
for c in movie_hot movie_latest movie_top movie_underrated tv variety; do
  check "chart $c" "/api/v1/charts/$c?limit=3"                    "len(d['items'])>0 and d['total']>0"
done
check search       "/api/v1/search?q=%E8%82%96%E7%94%B3%E5%85%8B" "any(i['id']=='1292052' for i in d['items'])"
check subject      /api/v1/subjects/1292052                       "d['title']=='肖申克的救赎' and d['rating']['value']>9"
check credits      /api/v1/subjects/1292052/credits               "len(d['actors'])>10"
poster="$(curl -fsS -m 30 "${auth[@]}" "$base/api/v1/subjects/1292052" | python3 -c "import json,sys; print(json.load(sys.stdin)['poster_proxy'])")"
case "$poster" in http*) url="$poster" ;; *) url="$base$poster" ;; esac
ct="$(curl -fsS -m 30 -o /dev/null -w '%{content_type}' "$url" || true)"
if [[ "$ct" == image/* ]]; then echo "ok   image proxy"; else echo "FAIL image proxy ($ct)"; fail=1; fi
exit $fail
