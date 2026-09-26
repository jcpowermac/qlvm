BIN := bin

build:
	go build -o $(BIN)/qlvm ./cmd/qlvm

test:
	go test ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run

.PHONY: build test lint
