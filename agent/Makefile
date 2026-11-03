# Agent build. Static binaries (CGO_ENABLED=0) for linux/amd64 and linux/arm64, vendored modules,
# reproducible: no clock, no VCS stamp, no build id. CI uses scripts/agent-ci.sh with the same flags.
SHELL := /bin/bash
VERSION ?= 0.0.0-dev
MODULE  := github.com/marinoscar/local-ai/agent
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
COMMIT  := $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)
BUILT   := $(shell date -u -d @$(SOURCE_DATE_EPOCH) +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r $(SOURCE_DATE_EPOCH) +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -buildid= -X $(MODULE)/internal/app.Version=$(VERSION) -X $(MODULE)/internal/app.Commit=$(COMMIT) -X $(MODULE)/internal/app.BuiltAt=$(BUILT)
MODFLAG := $(shell [ -d vendor ] && echo -mod=vendor)
GOBUILD := CGO_ENABLED=0 GOOS=linux go build $(MODFLAG) -trimpath -buildvcs=false -ldflags "$(LDFLAGS)"

.PHONY: build build-amd64 build-arm64 release src-tarball test vendor clean

build: build-amd64 build-arm64

build-amd64:
	GOARCH=amd64 $(GOBUILD) -o bin/aiagent-linux-amd64 ./cmd/aiagent

build-arm64:
	GOARCH=arm64 $(GOBUILD) -o bin/aiagent-linux-arm64 ./cmd/aiagent

# Release build by hand: make release VERSION=0.1.0 (the semver, without node-v). CI does the same through
# scripts/agent-ci.sh and signs in the release workflow; this target never signs.
release:
	@[[ "$(origin VERSION)" != file && "$(VERSION)" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$ ]] || { echo "set VERSION to the release semver, e.g. make release VERSION=0.1.0" >&2; exit 2; }
	$(MAKE) build VERSION=$(VERSION)
	@cd bin && sha256sum aiagent-linux-amd64 aiagent-linux-arm64

# Agent source tarball for the rebuild kit (docs/specs/rebuild-kit.md section 3.2): git archive of HEAD + go mod
# vendor + the BUILT UI (build it first: cd ui && npm ci && npm run build). Output: dist/local-ai-node-v<VERSION>-src.tar.gz.
src-tarball:
	@[[ "$(origin VERSION)" != file && "$(VERSION)" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$ ]] || { echo "set VERSION to the release semver, e.g. make src-tarball VERSION=0.1.0" >&2; exit 2; }
	../scripts/release/build-src-tarball.sh --tag node-v$(VERSION) --ref HEAD --out dist

test:
	go vet ./...
	go test -race ./...

vendor:
	go mod vendor

clean:
	rm -rf bin dist
