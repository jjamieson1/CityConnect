.DEFAULT_GOAL := help
SHELL := /bin/bash

APP      := cityconnect
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build every binary into ./bin
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/$(APP)-api ./cmd/server
	go build -ldflags "$(LDFLAGS)" -o bin/ccadm      ./cmd/ccadm
	go build -ldflags "$(LDFLAGS)" -o bin/c2stub     ./cmd/c2stub

.PHONY: test
test: ## Run the Go test suite (no database or network needed)
	go test ./...

.PHONY: test-verbose
test-verbose: ## Run the tests with names and timings
	go test -v ./...

.PHONY: cover
cover: ## Produce a coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	@echo "Run 'go tool cover -html=coverage.out' to browse it."

.PHONY: lint
lint: ## Vet the Go code and type-check the SPA
	go vet ./...
	cd web && npx tsc --noEmit

.PHONY: web
web: ## Build the SPA for production
	cd web && npm ci && CC_BASE_PATH=/cityconnect/ npm run build

.PHONY: dev
dev: ## Start the whole development environment
	./scripts/dev.sh start

.PHONY: dev-stop
dev-stop: ## Stop the development environment
	./scripts/dev.sh stop

.PHONY: dev-restart
dev-restart: ## Restart the development environment
	./scripts/dev.sh restart

.PHONY: dev-status
dev-status: ## Show what is running
	./scripts/dev.sh status

.PHONY: dev-logs
dev-logs: ## Follow every development log
	./scripts/dev.sh logs -f

.PHONY: doctor
doctor: ## Diagnose the development environment
	./scripts/dev.sh doctor

.PHONY: seed
seed: ## Install the baseline configuration
	go run ./cmd/ccadm seed

.PHONY: demo
demo: ## Install sample data for a walkthrough
	go run ./cmd/ccadm demo

.PHONY: check-c2
check-c2: ## Print the C2 endpoints actually in use
	go run ./cmd/ccadm check-c2

.PHONY: verify-audit
verify-audit: ## Replay and verify the audit hash chain
	go run ./cmd/ccadm verify-audit

.PHONY: deploy
deploy: ## Deploy (reads deployment/deploy.env)
	./deployment/deploy.sh

.PHONY: clean
clean: ## Remove build output
	rm -rf bin web/dist coverage.out
