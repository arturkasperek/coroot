UI_PATH = front
SHELL := /bin/bash
KIND_CLUSTER_NAME := coroot-dev

.PHONY: all
all: lint test

.PHONY: lint
lint: go-lint ui-lint

.PHONY: test
test: go-test

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
	go test ./...

.PHONY: help
help: ## Show common targets
	@echo "  make dev        kind + Tilt: Coroot + demo Next.js/Express with OTEL (http://localhost:18080)"
	@echo "  make dev-down   Tilt down + remove the coroot-dev namespace (keeps the cluster)"
	@echo "  make dev-clean  Delete the kind cluster"
	@echo "  make test       Go tests"
	@echo "  make lint       Go + UI linters"

.PHONY: dev
dev: ## Bootstrap kind (if needed) + Tilt (in-cluster backend/frontend)
	@bash scripts/dev/dev.sh

.PHONY: dev-down
dev-down: ## Tilt down + delete in-cluster dev namespace (keeps kind)
	@bash scripts/dev/k8s-dev-tools.sh
	@eval "$$(bash scripts/dev/kind-dev-kubeconfig.sh --export)"; \
	  tilt down --context kind-$(KIND_CLUSTER_NAME) || true; \
	  kubectl delete namespace coroot-dev --ignore-not-found; \
	  kubectl delete clusterrolebinding coroot-dev-cluster-agent --ignore-not-found; \
	  kubectl delete clusterrole coroot-dev-cluster-agent --ignore-not-found

.PHONY: dev-clean
dev-clean: ## Delete the kind cluster (wipes Prometheus/ClickHouse data)
	kind delete cluster --name $(KIND_CLUSTER_NAME)

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
