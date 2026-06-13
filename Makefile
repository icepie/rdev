.PHONY: all build clean cross server client

BINS = rdev-server rdev-client
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -s -w -X main.version=$(VERSION)

all: build

build: server client

server:
	go build -ldflags "$(LDFLAGS)" -o rdev-server ./cmd/rdev-server

client:
	go build -ldflags "$(LDFLAGS)" -o rdev-client ./cmd/rdev-client

clean:
	rm -f $(BINS) $(BINS)-*

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

rdev-server-linux-%:
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-linux-%:
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

rdev-server-darwin-%:
	CGO_ENABLED=0 GOOS=darwin GOARCH=$* go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-darwin-%:
	CGO_ENABLED=0 GOOS=darwin GOARCH=$* go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

rdev-server-windows-amd64.exe:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-server

rdev-client-windows-amd64.exe:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/rdev-client

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .
