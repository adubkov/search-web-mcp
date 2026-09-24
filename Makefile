BINARY := search-web-mcp
BIN := bin/$(BINARY)

.PHONY: all build test vet fmt fmt-check run install clean

all: build

build:
	go build -o $(BIN) ./cmd/search-web-mcp

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

run: build
	$(BIN)

install:
	go install ./cmd/search-web-mcp

clean:
	rm -rf bin
