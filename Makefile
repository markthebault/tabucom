IMAGE_REPOSITORY ?= tabucom
IMAGE_TAG ?= latest
IMAGE_PLATFORM ?= linux/amd64
IMAGE := $(IMAGE_REPOSITORY):$(IMAGE_TAG)
IMAGE_TAGS := --tag $(IMAGE) $(if $(IMAGE_SHORT_SHA),--tag $(IMAGE_REPOSITORY):$(IMAGE_SHORT_SHA))

.PHONY: fmt test vet check build docker-build docker-push

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

vet:
	go vet ./...

check: test vet

build:
	go build ./cmd/tabucom

docker-build:
	docker buildx build --platform $(IMAGE_PLATFORM) $(IMAGE_TAGS) --load .

docker-push:
	docker buildx build --platform $(IMAGE_PLATFORM) $(IMAGE_TAGS) --push .
