# Log Facet Counts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Overview Logs sidebar shows exact ClickHouse `count()` for Severity, Cluster, Application, and Host over the full selected time range, with Groundcover-style cross-filters.

**Architecture:** The existing overview logs Search request grows a `facets` array. For each ClickHouse client, four aggregations run in parallel with histogram + `GetLogs`. Each aggregation uses `LogQuery.filters(&groupName)` so the group's own filters are excluded. Overview merges counts across clients. The Vue sidebar reads `facets` for the four core keys and still counts dynamic attributes from loaded entries.

**Tech Stack:** Go, ClickHouse SQL (`otel_logs`), Vue 2, Node test runner (`front/tests/unit`), `//go:build e2e` against `make dev`.

**Spec:** `docs/superpowers/specs/2026-09-08-log-facet-counts-design.md`

**Working directory:** all paths below are relative to `coroot/`.

## Global Constraints

- Exact `count()` over the request `from`/`to`. Do not reuse `maxLogFilterScanWindow` (1 hour) for facet counts.
- Only these JSON keys: `Severity`, `Cluster`, `service.name`, `host.name`. No CH aggregations for dynamic attributes.
- Do not change `AppLogs.vue` or `/api/project/{project}/app/{app}/logs`.
- No new CH tables, MVs, or `/facets` endpoint.
- Histogram and log table stay fully filtered (including Severity).
- Facet query failures are non-fatal (log + omit that group). Histogram/`GetLogs` failures stay fatal.
- Always emit Severity values `unknown`, `info`, `warning`, `error` (missing bucket → `count: 0`).
- Application/Host: skip empty strings, `ORDER BY count DESC`, `LIMIT 1000`.
- Host value: `if(LogAttributes[@attr] != '', LogAttributes[@attr], ResourceAttributes[@attr])` so a row with the key in both maps is not double-counted.
- Cluster is `SELECT count()` per CH client; value is `project.Name`. Cluster filter is applied in Go via `LogFilter.Matches`, not SQL. Cluster facet must still query clients that fail `Matches` (exclude-self).
- `facets` omitted/`null` → frontend falls back to counting entries. Non-null `facets` array → never mix backend Severity with client-side Application.
- Tests first. Do not git commit unless the user explicitly asks (skip every Commit step otherwise).
- Do not add README or extra summary docs.

## File map

| File | Role |
|---|---|
| `clickhouse/logs.go` | Existing `LogQuery.filters(*string)` (exclude-self). Unchanged contract. |
| `clickhouse/logs_facets.go` | `FacetValue`, `FacetGroup`, `GetLogFacetCounts`, SQL builders. |
| `clickhouse/logs_facets_test.go` | Exclude-self WHERE tests + SQL fragment tests + empty-value skip. |
| `api/views/overview/logs.go` | `Logs.Facets`, parallel fetch, Cluster skip vs exclude-self. |
| `api/views/overview/logs_facets.go` | `completeSeverityFacets`, `mergeFacetValues`, `facetGroupsFromMerged`. |
| `api/views/overview/logs_facets_test.go` | Merge + severity zeros, no live CH. |
| `front/src/utils/logQuickFilters.js` | Prefer `options.facets` for core keys. |
| `front/src/components/LogQuickFilters.vue` | New `facets` prop. |
| `front/src/components/Logs.vue` | Pass `view.facets`; keep chart colors via `severityFacets`. |
| `front/tests/unit/logQuickFilters.test.js` | Backend counts, fallback, exclude-self display. |
| `e2e/coroot.go` | `fetchOverviewLogs`. |
| `e2e/clickhouse.go` | HTTP insert/delete/resolve DB for fixture. |
| `e2e/log_facets_test.go` | Ingest 50 rows, assert API counts. |

---

### Task 1: ClickHouse facet query helpers (TDD)

**Files:**
- Create: `clickhouse/logs_facets.go`
- Create: `clickhouse/logs_facets_test.go`
- Test: `clickhouse/logs.go` (`LogQuery.filters` — read only)

**Interfaces:**
- Consumes: `LogQuery.filters(attr *string) ([]string, []any)`
- Produces:
  - `type FacetValue struct { Value string \`json:"value"\`; Count uint64 \`json:"count"\` }`
  - `type FacetGroup struct { Key string \`json:"key"\`; Values []FacetValue \`json:"values"\` }`
  - `func (c *Client) GetLogFacetCounts(ctx context.Context, query LogQuery, name string) ([]FacetValue, error)`
  - `const maxLogFacetValues = 1000`
  - `func facetCountSQL(name string) (query string, attr *string, ok bool)`

- [ ] **Step 1: Write failing tests**

Create `clickhouse/logs_facets_test.go`:

```go
package clickhouse

import (
	"strings"
	"testing"

	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogQuery() LogQuery {
	return LogQuery{
		Ctx: timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		Filters: []LogFilter{
			{Name: "Severity", Op: "=", Value: "error"},
			{Name: "service.name", Op: "=", Value: "/k8s/coroot-dev/express-demo"},
			{Name: "host.name", Op: "=", Value: "node-a"},
			{Name: "e2e.facets", Op: "=", Value: "token"},
		},
	}
}

func TestLogQueryFiltersExcludeNamedAttr(t *testing.T) {
	q := testLogQuery()
	joined := func(attr string) string {
		where, _ := q.filters(&attr)
		return strings.Join(where, " AND ")
	}

	sev := joined("Severity")
	assert.NotContains(t, sev, "SeverityNumber")
	assert.Contains(t, sev, "ServiceName")
	assert.Contains(t, sev, "LogAttributes")
	assert.Contains(t, sev, "e2e.facets")

	svc := joined("service.name")
	assert.Contains(t, svc, "SeverityNumber")
	assert.NotContains(t, svc, "ServiceName =")
	assert.Contains(t, svc, "e2e.facets")

	host := joined("host.name")
	assert.Contains(t, host, "SeverityNumber")
	assert.Contains(t, host, "ServiceName")
	assert.NotContains(t, host, "host.name")
	assert.Contains(t, host, "e2e.facets")
}

func TestFacetCountSQL(t *testing.T) {
	q, attr, ok := facetCountSQL("Severity")
	require.True(t, ok)
	require.NotNil(t, attr)
	assert.Equal(t, "Severity", *attr)
	assert.Contains(t, q, "multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1)")
	assert.Contains(t, q, "GROUP BY 1")
	assert.NotContains(t, q, "LIMIT")

	q, attr, ok = facetCountSQL("service.name")
	require.True(t, ok)
	assert.Equal(t, "service.name", *attr)
	assert.Contains(t, q, "SELECT ServiceName, count(1)")
	assert.Contains(t, q, "HAVING ServiceName != ''")
	assert.Contains(t, q, "LIMIT 1000")

	q, attr, ok = facetCountSQL("host.name")
	require.True(t, ok)
	assert.Equal(t, "host.name", *attr)
	assert.Contains(t, q, "if(LogAttributes[@attr] != '', LogAttributes[@attr], ResourceAttributes[@attr])")
	assert.Contains(t, q, "HAVING v != ''")
	assert.Contains(t, q, "LIMIT 1000")

	q, attr, ok = facetCountSQL("Cluster")
	require.True(t, ok)
	assert.Equal(t, "Cluster", *attr)
	assert.Contains(t, q, "SELECT count(1)")
	assert.NotContains(t, q, "GROUP BY")

	_, _, ok = facetCountSQL("k8s.pod.name")
	assert.False(t, ok)
}
```

- [ ] **Step 2: Run tests — expect FAIL**

Run from `coroot/`:

```bash
go test ./clickhouse/ -count=1 -run 'TestLogQueryFiltersExcludeNamedAttr|TestFacetCountSQL'
```

Expected: `TestLogQueryFiltersExcludeNamedAttr` PASS (filters already exist). `TestFacetCountSQL` FAIL: `facetCountSQL` undefined.

- [ ] **Step 3: Implement SQL helpers + GetLogFacetCounts**

Create `clickhouse/logs_facets.go`:

```go
package clickhouse

import (
	"context"
	"fmt"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/model"
)

const maxLogFacetValues = 1000

type FacetValue struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

type FacetGroup struct {
	Key    string       `json:"key"`
	Values []FacetValue `json:"values"`
}

func facetCountSQL(name string) (string, *string, bool) {
	attr := name
	switch name {
	case "Severity":
		return "SELECT multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1), count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY 1", &attr, true
	case "service.name":
		return "SELECT ServiceName, count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY ServiceName HAVING ServiceName != '' ORDER BY count(1) DESC, ServiceName LIMIT 1000", &attr, true
	case "host.name":
		return "SELECT if(LogAttributes[@attr] != '', LogAttributes[@attr], ResourceAttributes[@attr]) AS v, count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY v HAVING v != '' ORDER BY count(1) DESC, v LIMIT 1000", &attr, true
	case "Cluster":
		return "SELECT count(1) FROM @@table_otel_logs@@ WHERE %s", &attr, true
	default:
		return "", nil, false
	}
}

func (c *Client) GetLogFacetCounts(ctx context.Context, query LogQuery, name string) ([]FacetValue, error) {
	sqlFmt, attr, ok := facetCountSQL(name)
	if !ok {
		return nil, fmt.Errorf("unsupported log facet %q", name)
	}
	where, args := query.filters(attr)
	q := fmt.Sprintf(sqlFmt, strings.Join(where, " AND "))
	if name == "host.name" {
		args = append(args, ch.Named("attr", name))
	}
	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	switch name {
	case "Cluster":
		var n uint64
		if rows.Next() {
			if err = rows.Scan(&n); err != nil {
				return nil, err
			}
		}
		return []FacetValue{{Value: c.project.Name, Count: n}}, nil
	case "Severity":
		by := map[string]uint64{}
		var sev int64
		var n uint64
		for rows.Next() {
			if err = rows.Scan(&sev, &n); err != nil {
				return nil, err
			}
			by[model.Severity(sev).String()] = n
		}
		out := make([]FacetValue, 0, 4)
		for _, label := range []string{"unknown", "info", "warning", "error"} {
			out = append(out, FacetValue{Value: label, Count: by[label]})
		}
		return out, nil
	default:
		var out []FacetValue
		var v string
		var n uint64
		for rows.Next() {
			if err = rows.Scan(&v, &n); err != nil {
				return nil, err
			}
			if v == "" {
				continue
			}
			out = append(out, FacetValue{Value: v, Count: n})
		}
		return out, nil
	}
}
```

If `TestLogQueryFiltersExcludeNamedAttr` fails because `host.name` still appears as map key `@attr` in SQL, keep the test asserting the **filter name** is skipped (`f.Name == *attr` continue). The WHERE will still contain `LogAttributes[@attr_name_…]` for `e2e.facets`. Do not require the literal `host.name` to be absent from the whole string if ClickHouse param names collide; assert using the same style as Severity (`host.name` filter uses `LogAttributes[@attr_name_…]` AND the skipped filter is the one with `Value: node-a`). If the current skip logic already drops that predicate, `node-a` must not appear in `args` for the host query:

Add this stronger check to the same test if the string assert is brittle:

```go
_, args := q.filters(&[]string{"host.name"}[0])
for _, a := range args {
	assert.NotContains(t, fmt.Sprint(a), "node-a")
}
```

(Need `"fmt"` in the test import if you add this.)

- [ ] **Step 4: Re-run tests — expect PASS**

```bash
go test ./clickhouse/ -count=1 -run 'TestLogQueryFiltersExcludeNamedAttr|TestFacetCountSQL'
```

Expected: `PASS`

- [ ] **Step 5: Commit (skip unless the user asks)**

```bash
git add clickhouse/logs_facets.go clickhouse/logs_facets_test.go
git commit -m "feat(logs): add ClickHouse facet count queries"
```

---

### Task 2: Overview merge helpers (TDD)

**Files:**
- Create: `api/views/overview/logs_facets.go`
- Create: `api/views/overview/logs_facets_test.go`

**Interfaces:**
- Consumes: `clickhouse.FacetValue`, `clickhouse.FacetGroup`
- Produces:
  - `func mergeFacetValues(dst map[string]uint64, values []clickhouse.FacetValue)`
  - `func completeSeverityFacets(values []clickhouse.FacetValue) []clickhouse.FacetValue`
  - `func facetGroupsFromMerged(merged map[string]map[string]uint64) []clickhouse.FacetGroup`

- [ ] **Step 1: Write failing tests**

Create `api/views/overview/logs_facets_test.go`:

```go
package overview

import (
	"testing"

	"github.com/coroot/coroot/clickhouse"
	"github.com/stretchr/testify/assert"
)

func TestMergeFacetValuesSumsCounts(t *testing.T) {
	dst := map[string]uint64{"a": 10}
	mergeFacetValues(dst, []clickhouse.FacetValue{
		{Value: "a", Count: 5},
		{Value: "b", Count: 2},
		{Value: "", Count: 9},
	})
	assert.Equal(t, map[string]uint64{"a": 15, "b": 2}, dst)
}

func TestCompleteSeverityFacetsFillsZerosAndOrder(t *testing.T) {
	got := completeSeverityFacets([]clickhouse.FacetValue{
		{Value: "error", Count: 12},
		{Value: "info", Count: 38},
		{Value: "fatal", Count: 3},
	})
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "unknown", Count: 0},
		{Value: "info", Count: 38},
		{Value: "warning", Count: 0},
		{Value: "error", Count: 12},
	}, got)
}

func TestFacetGroupsFromMergedKeepsClusterRowsAndSorts(t *testing.T) {
	merged := map[string]map[string]uint64{
		"service.name": {
			"/k8s/coroot-dev/nextjs-demo":  10,
			"/k8s/coroot-dev/express-demo": 40,
		},
		"Cluster": {
			"default": 50,
			"other":   7,
		},
		"Severity": {"info": 38, "error": 12},
	}
	groups := facetGroupsFromMerged(merged)
	byKey := map[string]clickhouse.FacetGroup{}
	for _, g := range groups {
		byKey[g.Key] = g
	}
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "unknown", Count: 0},
		{Value: "info", Count: 38},
		{Value: "warning", Count: 0},
		{Value: "error", Count: 12},
	}, byKey["Severity"].Values)
	assert.Equal(t, "/k8s/coroot-dev/express-demo", byKey["service.name"].Values[0].Value)
	assert.Equal(t, uint64(40), byKey["service.name"].Values[0].Count)
	assert.Len(t, byKey["Cluster"].Values, 2)
}
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
go test ./api/views/overview/ -count=1 -run 'TestMergeFacetValuesSumsCounts|TestCompleteSeverityFacetsFillsZerosAndOrder|TestFacetGroupsFromMergedKeepsClusterRowsAndSorts'
```

Expected: FAIL, functions undefined.

- [ ] **Step 3: Implement helpers**

Create `api/views/overview/logs_facets.go`:

```go
package overview

import (
	"sort"

	"github.com/coroot/coroot/clickhouse"
)

var severityFacetOrder = []string{"unknown", "info", "warning", "error"}

func mergeFacetValues(dst map[string]uint64, values []clickhouse.FacetValue) {
	if dst == nil {
		return
	}
	for _, v := range values {
		if v.Value == "" {
			continue
		}
		dst[v.Value] += v.Count
	}
}

func completeSeverityFacets(values []clickhouse.FacetValue) []clickhouse.FacetValue {
	by := map[string]uint64{}
	for _, v := range values {
		by[v.Value] = v.Count
	}
	out := make([]clickhouse.FacetValue, 0, len(severityFacetOrder))
	for _, label := range severityFacetOrder {
		out = append(out, clickhouse.FacetValue{Value: label, Count: by[label]})
	}
	return out
}

func facetGroupsFromMerged(merged map[string]map[string]uint64) []clickhouse.FacetGroup {
	order := []string{"Severity", "Cluster", "service.name", "host.name"}
	var groups []clickhouse.FacetGroup
	for _, key := range order {
		counts := merged[key]
		if counts == nil {
			continue
		}
		g := clickhouse.FacetGroup{Key: key}
		if key == "Severity" {
			vals := make([]clickhouse.FacetValue, 0, len(counts))
			for v, n := range counts {
				vals = append(vals, clickhouse.FacetValue{Value: v, Count: n})
			}
			g.Values = completeSeverityFacets(vals)
		} else {
			for v, n := range counts {
				g.Values = append(g.Values, clickhouse.FacetValue{Value: v, Count: n})
			}
			sort.Slice(g.Values, func(i, j int) bool {
				if g.Values[i].Count != g.Values[j].Count {
					return g.Values[i].Count > g.Values[j].Count
				}
				return g.Values[i].Value < g.Values[j].Value
			})
		}
		groups = append(groups, g)
	}
	return groups
}
```

Note: `GetLogFacetCounts` already completes Severity per client. Merge still runs `completeSeverityFacets` so a client that omitted a bucket still yields zeros after sum.

- [ ] **Step 4: Re-run tests — expect PASS**

```bash
go test ./api/views/overview/ -count=1 -run 'TestMergeFacetValuesSumsCounts|TestCompleteSeverityFacetsFillsZerosAndOrder|TestFacetGroupsFromMergedKeepsClusterRowsAndSorts'
```

Expected: `PASS`

- [ ] **Step 5: Commit (skip unless the user asks)**

---

### Task 3: Wire facets into overview logs Search

**Files:**
- Modify: `api/views/overview/logs.go`

**Interfaces:**
- Consumes: `Client.GetLogFacetCounts`, merge helpers from Task 2
- Produces: `Logs.Facets []clickhouse.FacetGroup \`json:"facets,omitempty"\`` on overview logs JSON (`data.logs.facets`)

- [ ] **Step 1: Add the JSON field**

In `type Logs struct` add after `Suggest`:

```go
Facets []clickhouse.FacetGroup `json:"facets,omitempty"`
```

- [ ] **Step 2: Fetch facets in `renderLogs`**

Skip when `q.Suggest != nil`. Use a copy of `lq` with `Since` zero so refresh (`since`) does not shrink counts.

Inside the client loop:

1. Keep `if !clusterFilter.Matches(ch.Project().Name) { continue }` **only** around histogram + `GetLogs` + non-Cluster facet names.
2. Always run `GetLogFacetCounts(ctx, facetQuery, "Cluster")` for every client (exclude-self).
3. Run `Severity`, `service.name`, `host.name` only when the cluster matches.
4. Each `GetLogFacetCounts` error: `klog.Errorln(err)` and skip that group. Do not set `v.Error`.
5. Merge into `map[string]map[string]uint64` under a mutex if using goroutines.

Recommended structure (same `WaitGroup` family as suggest):

```go
facetQuery := lq
facetQuery.Since = time.Time{}
mergedFacets := map[string]map[string]uint64{}
var facetMu sync.Mutex
var facetWg sync.WaitGroup

addFacet := func(ch *clickhouse.Client, name string) {
	facetWg.Add(1)
	go func() {
		defer facetWg.Done()
		values, e := ch.GetLogFacetCounts(ctx, facetQuery, name)
		if e != nil {
			klog.Errorln(e)
			return
		}
		facetMu.Lock()
		if mergedFacets[name] == nil {
			mergedFacets[name] = map[string]uint64{}
		}
		mergeFacetValues(mergedFacets[name], values)
		facetMu.Unlock()
	}()
}

// inside client loop, after clusterFilter check for histogram/logs:
if q.Suggest == nil {
	addFacet(ch, "Cluster")
	if clusterFilter.Matches(ch.Project().Name) {
		addFacet(ch, "Severity")
		addFacet(ch, "service.name")
		addFacet(ch, "host.name")
	}
}
```

If `clusterFilter.Matches` is false, still call `addFacet(ch, "Cluster")` **before** `continue`. Restructure the loop so Cluster facet is not skipped:

```go
for _, ch := range chs.Clients {
	match := clusterFilter.Matches(ch.Project().Name)
	if q.Suggest == nil {
		addFacet(ch, "Cluster")
		if match {
			addFacet(ch, "Severity")
			addFacet(ch, "service.name")
			addFacet(ch, "host.name")
		}
	}
	if !match {
		continue
	}
	// existing histogram + GetLogs + suggest
}
```

After `facetWg.Wait()` (and existing `suggestWg.Wait()`), set:

```go
v.Facets = facetGroupsFromMerged(mergedFacets)
```

Always assign when `q.Suggest == nil`, including empty `mergedFacets`, so JSON has `"facets": []` rather than omitting the field if you need the frontend to detect the new backend. `omitempty` drops empty slices — **use a non-nil empty slice** `v.Facets = []clickhouse.FacetGroup{}` at least, or drop `omitempty` so `facets: []` is always present on Search. Prefer **removing `omitempty`** on `Facets` so a successful Search always sends `facets` (frontend presence check). Suggest-only responses can leave it nil.

- [ ] **Step 3: Compile**

```bash
go test ./api/views/overview/ ./clickhouse/ -count=1
```

Expected: `PASS`

- [ ] **Step 4: Commit (skip unless the user asks)**

---

### Task 4: Frontend — consume `facets` (TDD)

**Files:**
- Modify: `front/src/utils/logQuickFilters.js`
- Modify: `front/tests/unit/logQuickFilters.test.js`
- Modify: `front/src/components/LogQuickFilters.vue`
- Modify: `front/src/components/Logs.vue`

**Interfaces:**
- Consumes: `view.facets` as `{ key, values: [{ value, count }] }[]`
- Produces: `buildLogQuickFilters(entries, { facets, severityFacets, ... })` uses backend for core keys when `Array.isArray(options.facets)`

- [ ] **Step 1: Write failing frontend tests**

Append to `front/tests/unit/logQuickFilters.test.js`:

```js
const backendFacets = [
    {
        key: 'Severity',
        values: [
            { value: 'unknown', count: 0 },
            { value: 'info', count: 38 },
            { value: 'warning', count: 0 },
            { value: 'error', count: 12 },
        ],
    },
    {
        key: 'service.name',
        values: [
            { value: '/k8s/coroot-dev/express-demo', count: 10 },
            { value: '/k8s/coroot-dev/nextjs-demo', count: 2 },
        ],
    },
    { key: 'host.name', values: [{ value: 'node-a', count: 10 }, { value: 'node-b', count: 2 }] },
    { key: 'Cluster', values: [{ value: 'default', count: 12 }] },
];

const pageEntries = Array.from({ length: 100 }, (_, i) => ({
    severity: 'error',
    cluster: 'default',
    attributes: {
        'service.name': i < 90 ? '/k8s/coroot-dev/express-demo' : '/k8s/coroot-dev/nextjs-demo',
        'host.name': i < 90 ? 'node-a' : 'node-b',
    },
}));

test('uses ClickHouse facet counts instead of the loaded page', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, { facets: backendFacets });
    const apps = groups.find((g) => g.key === 'service.name');
    const sev = groups.find((g) => g.key === 'Severity');
    assert.equal(apps.values.find((v) => v.value.includes('express-demo')).count, 10);
    assert.equal(sev.values.find((v) => v.value === 'info').count, 38);
    assert.equal(sev.values.find((v) => v.value === 'error').count, 12);
});

test('falls back to counting entries when facets is omitted', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, {});
    const apps = groups.find((g) => g.key === 'service.name');
    assert.equal(apps.values.find((v) => v.value.includes('express-demo')).count, 90);
});

test('does not mix backend severity with client-side application when facets is present', async () => {
    const { buildLogQuickFilters } = await loadLogQuickFilters();
    const groups = buildLogQuickFilters(pageEntries, {
        facets: [{ key: 'Severity', values: [{ value: 'info', count: 38 }, { value: 'error', count: 12 }, { value: 'unknown', count: 0 }, { value: 'warning', count: 0 }] }],
    });
    const apps = groups.find((g) => g.key === 'service.name');
    assert.equal(apps, undefined);
});
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
cd front && node --test tests/unit/logQuickFilters.test.js
```

Expected: new tests FAIL (`count` 90 not 10, or Application still built from entries when `facets` is present).

- [ ] **Step 3: Implement `buildLogQuickFilters`**

In `buildLogQuickFilters`, after building `defs` and before `.map`:

```js
const useBackendFacets = Array.isArray(options.facets);
const backend = new Map((options.facets || []).map((g) => [g.key, g]));
```

Inside `.map((d) => { ... })`, **before** the `severityFacets` / entry-counting branches:

```js
if (useBackendFacets && CORE_FACETS.some((c) => c.key === d.key)) {
    const group = backend.get(d.key);
    const values = (group?.values || []).map((facet) => ({
        value: facet.value,
        label: d.key === 'service.name' ? displayServiceName(facet.value) : facet.value,
        count: facet.count || 0,
        color: d.key === 'Severity' ? (options.severityFacets || []).find((s) => s.value === facet.value)?.color || '' : '',
    }));
    return { key: d.key, label: d.label, values };
}
```

Keep the existing `severityFacets` branch for the fallback path (no `facets` array). Keep entry counting for dynamic columns always.

Leave `.filter((g) => g.values.length > 0)` as-is (empty failed groups hide; catalog in `buildStableLogQuickFilters` still keeps active filters at 0).

- [ ] **Step 4: Wire Vue**

`LogQuickFilters.vue` props:

```js
facets: { type: Array, default: undefined },
```

`rawGroups`:

```js
return buildLogQuickFilters(this.entries, {
    hiddenAttributes: this.hiddenAttributes,
    columns: this.columns,
    severityFacets: this.severityFacets,
    facets: this.facets,
});
```

`Logs.vue` template:

```vue
<LogQuickFilters
    :entries="entries"
    :filters="query.filters"
    :hidden-attributes="hiddenAttributes"
    :columns="columns"
    :severity-facets="severityFacets"
    :facets="view.facets"
    @toggle="toggleQuickFilter"
    @clear="clearQuickFilters"
/>
```

Keep `severityFacets` computed from the chart **for colors only**. Counts for Severity come from `view.facets` once present.

- [ ] **Step 5: Re-run frontend tests — expect PASS**

```bash
cd front && node --test tests/unit/logQuickFilters.test.js
```

Expected: `PASS`

- [ ] **Step 6: Commit (skip unless the user asks)**

---

### Task 5: E2E ingest + overview API

**Files:**
- Modify: `e2e/coroot.go`
- Create: `e2e/clickhouse.go`
- Create: `e2e/log_facets_test.go`

**Interfaces:**
- Consumes: overview `GET /api/project/{id}/overview/logs`, ClickHouse HTTP `COROOT_DEV_CLICKHOUSE_HTTP` (default `http://127.0.0.1:18123`)
- Produces: `TestOverviewLogFacetCounts` in `make test-e2e`

- [ ] **Step 1: Overview fetch helper**

In `e2e/coroot.go` add types + helper (next to `fetchAppLogsQuery`):

```go
type overviewLogs struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Entries []struct {
		Message    string            `json:"message"`
		Attributes map[string]string `json:"attributes"`
		Cluster    string            `json:"cluster"`
	} `json:"entries"`
	Facets []struct {
		Key    string `json:"key"`
		Values []struct {
			Value string `json:"value"`
			Count uint64 `json:"count"`
		} `json:"values"`
	} `json:"facets"`
}

func fetchOverviewLogs(t *testing.T, projectID string, query map[string]any) overviewLogs {
	t.Helper()
	q, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	u := fmt.Sprintf("%s/api/project/%s/overview/logs?from=now-15m&query=%s",
		corootBase(),
		url.PathEscape(projectID),
		url.QueryEscape(string(q)),
	)
	var env apiEnvelope
	httpGetJSON(t, u, &env)
	var ov struct {
		Logs overviewLogs `json:"logs"`
	}
	if err := json.Unmarshal(env.Data, &ov); err != nil {
		t.Fatalf("decode overview logs: %v\n%s", err, env.Data)
	}
	return ov.Logs
}

func facetCount(logs overviewLogs, key, value string) uint64 {
	for _, g := range logs.Facets {
		if g.Key != key {
			continue
		}
		for _, v := range g.Values {
			if v.Value == value {
				return v.Count
			}
		}
	}
	return 0
}
```

- [ ] **Step 2: ClickHouse HTTP helper**

Create `e2e/clickhouse.go` (`//go:build e2e`). Env: `COROOT_DEV_CLICKHOUSE_HTTP` default `http://127.0.0.1:18123`, user `COROOT_DEV_CLICKHOUSE_USER` default `default`, password `COROOT_DEV_CLICKHOUSE_PASSWORD`.

Pick DB: `SELECT database FROM system.tables WHERE name = 'otel_logs'` — prefer `default`, else first `coroot_*`, skip `system` / `INFORMATION_SCHEMA`.

```go
func insertFacetFixture(t *testing.T, token string) {
	t.Helper()
	now := time.Now().UTC()
	var buf bytes.Buffer
	type spec struct {
		n, sevNum int
		sev, svc, host, body string
	}
	rows := []spec{
		{30, 9, "INFO", "/k8s/coroot-dev/express-demo", "node-a", "e2e express info"},
		{10, 17, "ERROR", "/k8s/coroot-dev/express-demo", "node-a", "e2e express error"},
		{8, 9, "INFO", "/k8s/coroot-dev/nextjs-demo", "node-b", "e2e nextjs info"},
		{2, 17, "ERROR", "/k8s/coroot-dev/nextjs-demo", "node-b", "e2e nextjs error"},
	}
	for _, s := range rows {
		for i := 0; i < s.n; i++ {
			line, err := json.Marshal(map[string]any{
				"Timestamp":      now.Format("2006-01-02 15:04:05.000000000"),
				"TraceId":        "",
				"SpanId":         "",
				"TraceFlags":     0,
				"SeverityText":   s.sev,
				"SeverityNumber": s.sevNum,
				"ServiceName":    s.svc,
				"Body":           s.body,
				"ResourceAttributes": map[string]string{
					"service.name": s.svc,
					"host.name":    s.host,
				},
				"LogAttributes": map[string]string{"e2e.facets": token},
			})
			if err != nil {
				t.Fatal(err)
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}
	chExec(t, `INSERT INTO otel_logs (
		Timestamp, TraceId, SpanId, TraceFlags,
		SeverityText, SeverityNumber, ServiceName, Body,
		ResourceAttributes, LogAttributes
	) FORMAT JSONEachRow`, buf.Bytes())
}

func deleteFacetFixture(t *testing.T, token string) {
	t.Helper()
	q := fmt.Sprintf("DELETE FROM otel_logs WHERE LogAttributes['e2e.facets'] = '%s'", token)
	chExec(t, q, nil)
}
```

`chExec` POSTs to the CH HTTP URL with `X-ClickHouse-User` / `X-ClickHouse-Key`, `database` query param, and fails the test on non-2xx. Escape `token` is safe: it is `e2e-facets-<digits>` only.

- [ ] **Step 3: Write the e2e test**

Create `e2e/log_facets_test.go`:

```go
//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestOverviewLogFacetCounts(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("e2e-facets-%d", time.Now().UnixNano())
	insertFacetFixture(t, token)
	t.Cleanup(func() { deleteFacetFixture(t, token) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":  "messages",
		"agent": true,
		"otel":  true,
		"limit": 100,
		"filters": []map[string]string{{
			"name": "e2e.facets", "op": "=", "value": token,
		}},
	}

	logs := fetchOverviewLogs(t, projectID, base)
	if logs.Error != "" {
		t.Fatalf("overview logs error: %s", logs.Error)
	}
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/express-demo", 40)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/nextjs-demo", 10)
	assertFacet(t, logs, "host.name", "node-a", 40)
	assertFacet(t, logs, "host.name", "node-b", 10)
	assertFacet(t, logs, "Severity", "info", 38)
	assertFacet(t, logs, "Severity", "error", 12)
	assertFacet(t, logs, "Severity", "warning", 0)
	assertFacet(t, logs, "Severity", "unknown", 0)
	if clusterTotal(logs) != 50 {
		t.Fatalf("Cluster total=%d want 50 facets=%v", clusterTotal(logs), logs.Facets)
	}

	withSev := copyQuery(base)
	withSev["filters"] = append(filtersOf(base), map[string]string{"name": "Severity", "op": "=", "value": "error"})
	logs = fetchOverviewLogs(t, projectID, withSev)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/express-demo", 10)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/nextjs-demo", 2)
	assertFacet(t, logs, "host.name", "node-a", 10)
	assertFacet(t, logs, "host.name", "node-b", 2)
	assertFacet(t, logs, "Severity", "info", 38)
	assertFacet(t, logs, "Severity", "error", 12)

	withApp := copyQuery(base)
	withApp["filters"] = append(filtersOf(base), map[string]string{
		"name": "service.name", "op": "=", "value": "/k8s/coroot-dev/express-demo",
	})
	logs = fetchOverviewLogs(t, projectID, withApp)
	assertFacet(t, logs, "Severity", "info", 30)
	assertFacet(t, logs, "Severity", "error", 10)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/express-demo", 40)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/nextjs-demo", 10)
}

func assertFacet(t *testing.T, logs overviewLogs, key, value string, want uint64) {
	t.Helper()
	got := facetCount(logs, key, value)
	if got != want {
		t.Fatalf("facet %s=%s count=%d want %d facets=%v", key, value, got, want, logs.Facets)
	}
}

func clusterTotal(logs overviewLogs) uint64 {
	var n uint64
	for _, g := range logs.Facets {
		if g.Key != "Cluster" {
			continue
		}
		for _, v := range g.Values {
			n += v.Count
		}
	}
	return n
}

func copyQuery(base map[string]any) map[string]any {
	b, _ := json.Marshal(base)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func filtersOf(q map[string]any) []map[string]string {
	raw, _ := json.Marshal(q["filters"])
	var filters []map[string]string
	_ = json.Unmarshal(raw, &filters)
	return filters
}
```

If Coroot has not yet picked up inserts (unlikely; logs are live CH), `waitUntil` 15s around the first fetch.

- [ ] **Step 4: Run e2e**

Requires `make dev` already up:

```bash
make test-e2e
```

Expected: `TestOverviewLogFacetCounts` PASS. Other e2e tests still PASS.

If CH HTTP is not forwarded: fail with a clear `Tilt forwarding ClickHouse on 18123?` message (same as chseed).

- [ ] **Step 5: Commit (skip unless the user asks)**

---

### Task 6: Verification

- [ ] **Step 1: Unit tests**

```bash
go test $$(go list ./... | grep -v '/e2e$$')
cd front && npm run test:unit
```

Expected: PASS

- [ ] **Step 2: Manual check (if `make dev` is running)**

Open overview Logs. With `limit` 100 and more than 100 info logs, Application/Cluster must not sum to 100. Set `Severity = error`: Application counts drop; Severity `info` does not go to 0. Histogram shows only error.

- [ ] **Step 3: Commit (skip unless the user asks)**

---

## Self-review vs spec

| Spec requirement | Task |
|---|---|
| Exact CH counts, full time range | 1, 3 |
| Cross-filter other groups | 1 (`filters(&name)`), 5 case 2 |
| Exclude-self on same group | 1, 3 Cluster loop, 5 case 2–3 |
| Histogram/table remain filtered | 3 (histogram still uses full `lq` filters) |
| Four core keys only | 1 `facetCountSQL` default false |
| No app-logs API/UI | not touched |
| Non-fatal facet errors | 3 |
| Severity always 4 values | 1 + 2 `completeSeverityFacets` |
| Frontend uses `facets`, fallback if omitted | 4 |
| No mix when `facets` present | 4 third unit test |
| E2E ingest 50 rows + 3 queries | 5 |
| `omitempty` vs frontend presence | Task 3: drop `omitempty` on Search responses |
