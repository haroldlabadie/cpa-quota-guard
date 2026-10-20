GO ?= go
.PHONY: test build package
test:
	$(GO) test ./...
	$(GO) vet ./...
	$(GO) test -race ./...
	node --check web/settings.js
	node --test web/settings.test.js
build:
	mkdir -p dist
	CGO_ENABLED=1 $(GO) build -buildvcs=false -trimpath -buildmode=c-shared -o dist/cpa-quota-guard.so .
	$(GO) build -buildvcs=false -trimpath -o dist/guard-state ./cmd/guard-state
package: build
	cp README.md LICENSE config.example.yaml settings.example.json dist/
	cd dist && sha256sum cpa-quota-guard.so guard-state > BINARY_SHA256SUMS
	cd dist && zip cpa-quota-guard_0.2.0_linux_amd64.zip cpa-quota-guard.so cpa-quota-guard.h guard-state README.md LICENSE config.example.yaml settings.example.json BINARY_SHA256SUMS
	cd dist && sha256sum cpa-quota-guard_0.2.0_linux_amd64.zip > checksums.txt
