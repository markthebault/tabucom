IMAGE_REPOSITORY ?= tabucom
IMAGE_TAG ?= latest
IMAGE_PLATFORM ?= linux/amd64
IMAGE_TAGS := --tag $(IMAGE_REPOSITORY):$(IMAGE_TAG) $(if $(IMAGE_SHORT_SHA),--tag $(IMAGE_REPOSITORY):$(IMAGE_SHORT_SHA))

.PHONY: check build docker-push

check:
	@fmt_out="$$(gofmt -l ./cmd ./internal)"; \
	test -z "$$fmt_out" || { \
		echo "$$fmt_out"; \
		exit 1; \
	}
	go test ./...
	go vet ./...
	python3 -m json.tool internal/server/web/openapi.json >/dev/null
	python3 -m json.tool internal/server/web/.well-known/agent.json >/dev/null

build:
	go build ./cmd/tabucom

docker-push:
	docker buildx build --platform $(IMAGE_PLATFORM) $(IMAGE_TAGS) --push .
