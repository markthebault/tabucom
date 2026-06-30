.PHONY: check build docker-push

check:
	just check

build:
	just binary

docker-push:
	just docker-push
