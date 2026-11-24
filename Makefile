APP_NAME := flyshadow-bar
APP_DISPLAY_NAME := FlyShadow Bar
GITHUB_REPO := KevinXC5/flyshadow-bar
VERSION_PKG := github.com/KevinXC5/flyshadow-bar/internal/version

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
SHORT_VERSION := $(shell printf '%s' '$(VERSION)' | sed -E 's/^v//; s/[^0-9.].*//')
ifeq ($(strip $(SHORT_VERSION)),)
SHORT_VERSION := 0.1.0
endif
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

BASE_LDFLAGS := \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT) \
	-X $(VERSION_PKG).BuildDate=$(BUILD_DATE) \
	-X $(VERSION_PKG).GithubRepo=$(GITHUB_REPO)

PROD_LDFLAGS := -s -w -X github.com/egoist/mygo.production=1 $(BASE_LDFLAGS)
PROD_TAGS := -tags mygo_noinspector

BIN_DIR := bin
DIST_DIR := dist
MACOS_APP_DIR := $(DIST_DIR)/$(APP_DISPLAY_NAME).app
MACOS_ZIP := $(DIST_DIR)/$(APP_NAME)-$(VERSION)-darwin-arm64.zip
WIN_EXE := $(DIST_DIR)/$(APP_NAME).exe
WIN_ZIP := $(DIST_DIR)/$(APP_NAME)-$(VERSION)-windows-amd64.zip

.PHONY: build run test test-live app win release-local clean

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags "$(BASE_LDFLAGS)" -o $(BIN_DIR)/$(APP_NAME) ./cmd/flyshadow-bar

run:
	CGO_ENABLED=0 go run -ldflags "$(BASE_LDFLAGS)" ./cmd/flyshadow-bar

test:
	CGO_ENABLED=0 go test -v ./...

test-live:
	CGO_ENABLED=0 FLYSHADOW_LIVE_TEST=1 go test -v ./internal/core

app:
	@rm -rf "$(MACOS_APP_DIR)" "$(MACOS_ZIP)"
	@mkdir -p "$(MACOS_APP_DIR)/Contents/MacOS" "$(MACOS_APP_DIR)/Contents/Resources"
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(PROD_TAGS) -ldflags "$(PROD_LDFLAGS)" \
		-o "$(MACOS_APP_DIR)/Contents/MacOS/$(APP_NAME)" ./cmd/flyshadow-bar
	@sed -e 's/{{VERSION}}/$(VERSION)/g' -e 's/{{SHORT_VERSION}}/$(SHORT_VERSION)/g' \
		packaging/darwin/Info.plist.tmpl > "$(MACOS_APP_DIR)/Contents/Info.plist"
	@if command -v codesign >/dev/null 2>&1; then \
		codesign --force --deep --sign - "$(MACOS_APP_DIR)"; \
	fi
	@cd $(DIST_DIR) && zip -r -q "$(notdir $(MACOS_ZIP))" "$(APP_DISPLAY_NAME).app"

win:
	@mkdir -p $(DIST_DIR)
	@rm -f "$(WIN_EXE)" "$(WIN_ZIP)"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(PROD_TAGS) -ldflags "$(PROD_LDFLAGS)" \
		-o "$(WIN_EXE)" ./cmd/flyshadow-bar
	@cd $(DIST_DIR) && zip -j -q "$(notdir $(WIN_ZIP))" "$(notdir $(WIN_EXE))"

release-local: app win
	@cd $(DIST_DIR) && rm -f checksums.txt && \
	if command -v shasum >/dev/null 2>&1; then \
		shasum -a 256 "$(notdir $(MACOS_ZIP))" "$(notdir $(WIN_ZIP))" "$(notdir $(WIN_EXE))" > checksums.txt; \
	else \
		sha256sum "$(notdir $(MACOS_ZIP))" "$(notdir $(WIN_ZIP))" "$(notdir $(WIN_EXE))" > checksums.txt; \
	fi
	@cat $(DIST_DIR)/checksums.txt

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)
