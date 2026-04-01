VERSION ?= $(shell git describe --tags --abbrev=0 --match "v[0-9]*" 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X github.com/nextlevelbuilder/goclaw/cmd.Version=$(VERSION)
BINARY   = goclaw
DOCKER_GO_IMAGE ?= golang:1.26-bookworm

UNAME_S := $(shell uname -s 2>/dev/null)
UNAME_M := $(shell uname -m 2>/dev/null)

ifeq ($(UNAME_S),Darwin)
HOST_GOOS := darwin
else ifeq ($(UNAME_S),Linux)
HOST_GOOS := linux
else
HOST_GOOS := $(shell printf '%s' "$(UNAME_S)" | tr '[:upper:]' '[:lower:]')
endif

ifeq ($(UNAME_M),x86_64)
HOST_GOARCH := amd64
else ifeq ($(UNAME_M),amd64)
HOST_GOARCH := amd64
else ifeq ($(UNAME_M),aarch64)
HOST_GOARCH := arm64
else ifeq ($(UNAME_M),arm64)
HOST_GOARCH := arm64
else
HOST_GOARCH := $(UNAME_M)
endif

BUILD_GOOS ?= $(HOST_GOOS)
BUILD_GOARCH ?= $(HOST_GOARCH)

.PHONY: build build-docker run clean version up down logs reset test vet check-web dev migrate setup ci desktop-dev desktop-build desktop-dmg check-go check-docker check-wails

check-go:
	@command -v go >/dev/null 2>&1 || { \
		echo "Go 1.26+ is required but 'go' was not found on PATH."; \
		echo "Install Go and rerun 'make build', or use 'make build-docker VERSION=$(VERSION)'."; \
		exit 127; \
	}

check-docker:
	@command -v docker >/dev/null 2>&1 || { \
		echo "Docker is required for this target but 'docker' was not found on PATH."; \
		exit 127; \
	}
	@docker info >/dev/null 2>&1 || { \
		echo "Docker is installed but the daemon is not reachable."; \
		echo "Start Docker Desktop (or the Docker daemon) and rerun 'make build-docker VERSION=$(VERSION)'."; \
		exit 125; \
	}

check-wails:
	@command -v wails >/dev/null 2>&1 || { \
		echo "Wails CLI is required for desktop targets but 'wails' was not found on PATH."; \
		echo "Install it with 'go install github.com/wailsapp/wails/v2/cmd/wails@latest'."; \
		exit 127; \
	}

build: check-go
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) .

build-docker: check-docker
	docker run --rm \
		-u "$$(id -u):$$(id -g)" \
		-v "$(CURDIR):/src" \
		-w /src \
		-e HOME=/tmp \
		-e GOCACHE=/tmp/go-build \
		-e GOMODCACHE=/tmp/go-mod \
		-e CGO_ENABLED=0 \
		-e GOOS=$(BUILD_GOOS) \
		-e GOARCH=$(BUILD_GOARCH) \
		$(DOCKER_GO_IMAGE) \
		go build -ldflags="$(LDFLAGS)" -o $(BINARY) .

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)

version:
	@echo $(VERSION)

COMPOSE_BASE = docker compose -f docker-compose.yml -f docker-compose.postgres.yml -f docker-compose.selfservice.yml
COMPOSE_EXTRA =
ifdef WITH_BROWSER
COMPOSE_EXTRA += -f docker-compose.browser.yml
endif
ifdef WITH_OTEL
COMPOSE_EXTRA += -f docker-compose.otel.yml
endif
ifdef WITH_SANDBOX
COMPOSE_EXTRA += -f docker-compose.sandbox.yml
endif
ifdef WITH_TAILSCALE
COMPOSE_EXTRA += -f docker-compose.tailscale.yml
endif
ifdef WITH_REDIS
COMPOSE_EXTRA += -f docker-compose.redis.yml
endif
ifdef WITH_CLAUDE_CLI
COMPOSE_EXTRA += -f docker-compose.claude-cli.yml
endif
COMPOSE = $(COMPOSE_BASE) $(COMPOSE_EXTRA)
UPGRADE = docker compose -f docker-compose.yml -f docker-compose.postgres.yml -f docker-compose.upgrade.yml

version-file:
	@echo $(VERSION) > VERSION

up: version-file
	$(COMPOSE) up -d --build
	$(UPGRADE) run --rm upgrade

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f goclaw

reset: version-file
	$(COMPOSE) down -v
	$(COMPOSE) up -d --build

test: check-go
	go test -race ./...

vet: check-go
	go vet ./...

check-web:
	cd ui/web && pnpm install --frozen-lockfile && pnpm build

dev:
	cd ui/web && pnpm dev

migrate:
	$(COMPOSE) run --rm goclaw migrate up

setup: check-go
	go mod download
	cd ui/web && pnpm install --frozen-lockfile

ci: build test vet check-web

# ── Desktop (Wails + SQLite) ──

desktop-dev: check-go check-wails
	cd ui/desktop && wails dev -tags sqliteonly

desktop-build: check-go check-wails
	cd ui/desktop && wails build -tags sqliteonly -ldflags="-s -w -X github.com/nextlevelbuilder/goclaw/cmd.Version=$(VERSION)"

desktop-dmg: check-go check-wails desktop-build
	@echo "Creating DMG..."
	rm -rf /tmp/goclaw-dmg-staging
	mkdir -p /tmp/goclaw-dmg-staging
	cp -R ui/desktop/build/bin/goclaw-lite.app /tmp/goclaw-dmg-staging/
	ln -s /Applications /tmp/goclaw-dmg-staging/Applications
	hdiutil create -volname "GoClaw Lite $(VERSION)" -srcfolder /tmp/goclaw-dmg-staging \
		-ov -format UDZO "goclaw-lite-$(VERSION)-darwin-$$(uname -m | sed 's/x86_64/amd64/').dmg"
	rm -rf /tmp/goclaw-dmg-staging
	@echo "DMG created: goclaw-lite-$(VERSION)-darwin-$$(uname -m | sed 's/x86_64/amd64/').dmg"
