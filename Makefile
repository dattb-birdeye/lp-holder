.PHONY: build test test-race cover lint run cli docker

POOL ?= BGm1tav58oGcsQJehL9WXBFXF7D27vZsKefj4xJKD5Y

build:
	go build -o bin/lp-holder-api ./cmd/lp-holder-api
	go build -o bin/lp-holder-cli ./cmd/lp-holder-cli

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

cover:
	go test -count=1 -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out | tail -1

lint:
	gofmt -l . && go vet ./...

run:
	go run ./cmd/lp-holder-api

# make cli POOL=<address>   (needs LP_RPC_URL)
cli:
	go run ./cmd/lp-holder-cli -pool $(POOL) -limit 10 -check

docker:
	docker build -t lp-holder .
