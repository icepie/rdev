.PHONY: all build clean cross server client web web-install test vet fmt win7-go-client win7-service-wrapper rust-client-gpu rust-client-gpu-check rust-client-gpu-fmt rust-client-gpu-clippy rust-client-gpu-test rust-client-gpu-smoke rust-client-gpu-linux-desktop-package rust-client-gpu-windows-arm64-package rust-client-gpu-win7-package rust-client-gpu-win7-rdev-desktop-package rust-client-gpu-win7-stage rust-client-gpu-win7-smoke

BINS = rdev-server rdev-client
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -s -w -X main.version=$(VERSION)
GO_WIN7 ?= go
WIN7_SERVICE_DIST = target/win7-service
RUST_CLIENT_GPU_MANIFEST = clients/rdev-client-gpu/Cargo.toml
RUST_CLIENT_GPU_WIN7_DIR = clients/rdev-client-gpu/target/x86_64-pc-windows-gnullvm/release
RUST_CLIENT_GPU_WIN7_DIST = clients/rdev-client-gpu/target/win7-dist
RUST_CLIENT_GPU_WIN7_RDEV_DESKTOP_DIST = clients/rdev-client-gpu/target/win7-rdev-desktop-dist

all: build

build: web server client

web:
	bun install --frozen-lockfile
	bun run build

web-install:
	bun install

server: web
	go build -ldflags "$(LDFLAGS)" -o rdev-server ./cmd/rdev-server

client:
	go build -ldflags "$(LDFLAGS)" -o rdev-client ./cmd/rdev-client

clean:
	rm -f $(BINS) $(BINS)-*
	rm -rf clients/rdev-client-gpu/target $(WIN7_SERVICE_DIST)

win7-go-client:
	mkdir -p $(WIN7_SERVICE_DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO_WIN7) build -ldflags "$(LDFLAGS)" -o $(WIN7_SERVICE_DIST)/rdev-client.exe ./cmd/rdev-client

win7-service-wrapper:
	mkdir -p $(WIN7_SERVICE_DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO_WIN7) build -ldflags "$(LDFLAGS)" -o $(WIN7_SERVICE_DIST)/rdev-service-wrapper.exe ./cmd/rdev-service-wrapper

rust-client-gpu:
	RDEV_VERSION=$(VERSION) cargo build --release --manifest-path $(RUST_CLIENT_GPU_MANIFEST) --features embedded-rdev-desktop

rust-client-gpu-linux-desktop-package:
	clients/rdev-client-gpu/scripts/package-linux-desktop.sh linux

rust-client-gpu-windows-arm64-package:
	RDEV_VERSION=$(VERSION) cargo build --release --manifest-path $(RUST_CLIENT_GPU_MANIFEST) --target aarch64-pc-windows-gnullvm --features embedded-rdev-desktop

rust-client-gpu-win7-package:
	RDEV_VERSION=$(VERSION) cargo build --release --manifest-path $(RUST_CLIENT_GPU_MANIFEST) --target x86_64-pc-windows-gnullvm
	$(MAKE) rust-client-gpu-win7-stage WIN7_STAGE_DIST=$(RUST_CLIENT_GPU_WIN7_DIST)

rust-client-gpu-win7-rdev-desktop-package:
	RDEV_VERSION=$(VERSION) cargo build --release --manifest-path $(RUST_CLIENT_GPU_MANIFEST) --target x86_64-pc-windows-gnullvm --features embedded-rdev-desktop
	$(MAKE) rust-client-gpu-win7-stage WIN7_STAGE_DIST=$(RUST_CLIENT_GPU_WIN7_RDEV_DESKTOP_DIST)

rust-client-gpu-win7-stage:
	test -n "$(WIN7_STAGE_DIST)"
	rm -rf $(WIN7_STAGE_DIST)
	mkdir -p $(WIN7_STAGE_DIST)
	python3 clients/rdev-client-gpu/win7/patch_imports.py \
		$(RUST_CLIENT_GPU_WIN7_DIR)/rdev-client-gpu.exe \
		$(WIN7_STAGE_DIST)/rdev-client-gpu.exe
	python3 clients/rdev-client-gpu/win7/build_shims.py $(WIN7_STAGE_DIST)
	python3 clients/rdev-client-gpu/win7/copy_winpty_runtime.py $(WIN7_STAGE_DIST)

rust-client-gpu-check: rust-client-gpu-fmt rust-client-gpu-clippy rust-client-gpu-test

rust-client-gpu-fmt:
	cargo fmt --manifest-path $(RUST_CLIENT_GPU_MANIFEST) --check

rust-client-gpu-clippy:
	cargo clippy --manifest-path $(RUST_CLIENT_GPU_MANIFEST) --all-targets -- -D warnings

rust-client-gpu-test:
	cargo test --manifest-path $(RUST_CLIENT_GPU_MANIFEST)

rust-client-gpu-smoke:
	clients/rdev-client-gpu/scripts/smoke.sh

rust-client-gpu-win7-smoke:
	clients/rdev-client-gpu/scripts/win7-smoke.sh

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
	rdev-client-windows-amd64.exe

rdev-server-linux-%: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-linux-%:
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

rdev-server-darwin-%: web
	CGO_ENABLED=0 GOOS=darwin GOARCH=$* go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-darwin-%:
	CGO_ENABLED=0 GOOS=darwin GOARCH=$* go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

rdev-server-windows-amd64.exe: web
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-windows-amd64.exe:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .
