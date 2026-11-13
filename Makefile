BINARY := eventrelay
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: bootstrap build test race lint fmt vet run docker clean

bootstrap:
	go mod download
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/eventrelay
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/worker ./cmd/worker

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

lint: fmt vet
	golangci-lint run ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

run: build
	./bin/$(BINARY)

docker:
	docker build -t eventrelay:$(VERSION) .

clean:
	go clean
	rm -rf bin
