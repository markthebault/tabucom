# Tabucom: Share Your AI Thoughts with Impact

Tabucom is a small self-hosted service for publishing temporary static pages.

You send it HTML, Markdown, or a ZIP of an already-built website. It gives back
a URL you can share. The page is immutable, can have an expiry time, and is
deleted automatically later.

It is useful for:

- sharing an HTML report with a team
- asking an LLM to publish a quick preview
- hosting temporary documentation, dashboards, or mockups
- giving an organization a simple internal "paste and publish" service

Tabucom does not run uploaded code, install packages, build projects, or execute
server-side scripts. It only serves static files.

## Use With Coding Agents

Install the Tabucom skill in the [Agent Skills](https://agentskills.io) format
with [`npx skills`](https://github.com/vercel-labs/skills):

```sh
npx skills add markthebault/tabucom --skill tabucom
```

Set the Tabucom origin in your shell profile so coding agents can find the
service. Use the URL for your own Tabucom deployment:

```sh
echo 'export TABUCOM_BASE_URL="https://tabucom.example.com"' >> ~/.zshrc
source ~/.zshrc
```

If you use Bash instead:

```sh
echo 'export TABUCOM_BASE_URL="https://tabucom.example.com"' >> ~/.bashrc
source ~/.bashrc
```

Then ask your coding agent to publish a static artifact with Tabucom:

```text
$tabucom Publish /tmp/report.html and return the URL plus expiry.
```

The canonical skill lives in this repository at `skills/tabucom/SKILL.md`.

## Quick Start

Run Tabucom locally with Docker:

```sh
docker run --rm -p 8080:8080 \
  -e PUBLIC_API_URL=http://localhost:8080 \
  -v tabucom-data:/data \
  ghcr.io/markthebault/tabucom:latest
```

Open <http://localhost:8080>.

The named Docker volume keeps published pages across container restarts.

Publish a small HTML page:

```sh
curl -sS -X POST http://localhost:8080/api/v1/publish \
  -H 'Content-Type: text/html' \
  --data-binary '<!doctype html><title>Hello</title><h1>Hello from Tabucom</h1>'
```

The response contains the URL to open:

```json
{
  "id": "...",
  "url": "http://localhost:8080/p/.../",
  "createdAt": "...",
  "expiresAt": "...",
  "files": 1,
  "bytes": 62,
  "spa": false,
  "protected": false
}
```

## Run On A VPS

For a small organization, the usual setup is:

1. Run Tabucom on a VPS with Docker.
2. Put a reverse proxy in front of it, such as Caddy, Traefik, nginx, or your
   platform's ingress.
3. Set `PUBLIC_API_URL` to the public HTTPS URL users will open.
4. Enable publish tokens if people or LLMs outside a trusted network can reach
   the service.

Example:

```sh
docker run -d --name tabucom \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -e PUBLIC_API_URL=https://tabucom.example.com \
  -v tabucom-data:/data \
  ghcr.io/markthebault/tabucom:latest
```

Your reverse proxy should send `https://tabucom.example.com` traffic to
`http://127.0.0.1:8080`.

`PUBLIC_API_URL` matters because Tabucom uses it in API responses and in the
instructions copied from the home page. If you run locally, use
`http://localhost:8080`. If you run on a VPS, use your real HTTPS URL.

## Publish Tokens

By default, publishing is open to anyone who can reach the service. That is fine
for a private local machine or a trusted internal network.

If Tabucom is exposed on a VPS or used by many people, enable stateless publish
tokens:

```sh
docker run -d --name tabucom \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -e PUBLIC_API_URL=https://tabucom.example.com \
  -e STATELESS_PUBLISH_TOKENS_ENABLED=true \
  -e STATELESS_TOKEN_SIGNING_SECRET='change-this-to-a-long-random-secret-value' \
  -v tabucom-data:/data \
  ghcr.io/markthebault/tabucom:latest
```

When tokens are enabled:

- the home page shows a "Generate Publish Token" button
- a user can copy the token and give it to an LLM
- the LLM can then publish documents to Tabucom
- generated tokens are not stored in a database
- expired or invalid tokens are rejected

Use a signing secret of at least 32 characters. A password manager is a good way
to generate it.

If you publish with a script while tokens are enabled, include this header:

```sh
-H "Authorization: Bearer $TABUCOM_PUBLISH_TOKEN"
```

## Publishing Content

Tabucom accepts three upload types.

HTML:

```sh
curl -sS -X POST https://tabucom.example.com/api/v1/publish \
  -H 'Content-Type: text/html' \
  --data-binary @index.html
```

Markdown:

```sh
curl -sS -X POST https://tabucom.example.com/api/v1/publish \
  -H 'Content-Type: text/markdown' \
  --data-binary @report.md
```

ZIP of a built static site:

```sh
(cd dist && zip -qr ../site.zip .)

curl -sS -X POST 'https://tabucom.example.com/api/v1/publish?spa=1' \
  -H 'Content-Type: application/zip' \
  --data-binary @site.zip
```

The ZIP must contain `index.html` at its root:

```text
site.zip
|-- index.html
|-- assets/
|   |-- app.js
|   `-- app.css
`-- images/logo.svg
```

Do not upload project source code. Build first, then publish the output folder,
usually `dist/`, `build/`, `out/`, or `public/`.

## Useful Options

Set a shorter or longer lifetime:

```sh
curl -sS -X POST 'https://tabucom.example.com/api/v1/publish?ttl=72h' \
  -H 'Content-Type: text/html' \
  --data-binary @index.html
```

Enable single-page-app fallback for React, Vue, Angular, SvelteKit static sites,
or other client-side routers:

```sh
curl -sS -X POST 'https://tabucom.example.com/api/v1/publish?spa=1' \
  -H 'Content-Type: application/zip' \
  --data-binary @site.zip
```

Add a readable ID prefix:

```sh
curl -sS -X POST 'https://tabucom.example.com/api/v1/publish?prefix=my-super-website' \
  -H 'Content-Type: text/html' \
  --data-binary @index.html
```

The returned ID still ends with a random suffix, such as
`my-super-website-1a2b3c4d5e6f7890`, so repeated prefixes can coexist.

Add a visitor password:

```sh
curl -sS -X POST https://tabucom.example.com/api/v1/publish \
  -H 'Content-Type: text/html' \
  -H 'Tabucom-Password: correct horse' \
  --data-binary @index.html
```

Or let Tabucom generate one:

```sh
curl -sS -X POST 'https://tabucom.example.com/api/v1/publish?generatePassword=1' \
  -H 'Content-Type: text/html' \
  --data-binary @index.html
```

Visitor passwords protect the published page. They do not protect the publish
API. Use publish tokens for that.

## Copy Page HTML

Published HTML pages include a small floating "Copy Page" button for browser
handoff to an AI agent. The button copies the resolved stored HTML file, not the
button chrome itself.

## Releases

Tabucom uses GitHub Actions and Release Please for automated changelogs, tags,
GitHub Releases, snapshot binaries, release binaries, and GHCR container images.
See [docs/releases.md](docs/releases.md) for the release flow and commit message
rules.

You can also fetch the same byte-exact source directly by adding `raw=1` to a
published URL:

```sh
curl -fsS 'https://tabucom.example.com/p/{id}/?raw=1'
```

`raw=1` follows the same expiry, SPA fallback, and visitor-password rules as
normal page viewing. For non-HTML assets it simply serves the resolved file
without adding any browser copy control. Pages with restrictive Content Security
Policy may block the floating button; `raw=1` remains available.

## Use With An LLM

Open the Tabucom home page and click "Copy setup instructions for my agent".

If publish tokens are enabled, first click "Generate Publish Token", copy the
token, and give it to the LLM when you ask it to publish.

The LLM instructions tell the agent to:

1. publish only built static output
2. avoid source code, secrets, `.env`, `.git`, and `node_modules`
3. use the URL served by your Tabucom instance
4. return only the published `url`, `expiresAt`, `protected`, and password when
   one is returned

The same instructions are also available at:

- `/llms.txt`
- `/agents`
- `/.well-known/agent.json`
- `/openapi.json`

## Configuration

Most installations only need `PUBLIC_API_URL`, `DATA_DIR`, and optionally publish
tokens.

| Variable | Default | What it does |
| --- | --- | --- |
| `PUBLIC_API_URL` | request origin | Public URL used in responses and generated instructions |
| `DATA_DIR` | `./data` | Local storage directory for deployments |
| `TTL` | `720h` | Default lifetime when a request does not set `ttl` |
| `STATELESS_PUBLISH_TOKENS_ENABLED` | `false` | Require a publish token for `POST /api/v1/publish` |
| `STATELESS_TOKEN_SIGNING_SECRET` | unset | Secret used to sign publish tokens |
| `PREVIEW_DOMAIN` | empty | Optional wildcard domain for isolated preview URLs |
| `MAX_UPLOAD_BYTES` | `104857600` | Maximum upload size |
| `MAX_EXPANDED_BYTES` | `524288000` | Maximum expanded ZIP size |
| `MAX_FILES` | `10000` | Maximum number of ZIP entries |
| `RATE_LIMIT_PER_HOUR` | `60` | Publish requests allowed per network peer |

S3-compatible storage is optional:

| Variable | Default | What it does |
| --- | --- | --- |
| `S3_BUCKET` | unset | Enables S3-compatible storage when set |
| `S3_ENDPOINT` | AWS default | Optional endpoint for R2, RustFS, MinIO, or another S3-compatible service |
| `S3_REGION` | `us-east-1` | Signing region; use `auto` for Cloudflare R2 |
| `S3_PREFIX` | unset | Optional object-key prefix |
| `S3_PATH_STYLE` | `false` | Use path-style bucket URLs |

If `S3_BUCKET` is not set, Tabucom uses local Docker volume storage.

## Optional Preview Subdomains

By default, deployments are served under the main domain:

```text
https://tabucom.example.com/p/{id}/
```

You can also configure a wildcard preview domain:

```text
https://{id}.preview.example.com/
```

This gives each deployment its own browser origin, which is better for untrusted
HTML and JavaScript.

To use it, point both the preview domain and its wildcard to the Tabucom server:

```dns
preview.example.com    A      192.0.2.10
*.preview.example.com  CNAME  preview.example.com.
```

Then configure:

```env
PUBLIC_API_URL=https://preview.example.com
PREVIEW_DOMAIN=preview.example.com
```

Your reverse proxy must route both `preview.example.com` and
`*.preview.example.com` to Tabucom. TLS must cover both names.

## Safety Notes

- Put Tabucom behind HTTPS before using passwords or publish tokens.
- Enable publish tokens when the publish API is reachable outside a trusted
  network.
- Published JavaScript runs in visitors' browsers.
- Path-mode deployments share the same browser origin as Tabucom itself.
- Use `PREVIEW_DOMAIN` for stronger browser isolation.
- ZIP uploads are checked for traversal, symlinks, duplicate paths, unsafe file
  types, missing root `index.html`, and archive expansion limits.

## Development

Requires Go 1.23 or newer.

```sh
go run ./cmd/tabucom
make check
make test
```

Useful local Docker commands:

```sh
make run
make run-tokens
make run-preview
```

Build from source with Docker Compose:

```sh
docker compose up --build
```

Read [the architecture guide](docs/architecture.md) for the request lifecycle,
storage model, and security boundaries. Contributor and integration-test
commands are in [AGENTS.md](AGENTS.md).
