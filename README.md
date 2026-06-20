# Tabucom

A single-container internal service for publishing HTML, Markdown, or complete prebuilt static sites at temporary URLs. Every immutable deployment expires at the client-requested TTL, or after the default 30-day retention window when omitted. There are no accounts, update operations, or application-level authentication.

The service hosts static output only. Build a Node project locally or in CI and upload its `dist/` or `build/` output; the server never runs npm, Node.js, SSR, or uploaded code.

This is designed for internal use behind network controls. Operators should put the publish host behind VPN, SSO, or another trusted ingress layer before exposing it to humans or agents.

## Run locally

With Go installed:

```sh
go run ./cmd/tabucom
```

Then open <http://localhost:8080/> or check readiness:

```sh
curl -fsS http://localhost:8080/healthz
```

Local data is stored under the configured `DATA_DIR`. Use a disposable directory for development.

## Run with Docker

Build and start the one-container service with a persistent volume:

```sh
docker build -t tabucom .
docker run --rm -p 8080:8080 \
  -e PUBLIC_API_URL=http://localhost:8080 \
  -v tabucom-data:/data \
  tabucom
```

The Makefile defaults to `markthebault/tabucom:latest`. Build or push that image with:

```sh
make docker-build
make docker-push
```

Override the repository or tag when needed:

```sh
make docker-push IMAGE_REPOSITORY=markthebault/tabucom IMAGE_TAG=v1.0.0
```

Pushes to `main` run the same build and push through GitHub Actions after tests pass. Configure these repository settings:

- Variable `DOCKER_IMAGE_REPOSITORY` (defaults to `markthebault/tabucom`)
- Variable `DOCKER_IMAGE_TAG` (defaults to `latest`)
- Secret `DOCKERHUB_USERNAME`
- Secret `DOCKERHUB_TOKEN` containing a Docker Hub access token with push access

In production, terminate TLS at the company ingress and mount persistent storage at `/data`. Set `PUBLIC_API_URL` to the externally reachable publish origin so returned deployment URLs do not depend on proxy headers. Restrict the publish host to the corporate network or VPN. If wildcard DNS/TLS is configured, route both the publish host and `*.$PREVIEW_DOMAIN` to this container.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port (overrides `LISTEN_ADDR`). |
| `LISTEN_ADDR` | `:8080` | Full listen address when `PORT` is unset. |
| `DATA_DIR` | `./data` (`/data` in container) | Persistent sites, metadata, and temporary uploads. |
| `TTL` | `720h` | Default deployment retention when the publish request omits `ttl`. |
| `SWEEP_INTERVAL` | `1h` | How often expired deployments are removed. |
| `PUBLIC_API_URL` | Request origin | External publishing origin used in links and responses. |
| `PREVIEW_DOMAIN` | empty | Optional wildcard preview domain. Empty selects `/p/{id}/` path URLs. |
| `MAX_UPLOAD_BYTES` | `104857600` | Maximum compressed request size (100 MB). |
| `MAX_EXPANDED_BYTES` | `524288000` | Maximum extracted ZIP size (500 MB). |
| `MAX_FILES` | `10000` | Maximum number of ZIP entries, including directories. |
| `RATE_LIMIT_PER_HOUR` | `60` | Maximum publish requests per network peer per hour. |

Clients can override deployment lifetime per request with `?ttl=<duration>`. When omitted, the service default remains `720h`. See the landing page and `internal/server/web/openapi.json` for entry-count and rate limits and the full API contract.

## Publish

HTML:

```sh
curl -sS -X POST http://localhost:8080/api/v1/publish \
  -H 'Content-Type: text/html; charset=utf-8' \
  --data-binary @index.html
```

Markdown:

```sh
curl -sS -X POST http://localhost:8080/api/v1/publish \
  -H 'Content-Type: text/markdown; charset=utf-8' \
  --data-binary @report.md
```

A Node-generated static site:

```sh
npm ci
npm run build
(cd dist && zip -qr ../site.zip .)
curl -sS -X POST 'http://localhost:8080/api/v1/publish?spa=1' \
  -H 'Content-Type: application/zip' \
  --data-binary @site.zip
```

To choose a custom lifetime, add a `ttl` query parameter with a positive duration such as `?ttl=72h` or `?spa=1&ttl=168h`. The ZIP must have a regular `index.html` file at its root. `spa=1` makes unknown paths fall back to `index.html`; omit it for normal static 404 behavior. Always use the `url` returned in the `201` JSON response and report both `url` and `expiresAt` to the user.

ZIP extraction is defensive. Archives with traversal, absolute paths, duplicate normalized paths, conflicting file and directory paths, symlinks, device files, excessive nesting, or size and entry-count overages are rejected.

## Discovery

- `/` — minimal human-facing landing page
- `/agents` — complete human- and agent-readable usage guide
- `/openapi.json` — OpenAPI 3.1 contract
- `/llms.txt` — compact agent instructions
- `/.well-known/agent.json` — structured discovery metadata
- `/healthz` — readiness check

## Security Model

Uploaded HTML, CSS, and JavaScript run in the viewer's browser. In path mode, deployments under `/p/{id}/` share the same browser origin as the service landing page and API. That means untrusted content can interact with same-origin browser storage for that host. If you need origin isolation or root-relative asset paths, configure `PREVIEW_DOMAIN` and serve deployments from wildcard subdomains instead.

## Development

```sh
make check
```

Run the black-box suite against a running server:

```sh
BASE_URL=http://127.0.0.1:8080 ./scripts/integration-test.sh
```

The final agent-discovery test uses Codex with a local OSS provider. It verifies the landing page's agent link, gives a fresh, read-only Codex session only the rendered `/agents` guide, asks it to derive the publish request, then executes that request in the trusted harness and verifies the returned site URL:

```sh
lms server start
lms load <local-model-key> --identifier codex-local -y
BASE_URL=http://127.0.0.1:8080 \
  CODEX_LOCAL_PROVIDER=lmstudio \
  CODEX_MODEL=codex-local \
  ./scripts/codex-agent-test.sh
```

The default local mode does not send the internal agent guide to a hosted model.

To use an explicitly approved hosted endpoint instead, export its credentials without printing them and select hosted mode:

```sh
set -a; . ./.env; set +a
BASE_URL=http://127.0.0.1:8080 \
  CODEX_PROVIDER_MODE=hosted \
  CODEX_MODEL=gpt-5.4 \
  ./scripts/codex-agent-test.sh
```

Operational and contributor commands are kept in [AGENTS.md](./AGENTS.md).
