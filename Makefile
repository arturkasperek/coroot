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
test-e2e: ## E2E tests against the running make-dev cluster. Optional: make test-e2e TestOverviewLogSourceFilters
	@bash scripts/dev/k8s-dev-tools.sh
	@eval "$$(bash scripts/dev/load-env.sh --export)"; \
	  go test -tags e2e -count=1 -timeout 5m ./e2e/... $(if $(E2E_RUN),-run "$(E2E_RUN)")

ifeq (test-e2e,$(firstword $(MAKECMDGOALS)))
E2E_RUN_ARG := $(wordlist 2,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
ifneq ($(E2E_RUN_ARG),)
$(eval $(E2E_RUN_ARG):;@:)
endif
endif
E2E_RUN := $(or $(RUN),$(E2E_RUN_ARG))

ifeq (seed,$(firstword $(MAKECMDGOALS)))
SEED_ARGS := $(wordlist 2,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
$(eval $(SEED_ARGS):;@:)
endif

.PHONY: seed
seed: ## Seed ClickHouse with demo logs: make seed <days> <thousands-per-day>  (1000 = 1e6 logs/day)
	@if [ -z "$(SEED_ARGS)" ]; then \
	  echo "usage: make seed <days> <thousands-of-logs-per-day>"; \
	  echo "example: make seed 7 1000   # 7 days, 1 000 000 logs/day"; \
	  exit 2; \
	fi
	@eval "$$(bash scripts/dev/load-env.sh --export)"; \
	  go run ./scripts/chseed $(SEED_ARGS)

.PHONY: help
help: ## Show common targets
	@echo "  make dev        Tilt into KUBERNETES_CONTEXT_NAME from .env (cluster must already exist)"
	@echo "  make down       Tilt down + remove the coroot-dev namespace (keeps the cluster)"
	@echo "  make seed 7 1000  Seed ClickHouse: 1e6 agent-style logs/day for 7 days (express/nextjs-demo)"
	@echo "  make test       Go and UI unit tests"
	@echo "  make test-e2e [RUN=regex]   E2E against the make-dev cluster (optional -run filter)"
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
