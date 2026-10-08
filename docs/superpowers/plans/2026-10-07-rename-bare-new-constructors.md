# Rename bare `New()` constructors to `New<Type>()` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every constructor named `New` becomes `New<StructName>`, always, even where the Go idiom (`ring.New`) would keep `New`. This is a deliberate project preference.

**Architecture:** Pure rename, no behaviour change. Use gopls rename (or `gofmt -r`) so every call site, including tests and `e2e` (build tag), is updated by the compiler's help. One commit per constructor.

**Tech Stack:** Go, gopls, `go build`, `go vet -tags e2e`.

**Spec:** none (user request, 2026-10-07). Does not touch `docs/superpowers/plans/2026-10-06-rename-constructor-cache.md`; do the two in separate sittings.

## Global Constraints

- No behaviour change; do not rename strings in logs, JSON or docs history.
- Do not edit old plans in `docs/superpowers/plans/`.

## Review Focus

- Call sites under the `e2e` build tag still compile: `go vet -tags e2e ./...`.
- Test files in the same package call the bare `New(` without a package prefix (`timeseries/timeseries_test.go:147,161`).
- Code outside this repo that imports these packages (check `go.mod` consumers) breaks on the rename.

## Inventory (found with `grep -rnE '^func New\(' --include='*.go'`)

| Constructor | Call sites | Decision |
| --- | --- | --- |
| `timeseries.New` -> `timeseries.NewTimeSeries` | 11 | **Rename.** Package already has `NewWithData`, `NewAggregate`, `NewContext`; bare `New` is ambiguous. |
| `constructor.New` -> `constructor.NewConstructor` | 7 | **Rename.** Stutters, accepted by preference. |
| `collector.New` -> `collector.NewCollector` | 1 | **Rename.** Package has many `NewXxx` (`NewMetricsBatch`, `NewGRPCLogsService`, ...). |
| `promql.New` -> `promql.NewClient` | 6 | **Rename.** Return type is `*Client`. |
| `ch/chtest.New` -> `chtest.NewEnv` | 14 | **Rename.** Returns `*Env`. |

## Task 1: Rename `timeseries.New`

**Files:**
- Modify: `timeseries/timeseries.go:29`, plus call sites (`grep -rn 'timeseries\.New(' .`, `timeseries/timeseries_test.go:147,161`)

- [ ] **Step 1:** `gopls rename -w timeseries/timeseries.go:29:6 NewTimeSeries`
- [ ] **Step 2:** `grep -rnE --include='*.go' '(^|[^.A-Za-z])New\(' timeseries` must show no remaining bare `New(`; fix any the tool missed.
- [ ] **Step 3:** `go build ./... && go vet -tags e2e ./... && go test ./timeseries/...` -> PASS
- [ ] **Step 4:** `git add -A timeseries . && git commit -m "refactor: rename timeseries.New to NewTimeSeries"` (stage only touched files)

## Task 2: Rename `constructor.New`, `collector.New`, `promql.New`, `chtest.New`

**Files:**
- Modify: `constructor/constructor.go:48`, `collector/collector.go:58`, `promql/client.go:34`, `ch/chtest/chtest.go:49` and call sites (`chtest.New(` has 14, in tests, some under `e2e` tag).

- [ ] **Step 1:** `gopls rename -w constructor/constructor.go:48:6 NewConstructor`
- [ ] **Step 2:** `gopls rename -w collector/collector.go:58:6 NewCollector`
- [ ] **Step 3:** `gopls rename -w promql/client.go:34:6 NewClient`
- [ ] **Step 4:** `gopls rename -w ch/chtest/chtest.go:49:6 NewEnv`
- [ ] **Step 5:** `go build ./... && go vet -tags e2e ./... && go test ./...` -> PASS
- [ ] **Step 6:** one commit per constructor (`git add` the touched files, `refactor: rename <pkg>.New to <NewName>`).

## Task 3: Verify

- [ ] **Step 1:** `grep -rnE --include='*.go' '^func New\(' .` -> no output.
- [ ] **Step 2:** `make test-e2e` -> a failure means a wrong rename, not a behaviour change.
