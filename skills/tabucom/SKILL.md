---
name: tabucom
description: Use Tabucom to publish temporary static HTML, Markdown, or already-built static ZIP artifacts for sharing with a user or another agent. Trigger when asked to publish a page, report, plan, spec, deck export, static app preview, or one-off HTML artifact to Tabucom.
---

# Tabucom Publishing

Use Tabucom when an agent needs to publish an immutable temporary static artifact and return a shareable deployment URL plus its expiry.

This skill intentionally does not include a fixed Tabucom URL, token, or security policy. The deployment origin and authentication settings depend on the environment. Discover them from the user, local docs, environment variables, or the page contract provided in the current task.

## Required Inputs

Before publishing, identify:

- `TABUCOM_BASE_URL`: the origin of the Tabucom service, without a trailing slash.
- Optional `TABUCOM_PUBLISH_TOKEN`: bearer token for services that require publish tokens.
- The artifact type: raw HTML, Markdown, or a ZIP of already-built static files.
- Optional publish settings: `ttl`, `spa`, `prefix`, `generatePassword`, or a visitor password.

If the base URL or token is unknown, ask for it or use an explicit value already present in the task context. Do not guess a production URL.

When publish tokens are enabled, add this header to the `curl` commands:

```sh
-H "Authorization: Bearer $TABUCOM_PUBLISH_TOKEN"
```

## Safety Rules

- Publish only static artifacts.
- Do not upload project source when a built output directory is required.
- Do not run package-manager or build commands inside Tabucom; build locally first if needed.
- ZIP uploads must contain `index.html` at the ZIP root.
- Treat every deployment as immutable: publish a new deployment for changes.
- Always return both `url` and `expiresAt` from the publish response.
- If Tabucom returns a visitor `password`, return it with the URL and expiry.

## Publish Raw HTML

Use `text/html` for complete HTML files:

```sh
curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish" \
  -H 'Content-Type: text/html' \
  --data-binary @/absolute/path/to/page.html
```

With a TTL:

```sh
curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish?ttl=72h" \
  -H 'Content-Type: text/html' \
  --data-binary @/absolute/path/to/page.html
```

## Publish Markdown

Use `text/markdown` when Tabucom should render a Markdown document:

```sh
curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish" \
  -H 'Content-Type: text/markdown' \
  --data-binary @/absolute/path/to/report.md
```

## Publish A Built Static Site

Build the site locally with the project's normal command, then ZIP the built output directory. The ZIP root must contain `index.html`.

```sh
(cd /absolute/path/to/dist && zip -qr /tmp/site.zip .)

curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish" \
  -H 'Content-Type: application/zip' \
  --data-binary @/tmp/site.zip
```

For a client-side-routed single-page app, enable SPA fallback:

```sh
curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish?spa=1" \
  -H 'Content-Type: application/zip' \
  --data-binary @/tmp/site.zip
```

## Optional Publish Settings

Use query parameters for publish behavior:

```text
ttl=72h
spa=1
prefix=my-readable-prefix
generatePassword=1
```

Use `Tabucom-Password` when the user provides a visitor password for the published page:

```sh
curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish?ttl=72h" \
  -H 'Content-Type: text/html' \
  -H "Tabucom-Password: $TABUCOM_VISITOR_PASSWORD" \
  --data-binary @/absolute/path/to/page.html
```

## Parse And Return The Response

Successful responses are JSON. Extract at least:

```json
{
  "url": "https://example.invalid/p/id/",
  "expiresAt": "2026-08-07T12:00:00Z"
}
```

Report these fields to the user:

```text
URL: <response.url>
Expires: <response.expiresAt>
```

Also include `password` if present, because generated visitor passwords are only visible in the publish response.

## Verify The Published Deployment

After publishing, verify the returned URL itself:

```sh
published_url="$(jq -r '.url' /tmp/tabucom-response.json)"
curl -fsS "$published_url" >/dev/null
```

If `jq` is unavailable, use another JSON parser rather than copying fields by eye. If the deployment is password-protected, verify that the URL responds with the expected password page or challenge instead of assuming the upload response is enough.

## Local Development Commands

Run a local Tabucom server:

```sh
go run ./cmd/tabucom
```

Run project checks:

```sh
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
python3 -m json.tool internal/server/web/openapi.json >/dev/null
python3 -m json.tool internal/server/web/.well-known/agent.json >/dev/null
```

Smoke-test all accepted input forms against a running local server:

```sh
printf '<!doctype html><title>smoke</title><h1>ok</h1>' > /tmp/site.html
printf '# smoke\n\nMarkdown works.\n' > /tmp/site.md
curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish" -H 'Content-Type: text/html' --data-binary @/tmp/site.html
curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish" -H 'Content-Type: text/markdown' --data-binary @/tmp/site.md
mkdir -p /tmp/static-site && cp /tmp/site.html /tmp/static-site/index.html
(cd /tmp/static-site && zip -qr /tmp/static-site.zip .)
curl -fsS -X POST "$TABUCOM_BASE_URL/api/v1/publish?spa=1" -H 'Content-Type: application/zip' --data-binary @/tmp/static-site.zip
```
