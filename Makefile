UI_PATH = front
SHELL := /bin/bash

.PHONY: all
all: lint test

.PHONY: lint
lint: go-lint ui-lint

.PHONY: test
test: go-test ui-test

.PHONY: go-lint
go-lint: go-mod go-vet go-fmt go-imports

.PHONY: go-mod
go-mod:
	go mod tidy

.PHONY: go-vet
go-vet:
	go vet ./...

.PHONY: go-fmt
go-fmt:
	gofmt -w .

.PHONY: go-imports
go-imports:
	go install golang.org/x/tools/cmd/goimports@latest
	goimports -w .

.PHONY: go-test
go-test:
	go test $$(go list ./... | grep -v '/e2e$$')
	bash scripts/dev/node-agent-local-dir.test.sh
	bash scripts/dev/load-env.test.sh
	bash scripts/dev/check-docker-remote.test.sh

.PHONY: test-e2e
test-e2e: ## E2E tests against the running make-dev cluster
	@bash scripts/dev/k8s-dev-tools.sh
	@eval "$$(bash scripts/dev/load-env.sh --export)"; \
	  go test -tags e2e -count=1 -timeout 5m ./e2e/...

.PHONY: help
help: ## Show common targets
	@echo "  make dev        Tilt into KUBERNETES_CONTEXT_NAME from .env (cluster must already exist)"
	@echo "  make down       Tilt down + remove the coroot-dev namespace (keeps the cluster)"
	@echo "  make test       Go and UI unit tests"
	@echo "  make test-e2e   E2E against the make-dev cluster (cluster must already be up)"
	@echo "  make lint       Go + UI linters"

.PHONY: dev
dev: ## Tilt (in-cluster backend/frontend) against .env KUBERNETES_CONTEXT_NAME
	@bash scripts/dev/dev.sh

.PHONY: down
down: ## Tilt down + delete in-cluster dev namespace (keeps the cluster)
	@bash scripts/dev/dev-down.sh

.PHONY: dev-down
dev-down: down

.PHONY: ui-test
ui-test: npm-test

.PHONY: npm-test
npm-test: npm-install
	cd $(UI_PATH) && npm run test:unit

.PHONY: ui-lint
ui-lint: npm-install npm-lint npm-fmt

.PHONY: npm-install
npm-install:
	cd $(UI_PATH) && npm ci

.PHONY: npm-lint
npm-lint:
	cd $(UI_PATH) && npm run lint

.PHONY: npm-fmt
npm-fmt:
	cd $(UI_PATH) && npm run fmt
