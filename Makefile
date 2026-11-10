.PHONY: run build test fmt vet

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
