set dotenv-load

image := env_var_or_default("IMAGE", "tabucom")
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

# Build the local Docker image.
build:
    docker build -t {{ image }} .

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

# Run the Go checks used by CI.
check:
    make check

# Stop the local test container if it is running.
stop:
    -docker stop {{ container }}

# Remove the local test data volume.
clean-data: stop
    -docker volume rm {{ volume }}
