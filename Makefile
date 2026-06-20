IMAGE_REPOSITORY ?= markthebault/tabucom
IMAGE_TAG ?= latest
IMAGE := $(IMAGE_REPOSITORY):$(IMAGE_TAG)

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
	docker build --tag $(IMAGE) .

docker-push: docker-build
	docker push $(IMAGE)
