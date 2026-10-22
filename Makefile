VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64

.PHONY: build test vet dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/muster ./cmd/muster

test:
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

dist:
	@rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/muster-$$os-$$arch$$ext ./cmd/muster || exit 1; \
	done
	@cd dist && shasum -a 256 muster-* > SHA256SUMS

clean:
	rm -rf bin dist
