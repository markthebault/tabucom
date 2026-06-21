#!/bin/sh
set -eu

BASE_URL=${BASE_URL:-http://127.0.0.1:8080}
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/tabucom-test.XXXXXX")
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'ok - %s\n' "$*"; }
json_string() {
  key=$1
  sed -n 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}
publish() {
  type=$1
  file=$2
  query=${3:-}
  output=$4
  status=$(curl -sS -o "$output" -w '%{http_code}' -X POST \
    -H "Content-Type: $type" --data-binary "@$file" "$BASE_URL/api/v1/publish$query")
  [ "$status" = 201 ] || fail "publish returned HTTP $status: $(cat "$output")"
}
assert_url() {
  url=$1
  case "$url" in http://*|https://*) ;; *) fail "response URL is not absolute: $url" ;; esac
}

curl -fsS "$BASE_URL/healthz" >/dev/null || fail "health endpoint is unavailable at $BASE_URL"
pass health
curl -fsS "$BASE_URL/" | grep -q 'href="/agents"' || fail "homepage does not link to agent instructions"
curl -fsS "$BASE_URL/agents" | grep -q '/api/v1/publish' || fail "agent guide does not document the publish endpoint"
curl -fsS "$BASE_URL/openapi.json" | grep -q 'openapi' || fail "OpenAPI discovery document missing"
curl -fsS "$BASE_URL/llms.txt" | grep -q '/api/v1/publish' || fail "llms.txt discovery document missing"
curl -fsS "$BASE_URL/.well-known/agent.json" | grep -q 'publish' || fail "agent discovery document missing"
pass 'agent discovery documents'

publish 'text/html; charset=utf-8' "$ROOT_DIR/testdata/page.html" '' "$TMP_DIR/html.json"
html_url=$(json_string url < "$TMP_DIR/html.json")
html_expiry=$(json_string expiresAt < "$TMP_DIR/html.json")
[ -n "$html_url" ] || fail "HTML response has no url"
[ -n "$html_expiry" ] || fail "HTML response has no expiresAt"
assert_url "$html_url"
curl -fsS "$html_url" | grep -q 'raw-html-published' || fail "published HTML marker missing"
pass 'raw HTML publish and fetch'

protected_status=$(curl -sS -o "$TMP_DIR/protected.json" -w '%{http_code}' -X POST \
  -H 'Content-Type: text/html' -H 'Tabucom-Password: integration-password' \
  --data-binary "@$ROOT_DIR/testdata/page.html" "$BASE_URL/api/v1/publish")
[ "$protected_status" = 201 ] || fail "protected publish returned HTTP $protected_status"
protected_url=$(json_string url < "$TMP_DIR/protected.json")
protected_password=$(json_string password < "$TMP_DIR/protected.json")
[ "$protected_password" = integration-password ] || fail 'protected response omitted the password'
locked_status=$(curl -sS -o "$TMP_DIR/password-form.html" -w '%{http_code}' "$protected_url")
[ "$locked_status" = 401 ] || fail "protected URL returned HTTP $locked_status before login"
login_status=$(curl -sS -o /dev/null -c "$TMP_DIR/cookies.txt" -w '%{http_code}' \
  -H 'Content-Type: application/x-www-form-urlencoded' --data 'password=integration-password' "$protected_url")
[ "$login_status" = 303 ] || fail "password login returned HTTP $login_status"
curl -fsS -b "$TMP_DIR/cookies.txt" "$protected_url" | grep -q 'raw-html-published' || fail 'unlocked page marker missing'
pass 'password-protected publish and unlock'

publish 'text/markdown; charset=utf-8' "$ROOT_DIR/testdata/report.md" '' "$TMP_DIR/markdown.json"
markdown_url=$(json_string url < "$TMP_DIR/markdown.json")
[ -n "$markdown_url" ] || fail "Markdown response has no url"
assert_url "$markdown_url"
curl -fsS "$markdown_url" | grep -q 'markdown-rendered-marker' || fail "rendered Markdown marker missing"
pass 'Markdown publish and render'

command -v zip >/dev/null 2>&1 || fail "zip is required for the SPA test"
(cd "$ROOT_DIR/testdata/spa" && zip -q -r "$TMP_DIR/spa.zip" .)
publish application/zip "$TMP_DIR/spa.zip" '?spa=1' "$TMP_DIR/spa.json"
spa_url=$(json_string url < "$TMP_DIR/spa.json")
spa_expiry=$(json_string expiresAt < "$TMP_DIR/spa.json")
[ -n "$spa_url" ] || fail "ZIP response has no url"
[ -n "$spa_expiry" ] || fail "ZIP response has no expiresAt"
assert_url "$spa_url"
curl -fsS "$spa_url" | grep -q 'spa-shell-loaded' || fail "SPA entrypoint missing"
curl -fsS "${spa_url%/}/assets/app.js" | grep -q 'dataset.ready' || fail "SPA JavaScript asset missing"
curl -fsS "${spa_url%/}/assets/pixel.svg" | grep -q '<svg' || fail "SPA nested asset missing"
curl -fsS "${spa_url%/}/a/client/side/route" | grep -q 'spa-shell-loaded' || fail "SPA fallback route missing"
pass 'ZIP assets and SPA fallback'

mkdir "$TMP_DIR/traversal-source"
printf 'escape-test\n' > "$TMP_DIR/escape.txt"
(cd "$TMP_DIR/traversal-source" && zip -q "$TMP_DIR/traversal.zip" ../escape.txt)
traversal_status=$(curl -sS -o "$TMP_DIR/traversal.json" -w '%{http_code}' -X POST \
  -H 'Content-Type: application/zip' --data-binary "@$TMP_DIR/traversal.zip" \
  "$BASE_URL/api/v1/publish")
case "$traversal_status" in 400|422) pass 'ZIP traversal rejected' ;; *) fail "traversal ZIP returned HTTP $traversal_status" ;; esac

# Start the service with a short TTL and set this to its TTL in seconds.
if [ -n "${EXPECT_EXPIRY_SECONDS:-}" ]; then
  sleep "$((EXPECT_EXPIRY_SECONDS + ${EXPIRY_GRACE_SECONDS:-3}))"
  expiry_status=$(curl -sS -o /dev/null -w '%{http_code}' "$html_url")
  case "$expiry_status" in 404|410) pass 'expired deployment removed' ;; *) fail "expired URL returned HTTP $expiry_status" ;; esac
fi

printf 'All integration checks passed.\n'
