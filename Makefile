IMAGE_REPOSITORY ?= tabucom
IMAGE_TAG ?= latest
IMAGE_PLATFORM ?= linux/amd64
IMAGE := $(IMAGE_REPOSITORY):$(IMAGE_TAG)
IMAGE_TAGS := --tag $(IMAGE) $(if $(IMAGE_SHORT_SHA),--tag $(IMAGE_REPOSITORY):$(IMAGE_SHORT_SHA))

.PHONY: fmt fmt-check test vet json-check check build docker-build docker-push

fmt:
	gofmt -w ./cmd ./internal

fmt-check:
	@fmt_out="$$(gofmt -l ./cmd ./internal)"; \
	test -z "$$fmt_out" || { \
		echo "$$fmt_out"; \
		exit 1; \
	}

test:
	go test ./...

vet:
	go vet ./...

json-check:
	python3 -m json.tool internal/server/web/openapi.json >/dev/null
	python3 -m json.tool internal/server/web/.well-known/agent.json >/dev/null

check: fmt-check test vet json-check

build:
	go build ./cmd/tabucom

docker-build:
	docker buildx build --platform $(IMAGE_PLATFORM) $(IMAGE_TAGS) --load .

docker-push:
	docker buildx build --platform $(IMAGE_PLATFORM) $(IMAGE_TAGS) --push .
