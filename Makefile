# PiFiles — developer and operator commands.
#
#   make help     list every target
#   make dev      run backend and frontend with hot reload
#   make up       start the whole stack in Docker
#   make check    everything CI runs: build, lint, test

SHELL := /bin/bash
.DEFAULT_GOAL := help

# --- Configuration ---------------------------------------------------------

COMPOSE          := docker compose
COMPOSE_PROD     := docker compose -f docker-compose.yml -f docker-compose.prod.yml
BACKEND_DIR      := backend
FRONTEND_DIR     := frontend
EMBED_DIR        := $(BACKEND_DIR)/internal/web/dist

VERSION          ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "1.0.0-dev")
COMMIT           ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE       ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Pin the three above to their value now, at parse time. Left recursive, each
# would re-run its shell command every time it is referenced — and by then
# build-frontend has overwritten the tracked SPA placeholder, so `git describe`
# would report a working tree its own build had dirtied and stamp "-dirty" on
# an otherwise clean release. `?=` above still lets the environment override.
VERSION          := $(VERSION)
COMMIT           := $(COMMIT)
BUILD_DATE       := $(BUILD_DATE)
VERSION_PKG      := github.com/joshuanmoses/piCloud/backend/internal/version
LDFLAGS          := -s -w \
                    -X $(VERSION_PKG).Version=$(VERSION) \
                    -X $(VERSION_PKG).Commit=$(COMMIT) \
                    -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

# Disposable PostgreSQL for integration tests.
TEST_DB_CONTAINER := pifiles-test-postgres
TEST_DB_PORT      ?= 55433
TEST_DB_PASSWORD  := pifiles-test-password

.PHONY: help
help: ## Show this help
	@echo "PiFiles — available commands"
	@echo ""
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | sort \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""

# ===========================================================================
# Development
# ===========================================================================

.PHONY: dev
dev: ## Run backend and frontend with hot reload (needs a running database)
	@echo "Starting PostgreSQL, backend, and frontend..."
	@$(MAKE) --no-print-directory dev-db
	@trap 'kill 0' EXIT INT TERM; \
	  ( cd $(BACKEND_DIR) && APP_ENV=development \
	      DATABASE_HOST=localhost DATABASE_PORT=$(TEST_DB_PORT) \
	      DATABASE_PASSWORD=$(TEST_DB_PASSWORD) \
	      STORAGE_ROOT=$(PWD)/.dev-data \
	      LOG_FORMAT=console LOG_LEVEL=debug \
	      go run ./cmd/server ) & \
	  ( cd $(FRONTEND_DIR) && npm run dev ) & \
	  wait

.PHONY: dev-db
dev-db: ## Start a disposable PostgreSQL for local development
	@if [ -z "$$(docker ps -q -f name=^/$(TEST_DB_CONTAINER)$$)" ]; then \
	  docker rm -f $(TEST_DB_CONTAINER) >/dev/null 2>&1 || true; \
	  echo "Starting $(TEST_DB_CONTAINER) on port $(TEST_DB_PORT)..."; \
	  if ! err=$$(docker run -d --name $(TEST_DB_CONTAINER) \
	    -e POSTGRES_USER=pifiles -e POSTGRES_DB=pifiles \
	    -e POSTGRES_PASSWORD=$(TEST_DB_PASSWORD) \
	    -p $(TEST_DB_PORT):5432 \
	    --health-cmd "pg_isready -U pifiles" --health-interval 2s --health-retries 15 \
	    postgres:17-alpine 2>&1 >/dev/null); then \
	    echo ""; \
	    echo "  Could not start the test database:"; \
	    echo "    $$err"; \
	    echo ""; \
	    echo "  If port $(TEST_DB_PORT) is taken by something else, choose another:"; \
	    echo "    make test-integration TEST_DB_PORT=55444"; \
	    echo ""; \
	    docker rm -f $(TEST_DB_CONTAINER) >/dev/null 2>&1 || true; \
	    exit 1; \
	  fi; \
	  printf "Waiting for PostgreSQL"; \
	  for i in $$(seq 1 45); do \
	    if [ "$$(docker inspect -f '{{.State.Health.Status}}' $(TEST_DB_CONTAINER) 2>/dev/null)" = "healthy" ]; then \
	      echo " ready."; exit 0; \
	    fi; printf "."; sleep 1; \
	  done; \
	  echo " timed out."; docker logs --tail 30 $(TEST_DB_CONTAINER); exit 1; \
	else \
	  echo "$(TEST_DB_CONTAINER) is already running."; \
	fi

.PHONY: dev-db-stop
dev-db-stop: ## Stop and remove the development database
	@docker rm -f $(TEST_DB_CONTAINER) >/dev/null 2>&1 || true
	@echo "Removed $(TEST_DB_CONTAINER)."

# ===========================================================================
# Build
# ===========================================================================

.PHONY: build
build: build-frontend build-backend restore-placeholder ## Build the frontend and the server binary

# build-frontend overwrites internal/web/dist/index.html, which is the one file
# in that directory git tracks. The compile has already embedded the real build
# by this point, so putting the placeholder back leaves a correct binary and a
# clean working tree — and stops a built index.html being committed over the
# placeholder, which would make a fresh clone serve a blank page with no
# warning. See TestCommittedIndexIsThePlaceholder.
.PHONY: restore-placeholder
restore-placeholder:
	@git checkout -- $(EMBED_DIR)/index.html 2>/dev/null \
	  || echo "  note: could not restore the SPA placeholder (no git history yet)"

.PHONY: build-frontend
build-frontend: ## Build the web interface and embed it in the backend
	cd $(FRONTEND_DIR) && npm run build
	@rm -rf $(EMBED_DIR)
	@mkdir -p $(EMBED_DIR)
	@cp -r $(FRONTEND_DIR)/dist/. $(EMBED_DIR)/
	@echo "Embedded $$(find $(EMBED_DIR) -type f | wc -l) files into $(EMBED_DIR)"

.PHONY: build-backend
build-backend: ## Compile the server binary for this machine
	cd $(BACKEND_DIR) && CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o ../bin/pifiles ./cmd/server
	@echo "Built bin/pifiles ($(VERSION))"

.PHONY: build-pi
build-pi: build-frontend ## Cross-compile the server for Raspberry Pi (ARM64)
	cd $(BACKEND_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	  go build -trimpath -ldflags="$(LDFLAGS)" -o ../bin/pifiles-arm64 ./cmd/server
	@echo "Built bin/pifiles-arm64 ($(VERSION))"

.PHONY: clean
clean: ## Remove build output
	rm -rf bin/ $(FRONTEND_DIR)/dist $(FRONTEND_DIR)/coverage
	git checkout -- $(EMBED_DIR) 2>/dev/null || true
	cd $(BACKEND_DIR) && go clean -cache -testcache
	@echo "Cleaned."

# ===========================================================================
# Quality gates
# ===========================================================================

.PHONY: check
check: lint test ## Run every check that CI runs

.PHONY: test
test: test-backend test-frontend test-scripts ## Run all unit tests

.PHONY: test-scripts
test-scripts: ## Test the backup, restore, and installer safety properties
	@./scripts/test/backup_test.sh

.PHONY: test-backend
test-backend: ## Run Go unit tests
	cd $(BACKEND_DIR) && go test -race -count=1 ./...

.PHONY: test-frontend
test-frontend: ## Run frontend tests
	cd $(FRONTEND_DIR) && npm run test

.PHONY: test-integration
test-integration: dev-db ## Run tests that need a real PostgreSQL
	cd $(BACKEND_DIR) && \
	  PIFILES_TEST_DATABASE_HOST=localhost \
	  PIFILES_TEST_DATABASE_PORT=$(TEST_DB_PORT) \
	  PIFILES_TEST_DATABASE_USER=pifiles \
	  PIFILES_TEST_DATABASE_PASSWORD=$(TEST_DB_PASSWORD) \
	  go test -race -count=1 ./tests/... ./internal/...

.PHONY: bench
bench: dev-db ## Measure the hot paths against a real PostgreSQL
	@echo "These run on this machine, not a Pi. The ratios and shapes transfer;"
	@echo "the absolute numbers are optimistic. See docs/raspberry-pi.md."
	@echo ""
	cd $(BACKEND_DIR) && \
	  PIFILES_TEST_DATABASE_HOST=localhost \
	  PIFILES_TEST_DATABASE_PORT=$(TEST_DB_PORT) \
	  PIFILES_TEST_DATABASE_USER=pifiles \
	  PIFILES_TEST_DATABASE_PASSWORD=pifiles-test-password \
	  go test ./tests/ -run '^$$' -bench . -benchtime $(BENCHTIME) -benchmem

# Iterations rather than a duration: these hit a database, so a time-based
# budget produces wildly different sample counts between the cheap and the
# expensive benchmarks.
BENCHTIME ?= 100x

.PHONY: up-pi-limits
up-pi-limits: preflight ## Start the stack capped like a Raspberry Pi 4
	docker compose -f docker-compose.yml -f docker-compose.pi.yml up -d
	@echo ""
	@echo "Running with Pi-4 memory and CPU caps. This does not make this machine"
	@echo "slow — it catches the class of bug that only shows up under a cap,"
	@echo "such as something allocating in proportion to file size."
	@echo "Watch it with: docker stats"

.PHONY: test-coverage
test-coverage: ## Produce coverage reports for both halves
	cd $(BACKEND_DIR) && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1
	cd $(FRONTEND_DIR) && npm run test:coverage

.PHONY: lint
lint: lint-backend lint-frontend ## Lint everything

.PHONY: lint-backend
lint-backend: ## Vet and format-check the Go code
	cd $(BACKEND_DIR) && go vet ./...
	@unformatted=$$(cd $(BACKEND_DIR) && gofmt -l .); \
	  if [ -n "$$unformatted" ]; then \
	    echo "These files are not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	  fi
	@echo "Go code is clean."

.PHONY: lint-frontend
lint-frontend: ## Lint and type-check the frontend
	cd $(FRONTEND_DIR) && npm run typecheck && npm run lint

.PHONY: fmt
fmt: ## Format all code
	cd $(BACKEND_DIR) && gofmt -w .
	cd $(FRONTEND_DIR) && npm run lint:fix

.PHONY: tidy
tidy: ## Tidy Go module dependencies
	cd $(BACKEND_DIR) && go mod tidy

# ===========================================================================
# Database
# ===========================================================================

.PHONY: migrate
migrate: ## Apply pending migrations inside the running stack
	$(COMPOSE) exec app pifiles -migrate

.PHONY: migrate-local
migrate-local: dev-db ## Apply migrations against the development database
	cd $(BACKEND_DIR) && APP_ENV=development \
	  DATABASE_HOST=localhost DATABASE_PORT=$(TEST_DB_PORT) \
	  DATABASE_PASSWORD=$(TEST_DB_PASSWORD) \
	  STORAGE_ROOT=$(PWD)/.dev-data \
	  go run ./cmd/server -migrate

.PHONY: db-shell
db-shell: ## Open a psql shell in the running database
	@set -a; [ -f .env ] && . ./.env; set +a; \
	  $(COMPOSE) exec -e PGPASSWORD="$$DATABASE_PASSWORD" postgres \
	    psql -U "$${DATABASE_USER:-pifiles}" -d "$${DATABASE_NAME:-pifiles}"

.PHONY: db-query
db-query: ## Run one SQL statement, e.g. make db-query SQL='SELECT count(*) FROM users'
	@if [ -z "$(SQL)" ]; then echo "Usage: make db-query SQL='SELECT 1'"; exit 1; fi
	@set -a; [ -f .env ] && . ./.env; set +a; \
	  printf '%s\n' '$(SQL)' | $(COMPOSE) exec -T -e PGPASSWORD="$$DATABASE_PASSWORD" postgres \
	    psql -U "$${DATABASE_USER:-pifiles}" -d "$${DATABASE_NAME:-pifiles}"

.PHONY: db-check
db-check: ## Verify the database and filesystem agree
	@set -a; [ -f .env ] && . ./.env; set +a; \
	  $(COMPOSE) exec -T -e PGPASSWORD="$$DATABASE_PASSWORD" postgres \
	    psql -U "$${DATABASE_USER:-pifiles}" -d "$${DATABASE_NAME:-pifiles}" \
	    < scripts/consistency-check.sql

.PHONY: create-admin
create-admin: ## Create an administrator account interactively
	$(COMPOSE) exec app pifiles -create-admin

# ===========================================================================
# Docker
# ===========================================================================

.PHONY: docker-build
docker-build: ## Build the application image for this machine
	$(COMPOSE) build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  app

.PHONY: docker-build-pi
docker-build-pi: ## Build the application image for ARM64 (Raspberry Pi)
	docker buildx build \
	  --platform linux/arm64 \
	  -f deploy/docker/Dockerfile \
	  -t pifiles/app:$(VERSION)-arm64 \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  --load .

# ===========================================================================
# Installation, backup, and upgrade
# ===========================================================================

.PHONY: install
install: ## Guided first-time setup for a new machine
	@./scripts/install.sh

.PHONY: backup
backup: ## Back up the database, content, and configuration
	@./scripts/backup.sh $(DEST)

.PHONY: backup-verify
backup-verify: ## Check an existing backup is intact (BACKUP=path)
	@test -n "$(BACKUP)" || { echo "usage: make backup-verify BACKUP=/path/to/pifiles-YYYYmmdd-HHMMSS"; exit 1; }
	@./scripts/backup.sh --verify "$(BACKUP)"

.PHONY: restore
restore: ## Restore from a backup (BACKUP=path)
	@test -n "$(BACKUP)" || { echo "usage: make restore BACKUP=/path/to/pifiles-YYYYmmdd-HHMMSS"; exit 1; }
	@./scripts/restore.sh "$(BACKUP)"

.PHONY: upgrade
upgrade: ## Back up, pull the new version, restart, and verify
	@./scripts/upgrade.sh

.PHONY: preflight
preflight: ## Check that this machine is ready to run PiFiles
	@./scripts/preflight.sh

.PHONY: up
up: preflight ## Start the full stack
	$(COMPOSE) up -d --build
	@$(MAKE) --no-print-directory wait-healthy
	@$(MAKE) --no-print-directory url

.PHONY: up-prod
up-prod: preflight ## Start the full stack with production hardening
	$(COMPOSE_PROD) up -d --build
	@$(MAKE) --no-print-directory wait-healthy
	@$(MAKE) --no-print-directory url

.PHONY: down
down: ## Stop the stack, keeping all data
	$(COMPOSE) down

.PHONY: down-volumes
down-volumes: ## Stop the stack and DELETE the database volume
	@echo "This permanently deletes the PostgreSQL volume."
	@echo "User files under STORAGE_ROOT are NOT affected."
	@read -p "Type 'yes' to continue: " confirm && [ "$$confirm" = "yes" ] || (echo "Cancelled."; exit 1)
	$(COMPOSE) down -v

.PHONY: restart
restart: ## Restart the application container
	$(COMPOSE) restart app

.PHONY: logs
logs: ## Follow logs from every service
	$(COMPOSE) logs -f --tail=100

.PHONY: logs-app
logs-app: ## Follow application logs only
	$(COMPOSE) logs -f --tail=100 app

.PHONY: ps
ps: ## Show service status
	$(COMPOSE) ps

.PHONY: shell
shell: ## Open a shell in the application container
	$(COMPOSE) exec app sh

# ===========================================================================
# Operations
# ===========================================================================

.PHONY: check-env
check-env: ## Verify .env exists and carries the required secrets
	@if [ ! -f .env ]; then \
	  echo ""; \
	  echo "  No .env file found."; \
	  echo ""; \
	  echo "  Create one with:   ./scripts/generate-secrets.sh"; \
	  echo "  Or by hand:        cp .env.example .env  (then set the two secrets)"; \
	  echo ""; \
	  exit 1; \
	fi
	@missing=""; \
	  for key in APP_SECRET_KEY DATABASE_PASSWORD; do \
	    value=$$(grep -E "^$$key=" .env | head -1 | cut -d= -f2-); \
	    if [ -z "$$value" ]; then missing="$$missing $$key"; fi; \
	  done; \
	  if [ -n "$$missing" ]; then \
	    echo ""; \
	    echo "  These required secrets are empty in .env:$$missing"; \
	    echo "  Run ./scripts/generate-secrets.sh to fill them in."; \
	    echo ""; \
	    exit 1; \
	  fi

.PHONY: wait-healthy
wait-healthy: ## Wait until the application reports ready
	@printf "Waiting for PiFiles to become ready"
	@for i in $$(seq 1 60); do \
	  if $(COMPOSE) exec -T app wget --quiet --tries=1 --spider http://127.0.0.1:8080/health/ready 2>/dev/null; then \
	    echo " ready."; exit 0; \
	  fi; printf "."; sleep 2; \
	done; \
	echo " timed out."; \
	echo "Recent application logs:"; \
	$(COMPOSE) logs --tail=40 app; \
	exit 1

.PHONY: health
health: ## Print the health endpoint
	@$(COMPOSE) exec -T app wget -qO- http://127.0.0.1:8080/health

.PHONY: url
url: ## Print the URL to open
	@port="$${HTTP_PUBLIC_PORT:-80}"; \
	  if [ -f .env ]; then port=$$(grep -E '^HTTP_PUBLIC_PORT=' .env | cut -d= -f2- | tr -d ' '); fi; \
	  port=$${port:-80}; \
	  if [ "$$port" = "80" ]; then suffix=""; else suffix=":$$port"; fi; \
	  echo ""; \
	  echo "  PiFiles is running at:  http://localhost$$suffix"; \
	  echo ""

.PHONY: version
version: ## Print build metadata
	@echo "version:    $(VERSION)"
	@echo "commit:     $(COMMIT)"
	@echo "build date: $(BUILD_DATE)"
