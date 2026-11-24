APP_NAME := flyshadow-bar
GITHUB_REPO := KevinXC5/flyshadow-bar
VERSION_PKG := github.com/KevinXC5/flyshadow-bar/internal/version

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

BASE_LDFLAGS := \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT) \
	-X $(VERSION_PKG).BuildDate=$(BUILD_DATE) \
	-X $(VERSION_PKG).GithubRepo=$(GITHUB_REPO)

BIN_DIR := bin
DIST_DIR := dist

.PHONY: build run test test-live vet build-local build-macos build-win app win release-local clean

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags "$(BASE_LDFLAGS)" -o $(BIN_DIR)/$(APP_NAME) ./cmd/flyshadow-bar

run:
	CGO_ENABLED=0 go run -ldflags "$(BASE_LDFLAGS)" ./cmd/flyshadow-bar

test:
	CGO_ENABLED=0 go test -v ./...

test-live:
	CGO_ENABLED=0 FLYSHADOW_LIVE_TEST=1 go test -v ./internal/core

vet:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go vet ./...
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go vet ./...
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...

# 使用 MyGo 构建本机当前架构应用（跳过 DMG、安装包与公证，便于本地快速验证）。
build-local:
	CGO_ENABLED=0 MYGO_LDFLAGS="$(BASE_LDFLAGS)" go tool mygo build -skip-dmg -skip-installer -skip-notarize

# 使用 MyGo 构建 macOS 双架构（Apple Silicon + Intel）正式包与签名更新产物。
build-macos app:
	CGO_ENABLED=0 MYGO_LDFLAGS="$(BASE_LDFLAGS)" go tool mygo build -platform darwin/arm64,darwin/amd64 -skip-notarize

# 使用 MyGo 零 CGO 交叉编译 Windows amd64 正式包与签名更新产物。
build-win win:
	CGO_ENABLED=0 MYGO_LDFLAGS="$(BASE_LDFLAGS)" go tool mygo build -platform windows/amd64

# 本地一键构建全平台（macOS arm64/amd64 + Windows amd64）产物。
release-local:
	CGO_ENABLED=0 MYGO_LDFLAGS="$(BASE_LDFLAGS)" go tool mygo build -platform darwin/arm64,darwin/amd64,windows/amd64 -skip-notarize

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)
