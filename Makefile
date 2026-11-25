.PHONY: all build clean cross server client web web-install test vet fmt win7-go-client win7-service-wrapper rust-client-rs rust-client-rs-check rust-client-rs-fmt rust-client-rs-clippy rust-client-rs-test rust-client-rs-smoke rust-client-rs-linux-desktop-package rust-client-rs-windows-arm64-package rust-client-rs-win7-package rust-client-rs-win7-rdev-desktop-package rust-client-rs-win7-stage rust-client-rs-win7-smoke client-cpp

BINS = rdev-server rdev-client
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -s -w -X main.version=$(VERSION)
# Static embeds ship gzip-only (see vite.config.ts), so no UPX is needed or wanted.
GO_TRIMPATH ?= -trimpath
GO264_TAGS ?=
GO_WIN7 ?= go
WIN7_SERVICE_DIST = target/win7-service
RUST_CLIENT_RS_MANIFEST = clients/rdev-client-rs/Cargo.toml
RUST_CLIENT_RS_WIN7_DIR = clients/rdev-client-rs/target/x86_64-pc-windows-gnullvm/release
RUST_CLIENT_RS_WIN7_DIST = clients/rdev-client-rs/target/win7-dist
RUST_CLIENT_RS_WIN7_RUNTIME_DIR ?= /opt/llvm-mingw/x86_64-w64-mingw32/bin
RUST_CLIENT_RS_WIN7_RDEV_DESKTOP_DIST = clients/rdev-client-rs/target/win7-rdev-desktop-dist
CPP_CLIENT_BUILD_DIR = target/rdev-client-cpp

all: build

build: web server client

web:
	bun install --frozen-lockfile
	bun run build

web-install:
	bun install

server: web
	go build $(GO_TRIMPATH) -ldflags "$(LDFLAGS)" -o rdev-server ./cmd/rdev-server

client:
	go build $(GO_TRIMPATH) $(GO264_TAGS) -ldflags "$(LDFLAGS)" -o rdev-client ./cmd/rdev-client


client-cpp:
	cmake -S clients/rdev-client-cpp -B $(CPP_CLIENT_BUILD_DIR)
	cmake --build $(CPP_CLIENT_BUILD_DIR) --parallel


clean:
	rm -f $(BINS) $(BINS)-*
	rm -rf clients/rdev-client-rs/target $(WIN7_SERVICE_DIST) $(CPP_CLIENT_BUILD_DIR)

win7-go-client:
	mkdir -p $(WIN7_SERVICE_DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO_WIN7) build $(GO264_TAGS) -ldflags "$(LDFLAGS)" -o $(WIN7_SERVICE_DIST)/rdev-client.exe ./cmd/rdev-client

win7-service-wrapper:
	mkdir -p $(WIN7_SERVICE_DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO_WIN7) build -ldflags "$(LDFLAGS)" -o $(WIN7_SERVICE_DIST)/rdev-service-wrapper.exe ./cmd/rdev-service-wrapper

rust-client-rs:
	RDEV_VERSION=$(VERSION) cargo build --release --manifest-path $(RUST_CLIENT_RS_MANIFEST) --features embedded-rdev-desktop

rust-client-rs-linux-desktop-package:
	clients/rdev-client-rs/scripts/package-linux-desktop.sh linux

rust-client-rs-windows-arm64-package:
	RDEV_VERSION=$(VERSION) cargo build --release --manifest-path $(RUST_CLIENT_RS_MANIFEST) --target aarch64-pc-windows-gnullvm --features embedded-rdev-desktop

rust-client-rs-win7-package:
	RDEV_VERSION=$(VERSION) cargo build --release --manifest-path $(RUST_CLIENT_RS_MANIFEST) --target x86_64-pc-windows-gnullvm
	$(MAKE) rust-client-rs-win7-stage WIN7_STAGE_DIST=$(RUST_CLIENT_RS_WIN7_DIST)

rust-client-rs-win7-rdev-desktop-package:
	RDEV_VERSION=$(VERSION) cargo build --release --manifest-path $(RUST_CLIENT_RS_MANIFEST) --target x86_64-pc-windows-gnullvm --features embedded-rdev-desktop
	$(MAKE) rust-client-rs-win7-stage WIN7_STAGE_DIST=$(RUST_CLIENT_RS_WIN7_RDEV_DESKTOP_DIST)

rust-client-rs-win7-stage:
	test -n "$(WIN7_STAGE_DIST)"
	rm -rf $(WIN7_STAGE_DIST)
	mkdir -p $(WIN7_STAGE_DIST)
	python3 clients/rdev-client-rs/win7/patch_imports.py \
		$(RUST_CLIENT_RS_WIN7_DIR)/rdev-client-rs.exe \
		$(WIN7_STAGE_DIST)/rdev-client-rs.exe
	python3 clients/rdev-client-rs/win7/build_shims.py $(WIN7_STAGE_DIST)
	python3 clients/rdev-client-rs/win7/copy_winpty_runtime.py $(WIN7_STAGE_DIST)
	test -f $(RUST_CLIENT_RS_WIN7_RUNTIME_DIR)/libunwind.dll
	cp $(RUST_CLIENT_RS_WIN7_RUNTIME_DIR)/libunwind.dll $(WIN7_STAGE_DIST)/

rust-client-rs-check: rust-client-rs-fmt rust-client-rs-clippy rust-client-rs-test

rust-client-rs-fmt:
	cargo fmt --manifest-path $(RUST_CLIENT_RS_MANIFEST) --check

rust-client-rs-clippy:
	cargo clippy --manifest-path $(RUST_CLIENT_RS_MANIFEST) --all-targets -- -D warnings

rust-client-rs-test:
	cargo test --manifest-path $(RUST_CLIENT_RS_MANIFEST)

rust-client-rs-smoke:
	clients/rdev-client-rs/scripts/smoke.sh

rust-client-rs-win7-smoke:
	clients/rdev-client-rs/scripts/win7-smoke.sh

cross: cross-linux cross-darwin cross-windows

cross-linux: \
	rdev-server-linux-amd64 \
	rdev-server-linux-arm64 \
	rdev-client-linux-amd64 \
	rdev-client-linux-arm64

cross-darwin: \
	rdev-server-darwin-amd64 \
	rdev-server-darwin-arm64 \
	rdev-client-darwin-amd64 \
	rdev-client-darwin-arm64

cross-windows: \
	rdev-server-windows-amd64.exe \
	rdev-client-windows-amd64.exe \
	rdev-client-windows-386.exe

rdev-server-linux-%: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build $(GO_TRIMPATH) -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-linux-%:
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build $(GO_TRIMPATH) $(GO264_TAGS) -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

rdev-server-darwin-%: web
	CGO_ENABLED=0 GOOS=darwin GOARCH=$* go build $(GO_TRIMPATH) -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-darwin-%:
	CGO_ENABLED=0 GOOS=darwin GOARCH=$* go build $(GO_TRIMPATH) $(GO264_TAGS) -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

rdev-server-windows-amd64.exe: web
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GO_TRIMPATH) -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-windows-amd64.exe:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GO_TRIMPATH) $(GO264_TAGS) -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

rdev-client-windows-386.exe:
	CGO_ENABLED=0 GOOS=windows GOARCH=386 go build $(GO_TRIMPATH) $(GO264_TAGS) -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

test:
	go test $(GO264_TAGS) ./...

vet:
	go vet $(GO264_TAGS) ./...

fmt:
	gofmt -w .
