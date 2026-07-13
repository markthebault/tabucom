#!/usr/bin/env bash
# Publish a single reviewed static artifact to a configured Tabucom origin.
#
# This helper is intentionally an upload client, not a build tool. It resolves
# the operator-controlled origin from the environment or local config, validates
# it before credentials are considered, and sends authentication only to that
# exact origin. Node is used only for safe JSON and URL handling; it is already
# required by the npx-based skill installer. The script writes no credentials,
# and its temporary response file is private and deleted on exit.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: publish.sh <artifact.html|artifact.md|site.zip> [options]

Options:
  --ttl <duration>          Deployment lifetime, for example 72h
  --spa                     Enable single-page-app fallback (ZIP only)
  --prefix <value>          Deployment ID prefix
  --generate-password       Ask Tabucom to generate a visitor password
  --visitor-password        Send TABUCOM_VISITOR_PASSWORD as a visitor password
  --no-verify               Do not fetch the returned public URL

Credentials are read only from TABUCOM_PUBLISH_API_KEY,
TABUCOM_PUBLISH_TOKEN, and TABUCOM_VISITOR_PASSWORD. They are never saved.
EOF
}

die() { printf '%s\n' "tabucom publish: $*" >&2; exit 1; }

require_node() { command -v node >/dev/null 2>&1 || die 'Node.js is required to read the local Tabucom configuration.'; }

config_path() {
  local config_home="${XDG_CONFIG_HOME:-${HOME:-}/.config}"
  [[ -n "$config_home" && "$config_home" != '/.config' ]] || die 'HOME or XDG_CONFIG_HOME must be set.'
  printf '%s/tabucom/config.json' "$config_home"
}

read_config_base_url() {
  local file="$1"
  [[ -f "$file" ]] || return 1
  require_node
  node - "$file" <<'NODE'
const fs = require('node:fs');
try {
  const config = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
  if (!config || typeof config.baseUrl !== 'string') process.exit(1);
  process.stdout.write(config.baseUrl);
} catch {
  process.exit(1);
}
NODE
}

normalize_origin() {
  require_node
  node - "$1" <<'NODE'
const value = process.argv[2];
try {
  const url = new URL(value);
  const isLocalHTTP = url.protocol === 'http:' &&
    ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  if (!value || url.username || url.password || url.pathname !== '/' || url.search || url.hash ||
      (url.protocol !== 'https:' && !isLocalHTTP)) {
    throw new Error();
  }
  process.stdout.write(url.origin);
} catch {
  process.exit(1);
}
NODE
}

query_encode() {
  require_node
  node -e 'process.stdout.write(encodeURIComponent(process.argv[1]))' "$1"
}

[[ $# -gt 0 ]] || { usage >&2; exit 1; }
artifact="$1"
shift
[[ -f "$artifact" ]] || die "artifact does not exist: $artifact"

ttl=''
spa=false
prefix=''
generate_password=false
visitor_password=false
verify=true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ttl) [[ $# -ge 2 ]] || die '--ttl requires a value'; ttl="$2"; shift 2 ;;
    --spa) spa=true; shift ;;
    --prefix) [[ $# -ge 2 ]] || die '--prefix requires a value'; prefix="$2"; shift 2 ;;
    --generate-password) generate_password=true; shift ;;
    --visitor-password) visitor_password=true; shift ;;
    --no-verify) verify=false; shift ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

lower_artifact="$(printf '%s' "$artifact" | tr '[:upper:]' '[:lower:]')"
case "$lower_artifact" in
  *.html|*.htm) content_type='text/html' ;;
  *.md|*.markdown) content_type='text/markdown' ;;
  *.zip) content_type='application/zip' ;;
  *) die 'artifact must be an HTML, Markdown, or ZIP file' ;;
esac
[[ "$spa" == false || "$content_type" == 'application/zip' ]] || die '--spa is valid only for ZIP uploads'

raw_base_url="${TABUCOM_BASE_URL:-}"
if [[ -z "$raw_base_url" ]]; then
  raw_base_url="$(read_config_base_url "$(config_path)")" || die 'Tabucom is not configured. Run `npx @tabucom/skill configure --base-url <url>` or set TABUCOM_BASE_URL.'
fi
base_url="$(normalize_origin "$raw_base_url")" || die 'Refusing untrusted or insecure TABUCOM_BASE_URL; it must be an HTTPS origin or local loopback HTTP origin.'

query=()
[[ -n "$ttl" ]] && query+=("ttl=$(query_encode "$ttl")")
[[ "$spa" == true ]] && query+=('spa=1')
[[ -n "$prefix" ]] && query+=("prefix=$(query_encode "$prefix")")
[[ "$generate_password" == true ]] && query+=('generatePassword=1')
publish_url="$base_url/api/v1/publish"
if [[ ${#query[@]} -gt 0 ]]; then
  publish_url+="?$(IFS='&'; printf '%s' "${query[*]}")"
fi

headers=(-H "Content-Type: $content_type")
credentials_present=false
if [[ -n "${TABUCOM_PUBLISH_API_KEY:-}" ]]; then
  headers+=(-H "X-API-Key: $TABUCOM_PUBLISH_API_KEY")
  credentials_present=true
fi
if [[ -n "${TABUCOM_PUBLISH_TOKEN:-}" ]]; then
  headers+=(-H "Authorization: Bearer $TABUCOM_PUBLISH_TOKEN")
  credentials_present=true
fi
if [[ "$visitor_password" == true ]]; then
  [[ -n "${TABUCOM_VISITOR_PASSWORD:-}" ]] || die '--visitor-password requires TABUCOM_VISITOR_PASSWORD in the environment'
  headers+=(-H "Tabucom-Password: $TABUCOM_VISITOR_PASSWORD")
  credentials_present=true
fi

response_file="$(mktemp -t tabucom-publish.XXXXXX)"
chmod 600 "$response_file" 2>/dev/null || true
trap 'rm -f "$response_file"' EXIT
curl_args=(-fsS --max-redirs 0 -X POST "$publish_url" "${headers[@]}" --data-binary "@$artifact" -o "$response_file")
# curl does not follow redirects without -L; --max-redirs makes that explicit
# when headers contain credentials and documents the intended trust boundary.
[[ "$credentials_present" == true ]] && curl_args+=(--max-redirs 0)
curl "${curl_args[@]}"

require_node
response_fields="$(node - "$response_file" "$generate_password" <<'NODE'
const fs = require('node:fs');
try {
  const body = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
  const generatedPassword = process.argv[3] === 'true';
  if (!body || typeof body.url !== 'string' || typeof body.expiresAt !== 'string') throw new Error();
  process.stdout.write(JSON.stringify({
    url: body.url,
    expiresAt: body.expiresAt,
    protected: Boolean(body.protected),
    ...(generatedPassword && typeof body.password === 'string' ? { password: body.password } : {}),
  }));
} catch {
  process.exit(1);
}
NODE
)" || die 'Tabucom returned an invalid publish response'

if [[ "$verify" == true ]]; then
  published_url="$(node -e 'process.stdout.write(JSON.parse(process.argv[1]).url)' "$response_fields")"
  protected="$(node -e 'process.stdout.write(String(JSON.parse(process.argv[1]).protected))' "$response_fields")"
  # Protected deployments deliberately challenge unauthenticated visitors, so a
  # successful public fetch is only expected for unprotected deployment URLs.
  if [[ "$protected" == false ]]; then
    curl -fsS --max-redirs 0 "$published_url" >/dev/null || die 'Published URL did not verify; the upload response was not accepted as sufficient.'
  fi
fi

# This is the caller-facing response. It contains no API key, publish token, or
# caller-supplied visitor password. A generated visitor password is returned only
# when it was explicitly requested and Tabucom included it in the response.
printf '%s\n' "$response_fields"
