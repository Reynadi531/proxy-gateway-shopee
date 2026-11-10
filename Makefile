DEV_COMPOSE = docker compose -f compose.yaml -f docker/docker-compose.dev.yaml

.PHONY: run build test fmt vet docker.dev docker.dev.build docker.dev.down

run:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test -race ./...

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

docker.dev:
	$(DEV_COMPOSE) up

docker.dev.build:
	$(DEV_COMPOSE) up --build

docker.dev.down:
	$(DEV_COMPOSE) down
