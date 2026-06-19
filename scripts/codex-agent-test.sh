#!/bin/sh
set -eu

BASE_URL=${BASE_URL:-http://127.0.0.1:8080}
CODEX_PROVIDER_MODE=${CODEX_PROVIDER_MODE:-local}
CODEX_LOCAL_PROVIDER=${CODEX_LOCAL_PROVIDER:-lmstudio}
CODEX_MODEL=${CODEX_MODEL:-}
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/tabucom-codex.XXXXXX")
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM

command -v codex >/dev/null 2>&1 || { printf 'codex CLI is required\n' >&2; exit 1; }
curl -fsS "$BASE_URL/" >/dev/null || { printf 'service unavailable: %s\n' "$BASE_URL" >&2; exit 1; }

marker="codex-agent-deployment-$(date +%s)"
curl -fsS "$BASE_URL/" > "$TMP_DIR/homepage.html"
prompt="The publishing service homepage is provided on stdin. Using only those instructions, specify the exact HTTP request for publishing a raw HTML page whose visible body contains the exact text '$marker'. Use the service origin $BASE_URL. Do not run commands or inspect files. Return only one JSON object with exactly these fields: method='POST', endpoint, contentType='text/html', body, spa=false, ttlHours=720. Do not use a Markdown code fence."

case "$CODEX_PROVIDER_MODE" in
  local) set -- --oss --local-provider "$CODEX_LOCAL_PROVIDER" ;;
  hosted) set -- ;;
  *) printf 'CODEX_PROVIDER_MODE must be local or hosted\n' >&2; exit 1 ;;
esac
if [ -n "$CODEX_MODEL" ]; then
  set -- "$@" --model "$CODEX_MODEL"
fi

(cd "$TMP_DIR" && codex exec \
  "$@" \
  --ephemeral \
  --skip-git-repo-check \
  --sandbox read-only \
  --ignore-user-config \
  --color never \
  --output-schema "$ROOT_DIR/testdata/codex-output-schema.json" \
  --output-last-message "$TMP_DIR/final.json" \
  "$prompt" < "$TMP_DIR/homepage.html") 2>&1 | tee "$TMP_DIR/codex-output.txt"

test -s "$TMP_DIR/final.json" || { printf 'Codex produced no structured final response\n' >&2; exit 1; }

python3 - "$TMP_DIR/final.json" "$TMP_DIR" "$BASE_URL" "$marker" <<'PY'
import json, pathlib, sys
raw = pathlib.Path(sys.argv[1]).read_text().strip()
try:
    result = json.loads(raw)
except json.JSONDecodeError:
    start, end = raw.find("{"), raw.rfind("}")
    if start < 0 or end < start:
        raise
    result = json.loads(raw[start:end + 1])
tmp = pathlib.Path(sys.argv[2])
base, marker = sys.argv[3], sys.argv[4]
assert result["method"] == "POST"
assert result["endpoint"] == f"{base}/api/v1/publish"
assert result["contentType"] == "text/html"
assert marker in result["body"]
assert result["spa"] is False
assert result["ttlHours"] == 720
(tmp / "request-body.html").write_text(result["body"])
PY

status=$(curl -sS -o "$TMP_DIR/publish.json" -w '%{http_code}' -X POST \
  "$BASE_URL/api/v1/publish" \
  -H 'Content-Type: text/html' \
  --data-binary "@$TMP_DIR/request-body.html")
[ "$status" = 201 ] || { printf 'Codex-derived request returned HTTP %s: %s\n' "$status" "$(cat "$TMP_DIR/publish.json")" >&2; exit 1; }

site_url=$(sed -n 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$TMP_DIR/publish.json" | head -n 1)
expires_at=$(sed -n 's/.*"expiresAt"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$TMP_DIR/publish.json" | head -n 1)
[ -n "$site_url" ] && [ -n "$expires_at" ] || { printf 'publish response is missing url or expiresAt\n' >&2; exit 1; }
curl -fsS "$site_url" | grep -q "$marker" || { printf 'published page does not contain the Codex marker\n' >&2; exit 1; }

printf 'Codex read the homepage, derived the API request, and produced a verified deployment: %s (expires %s)\n' "$site_url" "$expires_at"
