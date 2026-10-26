BIN ?= $(HOME)/.local/bin

.PHONY: build test install
build:
	go build -o worklog .
test:
	go vet ./... && go test ./...
install: test
	mkdir -p $(BIN) && go build -o $(BIN)/worklog .
