IMAGE_REPOSITORY ?= tabucom
IMAGE_TAG ?= latest
IMAGE_PLATFORM ?= linux/amd64
IMAGE ?= tabucom
PORT ?= 8080
PUBLIC_API_URL ?= http://localhost:$(PORT)
DATA_VOLUME ?= tabucom-test-data
CONTAINER ?= tabucom-test
STATELESS_TOKEN_SIGNING_SECRET ?= 12345678901234567890123456789012
IMAGE_TAGS := --tag $(IMAGE_REPOSITORY):$(IMAGE_TAG) $(if $(IMAGE_SHORT_SHA),--tag $(IMAGE_REPOSITORY):$(IMAGE_SHORT_SHA))

.PHONY: fmt fmt-check test vet json-check check build docker-build docker-push run run-open run-tokens run-preview run-preview-tokens stop clean-data

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

run: run-open

run-open:
	docker build -t $(IMAGE) .
	docker run --rm --name $(CONTAINER) -p $(PORT):8080 \
		-e PUBLIC_API_URL=$(PUBLIC_API_URL) \
		-v $(DATA_VOLUME):/data \
		$(IMAGE)

run-tokens:
	docker build -t $(IMAGE) .
	docker run --rm --name $(CONTAINER) -p $(PORT):8080 \
		-e PUBLIC_API_URL=$(PUBLIC_API_URL) \
		-e STATELESS_PUBLISH_TOKENS_ENABLED=true \
		-e STATELESS_TOKEN_SIGNING_SECRET=$(STATELESS_TOKEN_SIGNING_SECRET) \
		-v $(DATA_VOLUME):/data \
		$(IMAGE)

run-preview:
	docker build -t $(IMAGE) .
	docker run --rm --name $(CONTAINER) -p $(PORT):8080 \
		-e PUBLIC_API_URL=$(PUBLIC_API_URL) \
		-e PREVIEW_DOMAIN=$(PREVIEW_DOMAIN) \
		-v $(DATA_VOLUME):/data \
		$(IMAGE)

run-preview-tokens:
	docker build -t $(IMAGE) .
	docker run --rm --name $(CONTAINER) -p $(PORT):8080 \
		-e PUBLIC_API_URL=$(PUBLIC_API_URL) \
		-e PREVIEW_DOMAIN=$(PREVIEW_DOMAIN) \
		-e STATELESS_PUBLISH_TOKENS_ENABLED=true \
		-e STATELESS_TOKEN_SIGNING_SECRET=$(STATELESS_TOKEN_SIGNING_SECRET) \
		-v $(DATA_VOLUME):/data \
		$(IMAGE)

stop:
	-docker stop $(CONTAINER)

clean-data: stop
	-docker volume rm $(DATA_VOLUME)
