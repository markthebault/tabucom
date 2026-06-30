set dotenv-load

image := env_var_or_default("IMAGE", "tabucom")
image_repository := env_var_or_default("IMAGE_REPOSITORY", "tabucom")
image_tag := env_var_or_default("IMAGE_TAG", "latest")
image_platform := env_var_or_default("IMAGE_PLATFORM", "linux/amd64")
port := env_var_or_default("PORT", "8080")
origin := env_var_or_default("PUBLIC_API_URL", "http://localhost:" + port)
volume := env_var_or_default("DATA_VOLUME", "tabucom-test-data")
container := env_var_or_default("CONTAINER", "tabucom-test")
token_secret := env_var_or_default("STATELESS_TOKEN_SIGNING_SECRET", "12345678901234567890123456789012")

alias run := run-open
alias tokens := run-tokens

# List available commands.
default:
    @just --list

# Format Go code.
fmt:
    gofmt -w ./cmd ./internal

# Check Go formatting without modifying files.
fmt-check:
    @fmt_out="$(gofmt -l ./cmd ./internal)"; test -z "$fmt_out" || { echo "$fmt_out"; exit 1; }

# Run tests.
test:
    go test ./...

# Run go vet.
vet:
    go vet ./...

# Validate embedded JSON documents.
json-check:
    python3 -m json.tool internal/server/web/openapi.json >/dev/null
    python3 -m json.tool internal/server/web/.well-known/agent.json >/dev/null

# Run the checks used by CI.
check: fmt-check test vet json-check

# Build the Go binary.
binary:
    go build ./cmd/tabucom

# Build the local Docker image.
build:
    docker build -t {{ image }} .

# Build the release Docker image locally with buildx.
docker-build:
    docker buildx build --platform {{ image_platform }} --tag {{ image_repository }}:{{ image_tag }} --load .

# Build and push the release Docker image with buildx.
docker-push:
    #!/usr/bin/env sh
    set -eu
    tags="--tag {{ image_repository }}:{{ image_tag }}"
    if [ -n "${IMAGE_SHORT_SHA:-}" ]; then
        tags="$tags --tag {{ image_repository }}:$IMAGE_SHORT_SHA"
    fi
    docker buildx build --platform {{ image_platform }} $tags --push .

# Run locally with the default open publish API.
run-open: build
    docker run --rm --name {{ container }} -p {{ port }}:8080 \
        -e PUBLIC_API_URL={{ origin }} \
        -v {{ volume }}:/data \
        {{ image }}

# Run locally with stateless publish tokens enabled.
run-tokens: build
    docker run --rm --name {{ container }} -p {{ port }}:8080 \
        -e PUBLIC_API_URL={{ origin }} \
        -e STATELESS_PUBLISH_TOKENS_ENABLED=true \
        -e STATELESS_TOKEN_SIGNING_SECRET={{ token_secret }} \
        -v {{ volume }}:/data \
        {{ image }}

# Run locally with wildcard preview-domain config.
run-preview preview_domain="localhost": build
    docker run --rm --name {{ container }} -p {{ port }}:8080 \
        -e PUBLIC_API_URL={{ origin }} \
        -e PREVIEW_DOMAIN={{ preview_domain }} \
        -v {{ volume }}:/data \
        {{ image }}

# Run locally with wildcard preview-domain config and stateless publish tokens.
run-preview-tokens preview_domain="localhost": build
    docker run --rm --name {{ container }} -p {{ port }}:8080 \
        -e PUBLIC_API_URL={{ origin }} \
        -e PREVIEW_DOMAIN={{ preview_domain }} \
        -e STATELESS_PUBLISH_TOKENS_ENABLED=true \
        -e STATELESS_TOKEN_SIGNING_SECRET={{ token_secret }} \
        -v {{ volume }}:/data \
        {{ image }}

# Stop the local test container if it is running.
stop:
    -docker stop {{ container }}

# Remove the local test data volume.
clean-data: stop
    -docker volume rm {{ volume }}
