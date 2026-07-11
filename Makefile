BIN     := live
PREFIX  ?= /usr/local
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)
LDFLAGS := -X main.version=$(VERSION)

PLATFORMS         := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64
LIVESHORTLY_REPO  ?= ../liveshortly-server
INSTALL_DIR        = $(LIVESHORTLY_REPO)/web/public/install

DIST_REPO ?= liveshortly/live-dist

.PHONY: build install vet test clean release publish publish-gh check-clean use-local use-prod

# Point the installed `live` at the LOCAL stack (docker-compose.local.yml in
# liveshortly-server, single origin http://localhost:8080). Writes host.json;
# `live login` afterwards runs the device flow against your local server.
# Env-var alternative (no file): LIVESHORTLY_API_URL=http://localhost:8080 live …
LOCAL_ORIGIN ?= http://localhost:8080
PROD_ORIGIN  ?= https://liveshortly.com
use-local:
	@mkdir -p $$HOME/.liveshortly
	@printf '{"api_url":"%s","web_url":"%s"}\n' "$(LOCAL_ORIGIN)" "$(LOCAL_ORIGIN)" \
		> $$HOME/.liveshortly/host.json
	@echo "live → $(LOCAL_ORIGIN)  (run: live login)"

# Flip back to production. Writes the origin explicitly (the built-in default is
# localhost, so removing host.json would NOT point at prod).
use-prod:
	@mkdir -p $$HOME/.liveshortly
	@printf '{"api_url":"%s","web_url":"%s"}\n' "$(PROD_ORIGIN)" "$(PROD_ORIGIN)" \
		> $$HOME/.liveshortly/host.json
	@echo "live → $(PROD_ORIGIN)  (run: live login)"

# `git describe --dirty` appends "-dirty" when the tree has uncommitted changes,
# which would silently publish mislabeled assets (live-v1.0.1-dirty-*). Refuse.
# Skipped when VERSION is passed explicitly on the command line.
check-clean:
ifneq ($(origin VERSION),command line)
	@if git status --porcelain 2>/dev/null | grep -q .; then \
		echo "refusing to release from a dirty tree — VERSION would be '$(VERSION)'."; \
		echo "commit, stash, or pass it explicitly:  make $(MAKECMDGOALS) VERSION=vX.Y.Z"; \
		exit 1; \
	fi
endif

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
release: check-clean
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

# Publish the same artifacts as a GitHub Release on the public dist repo. This
# is the origin install.sh pulls from; $(INSTALL_DIR) is the fallback mirror.
# Run AFTER `git tag` so VERSION is the exact tag. Needs `gh auth login`.
publish-gh: release
	@gh release view $(VERSION) -R $(DIST_REPO) >/dev/null 2>&1 \
		|| gh release create $(VERSION) -R $(DIST_REPO) --title "live $(VERSION)" \
			--notes "Prebuilt \`live\` binaries for macOS and Linux (amd64/arm64). Install: curl -fsSL https://liveshortly.com/i.sh | bash"
	echo $(VERSION) > dist/latest.txt
	gh release upload $(VERSION) -R $(DIST_REPO) --clobber dist/*.gz dist/*.sha256 dist/latest.txt
	@echo "published $(VERSION) → https://github.com/$(DIST_REPO)/releases/tag/$(VERSION)"
