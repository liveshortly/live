BIN     := live
PREFIX  ?= /usr/local
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)
LDFLAGS := -X main.version=$(VERSION)

PLATFORMS         := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64
LIVESHORTLY_REPO  ?= ../LiveShortly
INSTALL_DIR        = $(LIVESHORTLY_REPO)/web/public/install

.PHONY: build install vet test clean release publish

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/live

install: build
	install -m 0755 bin/$(BIN) $(PREFIX)/bin/$(BIN)

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf bin dist

# Cross-compile stripped release binaries for every platform into dist/,
# each with a .sha256 (of the raw binary) and a .gz (what gets served).
release:
	rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		GOOS=$${p%-*} GOARCH=$${p#*-} go build -trimpath \
			-ldflags "-s -w $(LDFLAGS)" -o dist/$(BIN)-$(VERSION)-$$p ./cmd/live || exit 1; \
		shasum -a 256 dist/$(BIN)-$(VERSION)-$$p | awk '{print $$1}' > dist/$(BIN)-$(VERSION)-$$p.sha256; \
		gzip -9 -f dist/$(BIN)-$(VERSION)-$$p; \
		echo "built $(BIN)-$(VERSION)-$$p"; \
	done

# Copy the release into the LiveShortly web app's /install/ folder (served at
# https://liveshortly.com/install/) and mark it latest. Run AFTER `git tag`
# so VERSION is the exact tag. Older versions stay in place — the folder is
# cumulative so any published version remains installable.
publish: release
	mkdir -p $(INSTALL_DIR)
	cp dist/*.gz dist/*.sha256 $(INSTALL_DIR)/
	echo $(VERSION) > $(INSTALL_DIR)/latest.txt
	@echo "published $(VERSION) → $(INSTALL_DIR) (commit + deploy LiveShortly to serve it)"
