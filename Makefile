.PHONY: all build clean cross server client web web-install test vet fmt rust-client-gpu rust-client-gpu-check rust-client-gpu-fmt rust-client-gpu-clippy rust-client-gpu-test rust-client-gpu-smoke

BINS = rdev-server rdev-client
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -s -w -X main.version=$(VERSION)
RUST_CLIENT_GPU_MANIFEST = clients/rdev-client-gpu/Cargo.toml

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
	rm -rf clients/rdev-client-gpu/target

rust-client-gpu:
	cargo build --release --manifest-path $(RUST_CLIENT_GPU_MANIFEST)

rust-client-gpu-check: rust-client-gpu-fmt rust-client-gpu-clippy rust-client-gpu-test

rust-client-gpu-fmt:
	cargo fmt --manifest-path $(RUST_CLIENT_GPU_MANIFEST) --check

rust-client-gpu-clippy:
	cargo clippy --manifest-path $(RUST_CLIENT_GPU_MANIFEST) --all-targets -- -D warnings

rust-client-gpu-test:
	cargo test --manifest-path $(RUST_CLIENT_GPU_MANIFEST)

rust-client-gpu-smoke:
	clients/rdev-client-gpu/scripts/smoke.sh

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
