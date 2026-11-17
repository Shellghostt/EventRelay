BINARY := eventrelay
EXE_SUFFIX := $(shell go env GOEXE)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: bootstrap build test race lint fmt vet run docker clean
.PHONY: frontend-install frontend-typecheck frontend-lint frontend-test frontend-build frontend-audit frontend-licenses frontend-browser frontend-check frontend-assets-check

bootstrap:
	go mod download
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)$(EXE_SUFFIX) ./cmd/eventrelay
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/worker$(EXE_SUFFIX) ./cmd/worker

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
	./bin/$(BINARY)$(EXE_SUFFIX)

docker:
	docker build --build-arg VERSION="$(VERSION)" -t eventrelay:$(VERSION) .

clean:
	go clean
	rm -rf bin

frontend-install:
	npm --prefix web ci --ignore-scripts

frontend-typecheck:
	npm --prefix web run typecheck

frontend-lint:
	npm --prefix web run lint

frontend-test:
	npm --prefix web test

frontend-build:
	npm --prefix web run build

frontend-audit:
	npm --prefix web run audit

frontend-licenses:
	npm --prefix web run licenses

frontend-browser:
	npm --prefix web run test:browser

frontend-check: frontend-typecheck frontend-lint frontend-test frontend-build frontend-licenses frontend-audit

frontend-assets-check: frontend-build
	git diff --exit-code -- internal/dashboard/dist
