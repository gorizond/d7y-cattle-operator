IMAGE ?= ghcr.io/gorizond/d7y-cattle-operator
TAG ?= dev

.PHONY: build docker-build test docker-test lint helm-lint helm-template

# Docker is the source of truth for builds and tests.
build: docker-build

docker-build:
	docker build -t $(IMAGE):$(TAG) .

test: docker-test

docker-test:
	docker build --target test -t $(IMAGE):test .
	docker run --rm $(IMAGE):test go test ./...

lint:
	docker build --target test -t $(IMAGE):test .
	docker run --rm $(IMAGE):test go vet ./...

# Every chart checked into charts/ is linted. The KeyDB chart is published from
# its own repository; see README.md.
helm-lint:
	helm lint charts/*

helm-template:
	helm template d7y-cattle-operator charts/d7y-cattle-operator
	helm template d7y-cattle-operator charts/d7y-cattle-operator --set webhook.enabled=true
	helm template dragonfly-extras charts/dragonfly-extras --set ingress.enabled=true --set vpa.enabled=true --api-versions autoscaling.k8s.io/v1
	helm template p2p-static charts/p2p-static
