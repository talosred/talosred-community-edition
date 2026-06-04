VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
OUT     ?= talosred
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test run clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT) .

test:
	go test ./...

run: build
	./$(OUT)

clean:
	rm -f $(OUT) talosred-*-* talosred.db talosred.db-wal talosred.db-shm
