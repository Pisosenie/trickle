VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS := -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: all
all: out/torrentd

.PHONY: out/torrentd
out/torrentd:
	mkdir -p out
	cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o ../out/torrentd ./cmd/torrentd

.PHONY: test
test:
	cd src && go vet ./... && go test -race ./...

.PHONY: clean
clean:
	rm -rf out
