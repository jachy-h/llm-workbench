SHELL := /bin/bash

BINARY  := llm-workbench
VERSION := $(shell sed -n 's/^[[:space:]]*Version = "\(.*\)"/\1/p' internal/config/config.go)
DIST    := dist
LDFLAGS := -s -w
PLATFORMS := darwin/arm64 darwin/amd64 windows/amd64 linux/amd64 linux/arm64

.PHONY: build test race vet fmt web dist clean clean-dist

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/llm-workbench

web:
	cd web && npm ci --no-audit --no-fund && npm run build

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd internal tests

dist: web clean-dist
	@set -euo pipefail; \
	for platform in $(PLATFORMS); do \
		goos="$${platform%/*}"; goarch="$${platform#*/}"; \
		ext=""; [ "$$goos" = "windows" ] && ext=".exe"; \
		name="$(BINARY)_$(VERSION)_$${goos}_$${goarch}"; \
		dir="$(DIST)/$$name"; \
		mkdir -p "$$dir"; \
		echo "building $$name"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
			go build -trimpath -ldflags="$(LDFLAGS)" -o "$$dir/$(BINARY)$$ext" ./cmd/llm-workbench; \
		cp README.md README.zh.md "$$dir/"; \
		if [ "$$goos" = "windows" ]; then \
			(cd $(DIST) && zip -qr "$$name.zip" "$$name"); \
		else \
			(cd $(DIST) && tar -czf "$$name.tar.gz" "$$name"); \
		fi; \
		rm -rf "$$dir"; \
	done; \
	cd $(DIST) && { command -v sha256sum >/dev/null && sha256sum *.tar.gz *.zip || shasum -a 256 *.tar.gz *.zip; } > checksums.txt; \
	cat checksums.txt

clean-dist:
	rm -rf $(DIST)

clean: clean-dist
	rm -f $(BINARY)
