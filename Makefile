BIN     := live
PREFIX  ?= /usr/local
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install vet test clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/live

install: build
	install -m 0755 bin/$(BIN) $(PREFIX)/bin/$(BIN)

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf bin
