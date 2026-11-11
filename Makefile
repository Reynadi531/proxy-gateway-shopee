DEV_COMPOSE = docker compose -f compose.yaml -f docker/docker-compose.dev.yaml
PROD_COMPOSE = docker compose -f compose.prod.yaml

.PHONY: run build test fmt vet docker.dev docker.dev.build docker.dev.down docker.prod docker.prod.down docker.prod.pull

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

docker.prod:
	$(PROD_COMPOSE) up -d

docker.prod.pull:
	$(PROD_COMPOSE) pull

docker.prod.down:
	$(PROD_COMPOSE) down
