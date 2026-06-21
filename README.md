# Tabucom

Temporary static hosting in one small container. Publish HTML, Markdown, or a prebuilt ZIP and get an immutable URL that expires automatically—no accounts required.

Tabucom is intended for trusted internal networks. It serves uploaded files but never executes them or runs build commands.

## Features

- One HTTP API and one persistent volume
- HTML, Markdown, and prebuilt static-site ZIP uploads
- Per-deployment TTLs with a 30-day default
- Optional generated or custom single-password protection
- Optional SPA fallback and wildcard subdomains
- Defensive ZIP extraction and configurable resource limits
- OpenAPI and agent-discovery endpoints included

## Usage

Run the published image without cloning the repository:

```sh
docker run --rm -p 8080:8080 \
  -e PUBLIC_API_URL=http://localhost:8080 \
  -v tabucom-data:/data \
  ghcr.io/markthebault/tabucom:latest
```

Tabucom is now available at <http://localhost:8080>. The named volume keeps deployments across container restarts.

To build the image from source instead:

```sh
docker compose up --build
```

Publish a page:

```sh
curl -sS -X POST http://localhost:8080/api/v1/publish \
  -H 'Content-Type: text/html' \
  --data-binary '<!doctype html><title>Hello</title><h1>Hello from Tabucom</h1>'
```

The `201` response includes the deployment's immutable `url`, `expiresAt`, and protection state:

```json
{
  "id": "…",
  "createdAt": "…",
  "expiresAt": "…",
  "files": 1,
  "bytes": 62,
  "protected": false,
  "spa": false,
  "url": "http://localhost:8080/p/…/"
}
```

Use `Content-Type: text/markdown` for Markdown, or upload an `application/zip` archive containing `index.html` at its root. Publish only the final static artifact that a browser can serve directly. Do not upload project source files. Add `?spa=1` for client-side routing and `?ttl=72h` for a custom lifetime.

Add `-H 'Tabucom-Password: correct horse'` to set a custom 8–128 character printable ASCII password, or add `?generatePassword=1` to generate one. Protected responses include `"protected": true` and `"password"`; visitors enter that password in Tabucom's form and remain unlocked until expiry. Do not combine the two options. Use HTTPS in production so passwords and cookies are encrypted in transit.

## Agent execution rules

1. Determine the publish origin from the user, environment, or project config. Prefer `TABUCOM_ORIGIN` when available.
2. Always publish built static output only. Common build directories are `dist/`, `build/`, `out/`, and `public/`.
3. If no build output exists, run the project build command only when obvious, such as `npm run build`, `pnpm build`, or `yarn build`.
4. For ZIP publishing, zip the contents of the build directory rather than the directory itself, ensure `index.html` is at the ZIP root, and use `spa=1` for React, Vue, Vite, SvelteKit static, Angular, or other client-side routers.
5. Never upload source code directories, `node_modules`, `.git`, secrets, `.env`, or backend/server code.
6. On success, return only `url`, `expiresAt`, `password` if returned, and `protected`.
7. On failure, read the HTTP status and response body, explain the cause briefly, and suggest the smallest correction.

## Configuration

The most commonly used environment variables are:

| Variable | Default | Description |
| --- | --- | --- |
| `PUBLIC_API_URL` | request origin | Public URL used in API responses |
| `DATA_DIR` | `./data` | Local deployment storage and upload staging directory |
| `S3_BUCKET` | unset | Enables S3-compatible storage when set; otherwise `DATA_DIR` is used |
| `S3_ENDPOINT` | AWS default | Optional endpoint for Cloudflare R2, RustFS, or another S3-compatible service |
| `S3_REGION` | `us-east-1` | Signing region; use `auto` for Cloudflare R2 |
| `S3_PREFIX` | unset | Optional object-key prefix |
| `S3_PATH_STYLE` | `false` | Use path-style bucket URLs; commonly required by local endpoints |
| `TTL` | `720h` | Default deployment lifetime |
| `PREVIEW_DOMAIN` | empty | Wildcard domain for isolated preview origins |
| `MAX_UPLOAD_BYTES` | `104857600` | Maximum request size |
| `MAX_EXPANDED_BYTES` | `524288000` | Maximum expanded ZIP size |

See the hosted `/agents` guide or `/openapi.json` endpoint for the complete API and configuration limits.

Set `S3_BUCKET` to store deployments in AWS S3, Cloudflare R2, RustFS, or another S3-compatible service. Credentials use the standard AWS environment variables or credential chain. For R2, set `S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com` and `S3_REGION=auto`. If `S3_BUCKET` is unset, local storage is unchanged.

## DNS and TLS

For isolated preview subdomains, choose a base hostname such as `preview.example.com` and point both it and its wildcard to the Tabucom server:

```dns
preview.example.com    A      192.0.2.10
*.preview.example.com  CNAME  preview.example.com.
```

Use an `AAAA` record as well when the server has IPv6. The wildcard may instead point directly to the server with `A` and `AAAA` records.

Configure Tabucom with:

```env
PUBLIC_API_URL=https://preview.example.com
PREVIEW_DOMAIN=preview.example.com
```

The reverse proxy must route both `preview.example.com` and `*.preview.example.com` to Tabucom while preserving the request host. TLS must cover both names, using either one certificate with both names or separate exact and wildcard certificates.

Wildcard certificates require DNS-01 validation. Use a DNS provider supported by your certificate resolver, or automate the required `_acme-challenge` TXT records. If a CDN or DNS proxy cannot issue or serve certificates for these wildcard hosts, leave the records DNS-only and terminate TLS on your server.

## Development

Requires Go 1.23 or newer.

```sh
go run ./cmd/tabucom
make check
```

The opt-in S3 lifecycle test is compatible with RustFS:

```sh
docker run --rm -d --name tabucom-rustfs -p 19000:9000 rustfs/rustfs:latest
AWS_ACCESS_KEY_ID=rustfsadmin AWS_SECRET_ACCESS_KEY=rustfsadmin \
  AWS_EC2_METADATA_DISABLED=true S3_TEST_ENDPOINT=http://127.0.0.1:19000 \
  go test ./internal/server -run TestRustFSLifecycle -v
docker stop tabucom-rustfs
```

Read [the architecture guide](docs/architecture.md) for the request lifecycle, storage model, and security boundaries. Contributor and integration-test commands are in [AGENTS.md](AGENTS.md).

## Security

Publishing has no application-level authentication, so put Tabucom behind a VPN, SSO, or trusted ingress when publishing must be restricted. Per-deployment passwords protect visitor access, not the publish API. Uploaded JavaScript runs in visitors' browsers. Configure `PREVIEW_DOMAIN` with wildcard DNS and TLS when deployments require origin isolation.
