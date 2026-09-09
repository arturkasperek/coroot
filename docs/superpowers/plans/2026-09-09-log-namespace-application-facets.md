# Namespace and Application Log Facets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Overview Logs sidebar gains a Kubernetes **Namespace** facet (plus **Not applicable**) and **Application** shows short names (`express-demo`), with ClickHouse-accurate counts and filters.

**Architecture:** Same pattern as Source. Shared SQL expressions derive Namespace and Application from `ServiceName` (`/k8s...`) or `k8s.namespace.name`. `facetCountSQL` + `LogQuery.filters` use those expressions. Overview emits facet keys `Namespace` and `Application` instead of `service.name`. Vue `CORE_FACETS` matches. E2E inserts mixed agent/OTEL rows into ClickHouse and hits `/overview/logs`.

**Tech Stack:** Go, ClickHouse SQL (`otel_logs`), Vue 2, Node `node:test`, `//go:build e2e` against `make dev`.

**Spec:** `docs/superpowers/specs/2026-09-09-log-namespace-application-facets-design.md`

**Working directory:** all paths below are relative to `coroot/`.

## Global Constraints

- Do not change `AppLogs.vue` or `/api/project/{project}/app/{app}/logs`.
- No new CH tables, MVs, or HTTP endpoints.
- Raw `service.name` filters keep the existing `ServiceName =` path. Do not rewrite old chips.
- Namespace stored value for non-k8s logs is exactly `n/a`. UI label is **Not applicable**.
- Path `/k8s` (including `/k8s-cronjob/`) wins over `k8s.namespace.name`.
- Operators for Namespace and Application: `=` and `!=` only.
- Facet failures stay non-fatal. Tests first.
- Do not git commit unless the user explicitly asks (skip every Commit step otherwise).
- Do not add README or extra summary docs.

## File map

| File | Role |
|---|---|
| `clickhouse/logs_facets.go` | `logNamespaceExpr`, `logApplicationExpr`, `facetCountSQL` cases, Namespace `n/a` padding in `GetLogFacetCounts` |
| `clickhouse/logs.go` | `filters()` cases; `GetLogFilters` names/values |
| `clickhouse/logs_facets_test.go` | SQL + WHERE tests |
| `api/views/overview/logs.go` | `addFacet` `Namespace` + `Application`; drop `service.name` |
| `api/views/overview/logs_facets.go` | order; `completeNamespaceFacets` |
| `api/views/overview/logs_facets_test.go` | merge order + `n/a` |
| `front/src/utils/logQuickFilters.js` | CORE_FACETS, labels, display, fallback derive |
| `front/src/components/Logs.vue` | QueryBuilder ops `=`/`!=` |
| `front/src/components/QueryBuilder.vue` | format Namespace `n/a` (via `formatLogFilter`) |
| `front/src/views/Kubernetes.vue` | hide Namespace + Application |
| `front/tests/unit/logQuickFilters.test.js` | display + order + search |
| `e2e/clickhouse.go` | extra ResourceAttributes on fixture rows |
| `e2e/log_namespace_test.go` | ingest + API cases |
| `e2e/log_facets_test.go` | Application key + short names |

Shared SQL (put in `clickhouse/logs_facets.go`, same package as `logs.go`):

```go
const logNamespaceNA = "n/a"

func logNamespaceExpr() string {
	return `if(startsWith(ServiceName, '/k8s'), if(arrayElement(splitByChar('/', ServiceName), 3) = '', 'n/a', arrayElement(splitByChar('/', ServiceName), 3)), if(ResourceAttributes['k8s.namespace.name'] != '', ResourceAttributes['k8s.namespace.name'], if(LogAttributes['k8s.namespace.name'] != '', LogAttributes['k8s.namespace.name'], 'n/a')))`
}

func logApplicationExpr() string {
	return `if(startsWith(ServiceName, '/k8s'), nullIf(arrayElement(splitByChar('/', ServiceName), length(splitByChar('/', ServiceName))), ''), ServiceName)`
}
```

`splitByChar('/', '/k8s/coroot-dev/express-demo')` → `['', 'k8s', 'coroot-dev', 'express-demo']` (1-based index 3 = ns, last = app). Same indexes for `/k8s-cronjob/coroot-dev/backup`.

---

### Task 1: ClickHouse Namespace and Application SQL + filters

**Files:**
- Modify: `clickhouse/logs_facets.go`
- Modify: `clickhouse/logs.go` (`filters` switch ~317–358, `GetLogFilters` ~129–140)
- Test: `clickhouse/logs_facets_test.go`

**Interfaces:**
- Produces: `logNamespaceExpr() string`, `logApplicationExpr() string`, `logNamespaceNA`, `facetCountSQL("Namespace"|"Application")`, `LogQuery.filters` handling those names

- [ ] **Step 1: Write failing unit tests** in `clickhouse/logs_facets_test.go`:

```go
func TestFacetCountSQLNamespaceAndApplication(t *testing.T) {
	q, attr, ok := facetCountSQL("Namespace")
	require.True(t, ok)
	assert.Equal(t, "Namespace", *attr)
	assert.Contains(t, q, "startsWith(ServiceName, '/k8s')")
	assert.Contains(t, q, "k8s.namespace.name")
	assert.Contains(t, q, "'n/a'")
	assert.Contains(t, q, "GROUP BY 1")

	q, attr, ok = facetCountSQL("Application")
	require.True(t, ok)
	assert.Equal(t, "Application", *attr)
	assert.Contains(t, q, "startsWith(ServiceName, '/k8s')")
	assert.Contains(t, q, "LIMIT 1000")
}

func TestLogQueryFiltersNamespaceAndApplication(t *testing.T) {
	q := LogQuery{
		Ctx: timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		Filters: []LogFilter{
			{Name: "Namespace", Op: "=", Value: "coroot-dev"},
			{Name: "Application", Op: "=", Value: "express-demo"},
			{Name: "Severity", Op: "=", Value: "error"},
		},
	}
	where, _ := q.filters(nil)
	joined := strings.Join(where, " AND ")
	assert.Contains(t, joined, logNamespaceExpr())
	assert.Contains(t, joined, logApplicationExpr())
	assert.Contains(t, joined, "SeverityNumber")

	where, _ = q.filters(strPtr("Namespace"))
	joined = strings.Join(where, " AND ")
	assert.NotContains(t, joined, logNamespaceExpr())
	assert.Contains(t, joined, logApplicationExpr())

	q.Filters = []LogFilter{{Name: "Namespace", Op: "!=", Value: "n/a"}}
	where, _ = q.filters(nil)
	assert.Contains(t, strings.Join(where, " AND "), "NOT (")
}
```

- [ ] **Step 2: Run tests, confirm fail**

Run: `go test ./clickhouse -count=1 -run 'TestFacetCountSQLNamespaceAndApplication|TestLogQueryFiltersNamespaceAndApplication'`

Expected: FAIL (`facetCountSQL` unknown / no Namespace case)

- [ ] **Step 3: Implement expressions + `facetCountSQL` + `filters` + suggest names**

In `logs_facets.go` add the two expr helpers and:

```go
case "Namespace":
	return "SELECT " + logNamespaceExpr() + ", count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY 1", &attr, true
case "Application":
	return "SELECT " + logApplicationExpr() + " AS v, count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY v HAVING v != '' ORDER BY count(1) DESC, v LIMIT 1000", &attr, true
```

In `GetLogFacetCounts`, after scanning default/Source-style string rows for `"Namespace"`: collect by value, append `{Value: logNamespaceNA, Count: 0}` if missing, sort non-`n/a` by count desc then name, put `n/a` last.

In `logs.go` `filters` switch, before `default:`:

```go
case "Namespace", "Application":
	exprFn := logNamespaceExpr
	if name == "Application" {
		exprFn = logApplicationExpr
	}
	base := exprFn()
	for j, a := range attrs {
		v := fmt.Sprintf("derived_%s_%d_%d", name, i, j)
		switch a.Op {
		case "=":
			ors = append(ors, fmt.Sprintf("(%s) = @%s", base, v))
			args = append(args, clickhouse.Named(v, a.Value))
		case "!=":
			ands = append(ands, fmt.Sprintf("NOT ((%s) = @%s)", base, v))
			args = append(args, clickhouse.Named(v, a.Value))
		}
	}
```

`GetLogFilters` empty name list: append `"Namespace", "Application"`. Cases:

```go
case "Namespace", "Application":
	expr := logNamespaceExpr()
	if name == "Application" {
		expr = logApplicationExpr()
	}
	q = "SELECT DISTINCT " + expr
```

- [ ] **Step 4: Re-run unit tests**

Run: `go test ./clickhouse -count=1`

Expected: PASS

- [ ] **Step 5: Commit** (skip unless user asked)

---

### Task 2: Overview facet groups

**Files:**
- Modify: `api/views/overview/logs.go` (~128–132)
- Modify: `api/views/overview/logs_facets.go`
- Test: `api/views/overview/logs_facets_test.go`

**Interfaces:**
- Consumes: facet keys `Namespace`, `Application` from Task 1
- Produces: `facetGroupsFromMerged` order `Source, Severity, Cluster, Namespace, Application, host.name`; no core `service.name`

- [ ] **Step 1: Write failing tests** in `logs_facets_test.go`:

```go
func TestFacetGroupsFromMergedNamespaceAndApplication(t *testing.T) {
	groups := facetGroupsFromMerged(map[string]map[string]uint64{
		"Namespace":   {"coroot-dev": 32},
		"Application": {"express-demo": 23, "nextjs-demo": 7},
		"Severity":    {"info": 10},
		"Source":      {"agent": 20},
	})
	keys := make([]string, len(groups))
	for i, g := range groups {
		keys[i] = g.Key
	}
	assert.Equal(t, []string{"Source", "Severity", "Namespace", "Application"}, keys)

	by := map[string]clickhouse.FacetGroup{}
	for _, g := range groups {
		by[g.Key] = g
	}
	assert.Equal(t, uint64(32), by["Namespace"].Values[0].Count) // after complete: n/a present
	var hasNA bool
	for _, v := range by["Namespace"].Values {
		if v.Value == "n/a" {
			hasNA = true
			assert.Equal(t, uint64(0), v.Count)
		}
	}
	assert.True(t, hasNA)
	assert.Equal(t, "express-demo", by["Application"].Values[0].Value)
}
```

If `completeNamespaceFacets` lives in overview, the test asserts `n/a` here. If padding happens only in `GetLogFacetCounts`, still pad in `completeNamespaceFacets` so merge across clusters cannot drop `n/a`.

- [ ] **Step 2: Run test, confirm fail**

Run: `go test ./api/views/overview -count=1 -run TestFacetGroupsFromMergedNamespaceAndApplication`

Expected: FAIL (order still has `service.name`, no Namespace)

- [ ] **Step 3: Implement**

`logs.go`:

```go
addFacet(ch, "Namespace")
addFacet(ch, "Application")
// remove addFacet(ch, "service.name")
```

`logs_facets.go` order:

```go
order := []string{"Source", "Severity", "Cluster", "Namespace", "Application", "host.name"}
```

Add `completeNamespaceFacets`: always emit all discovered ns (count desc, name asc) then `n/a` (0 if absent). Call it in the `Namespace` switch like Source.

- [ ] **Step 4: Run**

Run: `go test ./api/views/overview ./clickhouse -count=1`

Expected: PASS (update `TestFacetGroupsFromMergedKeepsClusterRowsAndSorts` if it still expects `service.name` key)

- [ ] **Step 5: Commit** (skip unless user asked)

---

### Task 3: Frontend sidebar + chips

**Files:**
- Modify: `front/src/utils/logQuickFilters.js`
- Modify: `front/src/components/Logs.vue` (`qbGet` ops ~368–372)
- Modify: `front/src/components/QueryBuilder.vue` (already uses `formatLogFilter` / `displaySourceName`; Namespace goes through `formatLogFilter`)
- Modify: `front/src/views/Kubernetes.vue` (~17)
- Test: `front/tests/unit/logQuickFilters.test.js`

**Interfaces:**
- Consumes: facet keys `Namespace`, `Application`; value `n/a`
- Produces: `displayNamespaceName`, CORE_FACETS keys, local search on Namespace + Application

- [ ] **Step 1: Write failing frontend tests**

```js
test('displays Namespace n/a as Not applicable', async () => {
    const { displayNamespaceName, formatLogFilter } = await loadLogQuickFilters();
    assert.equal(displayNamespaceName('n/a'), 'Not applicable');
    assert.equal(displayNamespaceName('coroot-dev'), 'coroot-dev');
    assert.equal(formatLogFilter({ name: 'Namespace', op: '=', value: 'n/a' }), 'Namespace = Not applicable');
});

test('Application facet uses short names from backend', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters([], {
        facets: [
            { key: 'Application', values: [{ value: 'express-demo', count: 40 }] },
            { key: 'Namespace', values: [{ value: 'coroot-dev', count: 32 }, { value: 'n/a', count: 0 }] },
        ],
    });
    assert.deepEqual(
        groups.map((g) => g.key),
        ['Source', 'Severity', 'Cluster', 'Namespace', 'Application', 'Host'].filter((k) =>
            groups.some((g) => g.key === k),
        ),
    );
    const apps = groups.find((g) => g.key === 'Application');
    assert.equal(apps.values[0].value, 'express-demo');
    assert.equal(apps.values[0].label, 'express-demo');
    const ns = groups.find((g) => g.key === 'Namespace');
    assert.equal(ns.values.find((v) => v.value === 'n/a').label, 'Not applicable');
});

test('group search is available for Namespace and Application', async () => {
    const { groupHasLocalSearch } = await loadLogQuickFilters();
    assert.equal(groupHasLocalSearch('Namespace'), true);
    assert.equal(groupHasLocalSearch('Application'), true);
    assert.equal(groupHasLocalSearch('service.name'), false);
});

test('falls back to deriving Namespace and Application from attributes', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(
        [
            { attributes: { 'service.name': '/k8s/coroot-dev/express-demo' } },
            { attributes: { 'service.name': 'checkout', 'k8s.namespace.name': 'coroot-dev' } },
            { attributes: { 'service.name': 'ollama' } },
        ],
        {},
    );
    const ns = groups.find((g) => g.key === 'Namespace');
    assert.equal(ns.values.find((v) => v.value === 'coroot-dev').count, 2);
    assert.equal(ns.values.find((v) => v.value === 'n/a').count, 1);
    const apps = groups.find((g) => g.key === 'Application');
    assert.equal(apps.values.find((v) => v.value === 'express-demo').count, 1);
    assert.equal(apps.values.find((v) => v.value === 'checkout').count, 1);
});
```

Update existing tests that expect `service.name` as the Application group key (`puts Source before Severity`, `groupHasLocalSearch('service.name')`, backendFacets key `service.name`, `does not mix... service.name`).

New order expectation: `['Source', 'Severity', 'Cluster', 'Namespace', 'Application', 'host.name']` when those groups have values.

- [ ] **Step 2: Run frontend tests, confirm new ones fail**

Run: `cd front && node --test tests/unit/logQuickFilters.test.js`

Expected: FAIL on missing `displayNamespaceName` / Application key

- [ ] **Step 3: Implement JS + Vue**

`CORE_FACETS`:

```js
{ key: 'Namespace', label: 'Namespace', from: 'namespace' },
{ key: 'Application', label: 'Application', from: 'application' },
```

Remove `{ key: 'service.name', ... }`.

`LOCAL_SEARCH_FACETS`: `'Namespace', 'Application', 'host.name'`.

```js
export function displayNamespaceName(value) {
    return String(value) === 'n/a' ? 'Not applicable' : String(value || '');
}
```

`facetLabel`: Namespace → `displayNamespaceName`; Application → value as-is (already short). Keep `displayServiceName` for raw `service.name` chips.

`facetValue` for `from === 'namespace'` / `'application'`:

```js
function k8sParts(svc) {
    const s = String(svc || '');
    if (!s.startsWith('/k8s')) return null;
    const parts = s.split('/').filter(Boolean); // ['k8s'| 'k8s-cronjob', ns, ...app]
    if (parts.length < 2) return null;
    return parts;
}
// namespace: parts[1] or attributes['k8s.namespace.name'] or 'n/a'
// application: k8s → parts[parts.length-1], else service.name
```

`buildStableLogQuickFilters` labels: `Namespace`, `Application` (drop `'service.name': 'Application'` or keep for old chips in the sidebar — spec: sidebar uses Application; old `service.name` filters may still show a group if label map includes it. Keep `'service.name': 'Application'` so an old chip still appears).

`Logs.vue` `qbGet`:

```js
case 'Namespace':
case 'Application':
    this.qb.items = ['=', '!='];
    break;
```

`Kubernetes.vue`:

```js
:hidden-attributes="['service.name', 'Source', 'Namespace', 'Application']"
```

- [ ] **Step 4: Re-run frontend tests**

Run: `cd front && node --test tests/unit/logQuickFilters.test.js`

Expected: PASS

- [ ] **Step 5: Commit** (skip unless user asked)

---

### Task 4: E2E ingest helper + namespace/application cases

**Files:**
- Modify: `e2e/clickhouse.go` (`logFixtureRow`)
- Create: `e2e/log_namespace_test.go`
- Modify: `e2e/log_facets_test.go` (facet key `Application`, values `express-demo` / `nextjs-demo`)

**Interfaces:**
- Consumes: overview filters `Namespace`, `Application`, `Source`; insert helper
- Produces: `TestOverviewLogNamespaceFilters`

- [ ] **Step 1: Extend `logFixtureRow`**

```go
type logFixtureRow struct {
	Count               int
	SeverityText        string
	SeverityNumber      int
	ServiceName         string
	Host                string
	Body                string
	ResourceAttributes  map[string]string // merged on top of service.name + host.name
}
```

When building `ResourceAttributes`, start with `service.name` + `host.name`, then copy `s.ResourceAttributes` (so `k8s.namespace.name` can be set without changing `ServiceName`).

Existing `insertFacetFixture` callers stay valid (nil map).

- [ ] **Step 2: Write `e2e/log_namespace_test.go`** (`//go:build e2e`)

Use the spec fixture (61 rows). Isolation `e2e.facets = token`. `waitUntil` until Cluster total is 61.

Subtests exactly as spec:

| query extra filters | expect Cluster total | notes |
|---|---|---|
| none | 61 | NS coroot-dev=32, kube-system=15, n/a=14 |
| Application=express-demo | 32 | |
| Namespace=coroot-dev + Application=express-demo | 23 | |
| Namespace=kube-system + Application=express-demo | 4 | |
| Namespace=n/a + Application=express-demo | 5 | |
| Namespace=coroot-dev + Source=agent | 24 | |
| Namespace != coroot-dev | 29 | |
| Namespace=coroot-dev (self-exclusion) | 24 matching rows | Namespace facet still 32/15/14 |
| Namespace=kube-system + Application=nextjs-demo | 0 | |

Assert `assertFacet` + `assertEntrySources`-style checks: for k8s agent rows `service.name` starts with `/k8s`; for OTEL-with-attr `service.name==express-demo` and attributes contain namespace.

Cronjob row: `ServiceName=/k8s-cronjob/coroot-dev/backup`.

OTEL-with-NS row: `ServiceName=express-demo`, `ResourceAttributes: {"k8s.namespace.name": "coroot-dev"}`.

- [ ] **Step 3: Run e2e (will fail until Task 1–2 are on the running backend)**

Run from `coroot/`:

```bash
make test-e2e TestOverviewLogNamespaceFilters
```

Expected after Tasks 1–2 are live in Tilt: PASS. If backend not rebuilt, FAIL on missing Namespace facets / totals.

Also run:

```bash
make test-e2e TestOverviewLogFacetCounts
```

Update that test: `assertFacet(..., "Application", "express-demo", 40)` etc. Filters that still use raw `service.name` stay; Application counts **are** narrowed by `service.name` (different filter name — not exclude-self). Adjust subtests that expected both apps visible while filtering one `service.name`.

- [ ] **Step 4: Commit** (skip unless user asked)

---

## Spec coverage

| Spec item | Task |
|---|---|
| Namespace derivation path / attr / n/a | 1 |
| Application last segment vs full ServiceName | 1 |
| filters `=`/`!=`, exclude-self | 1 |
| GetLogFilters names | 1 |
| addFacet + order, drop service.name core facet | 2 |
| complete n/a | 2 |
| CORE_FACETS, labels, search, chips | 3 |
| Kubernetes hide | 3 |
| E2E fixture + 9 cases | 4 |
| Update existing facets e2e | 4 |
| No AppLogs / no schema | constraints |

## Self-review

- No TBD. SQL expressions are written out. E2E numbers match the spec (32/15/14, 23, 4, 5, 24, 29, 0).
- Existing `TestOverviewLogFacetCounts` **must** change Application key; called out in Task 4 so it does not silently fail.
- Commit steps skipped unless the user asks.
