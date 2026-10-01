# ClickHouse Trace Body Columns Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** store HTTP request and response bodies sent as span attributes in dedicated `otel_traces` columns instead of inside the `SpanAttributes` map, read them only where a single trace is shown, and make them searchable by token.

**Scope:** the coroot server: DDL migration, collector write path (extraction and a size cap), span read path, span model.

**Non-goals:**
- Front-end rendering of the bodies. After this plan they are stored, returned by the trace-detail API and searchable in SQL; nothing shows them in the UI yet.
- Body redaction or any other protection of sensitive data in bodies. Out of scope for now, by decision.
- Decompression or decoding (gzip, protobuf); bodies are stored as the bytes received.
- Backfilling bodies that an earlier build may have written into `SpanAttributes`.
- The producer. Nothing sends these attributes today (see "Contract" below).

---

## Contract with producers

A span carries the bodies as two string attributes:

| attribute | content |
| --- | --- |
| `http.request.body` | the request body bytes as captured, possibly truncated |
| `http.response.body` | the response body bytes as captured, possibly truncated |

These names are not OpenTelemetry semantic conventions (those only define `http.request.body.size`); they are this project's contract. **No producer sends them yet**: `coroot-node-agent` emits only `http.url`, `http.method`, `http.status_code` and peer attributes (`tracing/tracing.go`), and its earlier body-capture plan was deleted. The node agent captures at most 4 KB of an HTTP/1 body per message and 4 KB per HTTP/2 stream direction in the kernel, so its bodies will be truncated.

This plan is independent of the producer and safe to ship first. It must ship **before** any producer starts sending bodies in production, because without it the bodies land in `SpanAttributes` (see below).

---

## Why dedicated columns (measured)

The collector writes span attributes into `SpanAttributes Map(LowCardinality(String), String)` verbatim (`collector/traces.go`, `attributesToMap`). Adding bodies needs no schema change, which is exactly the problem:

**A `Map` column is read whole for any key.** ClickHouse stores a map as one column of key/value pairs, so `SpanAttributes['http.status_code']` reads every body in the map too. Span filters, facets (`clickhouse/traces_facets.go`) and the histogram materialized view (`otel_traces_histogram_mv` reads `SpanAttributes['net.peer.name']` on every insert) all do this.

Measured on ClickHouse 24.3 (the version in `deploy/`), 2 M spans, ~920-byte JSON bodies, after `OPTIMIZE FINAL`:

| query | body in `SpanAttributes` | body in its own column |
| --- | --- | --- |
| `count() WHERE SpanAttributes['http.status_code'] = '500'` | 292 ms, 1.90 GiB read | 36 ms, 174 MiB read |
| `SELECT TraceId, SpanAttributes ... LIMIT 100` | 10 ms, 8.2 MiB read | 1 ms, 0.9 MiB read |

An order of magnitude on every attribute filter, growing with body size. (An earlier draft also argued that bodies spoil `idx_span_attr_value`. That is weak: `bloom_filter(0.01)` is sized per granule for its false-positive rate and stores hashes, so large values make it bigger, not less selective. The read cost above is the real reason.)

Coroot already stores this shape of data the same way: `otel_logs.Body String CODEC(ZSTD(1))` with a token bloom filter.

### Rejected alternatives

| Approach | Why not |
| --- | --- |
| Leave the bodies in `SpanAttributes` | Every attribute filter reads them; table above |
| `MATERIALIZED SpanAttributes['http.request.body']` columns (the `NetSockPeerAddr` precedent) | Keeps the body in the map, so the map cost stays and the body is stored twice |
| `ngrambf_v1` index | Measured: `ngrambf_v1(4, 1048576, 3, 0)` was 38% the size of the data and skipped nothing (245/245 granules); ~900 4-grams per body saturate it |
| `tokenbf_v1(32768, 3, 0)`, copied from `otel_logs` | Measured: skips only 32% of granules for a unique token (167/245). Log lines are short; bodies are not |
| No index | A token search scans every body in the time range: 240 ms and 1.73 GiB for 2 M spans, versus 21 ms and 7 MiB with the index below |
| `LowCardinality(String)` | Bodies are near-unique |

### Index choice (measured)

`tokenbf_v1(size_bytes, 3, 0)`, `GRANULARITY 1`, 8192-row granules, same data as above. Searching one unique token (an order id) and one token in ~20 bodies:

| bloom filter size | index size vs. data | unique token: granules read | ~20 matches: granules read |
| --- | --- | --- | --- |
| 32 KB | 0.9% | 167/245 | 210/245 |
| 128 KB | 4.8% | 13/245 | 34/245 |
| **256 KB** | **9.6%** | **1/245** | **18/245** |
| 512 KB | 18.9% | 1/245 | 17/245 |
| 1 MB | 28.9% | 1/245 | 17/245 |

256 KB is the knee: as selective as larger filters at half the space. Bodies with many more distinct tokens than ~920-byte JSON need more; Task 3 Step 7 re-checks on real data.

**How to search.** The index serves `hasToken(RequestBody, 'token')` and `LIKE` patterns whose tokens are bounded by non-alphanumeric characters (`LIKE '%"order-1234567"%'`). It does **not** serve `LIKE '%order-1234567%'`: the tokens touching `%` may be parts of longer tokens, so the index is skipped and every granule is read (measured: 245/245). Any API or UI search built on these columns must use `hasToken` (or bounded `LIKE`).

---

## Global Constraints

- Do not git commit; the user drives that.
- No new README or summary documents.
- Tests first for Go.
- Migrations are additive and idempotent (`ADD COLUMN IF NOT EXISTS`, `ADD INDEX IF NOT EXISTS`); the list is replayed on every startup.
- Attribute key names are the contract above: `http.request.body`, `http.response.body`.

---

## File map

| File | Change |
| --- | --- |
| `ch/client.go` | Columns and index in `CREATE TABLE otel_traces`; `ALTER`s for existing installs; `ALTER`s for `otel_traces_distributed` |
| `ch/client_test.go` | DDL assertions |
| `collector/traces.go` | Extract the two keys out of `spanAttributes`, cap their size, write the new columns |
| `collector/traces_test.go` (new) | Extraction and cap |
| `clickhouse/traces.go` | One shared span column list; bodies selected only on request; `GetSpansByTraceId` takes a `withBodies` flag |
| `clickhouse/traces_test.go` (new) or an existing test file in the package | Column-list assertions |
| `model/trace.go` | `TraceSpan.RequestBody`, `TraceSpan.ResponseBody` |
| `api/views/tracing/tracing.go`, `api/views/overview/traces.go` | Ask for bodies in the trace-detail calls |

---

### Task 1: Schema

**Files:** modify `ch/client.go`, `ch/client_test.go`

- [ ] **Step 1: Write the failing DDL test.** In `ch/client_test.go`, next to `TestMaterializedColumnsOnCreateTable`, using `tableSQLContaining`:

```go
func TestTraceBodyColumns(t *testing.T) {
	traces := tableSQLContaining("CREATE TABLE IF NOT EXISTS otel_traces @on_cluster")
	require.NotEmpty(t, traces)
	// fresh install: the CREATE has them
	assert.Contains(t, traces, "RequestBody String CODEC(ZSTD(1))")
	assert.Contains(t, traces, "ResponseBody String CODEC(ZSTD(1))")
	assert.Contains(t, traces, "INDEX idx_request_body RequestBody TYPE tokenbf_v1(262144, 3, 0) GRANULARITY 1")
	assert.Contains(t, traces, "INDEX idx_response_body ResponseBody TYPE tokenbf_v1(262144, 3, 0) GRANULARITY 1")

	joined := strings.Join(tables, "\n")
	// upgrade: an existing table gets them by ALTER
	assert.Contains(t, joined, "ALTER TABLE otel_traces @on_cluster ADD COLUMN IF NOT EXISTS RequestBody String CODEC(ZSTD(1))")
	assert.Contains(t, joined, "ALTER TABLE otel_traces @on_cluster ADD COLUMN IF NOT EXISTS ResponseBody String CODEC(ZSTD(1))")
	assert.Contains(t, joined, "ALTER TABLE otel_traces @on_cluster ADD INDEX IF NOT EXISTS idx_request_body")
	assert.Contains(t, joined, "ALTER TABLE otel_traces @on_cluster ADD INDEX IF NOT EXISTS idx_response_body")
	// never materialized out of the map: that keeps the body in SpanAttributes
	assert.NotContains(t, joined, "MATERIALIZED SpanAttributes['http.request.body']")

	// a Distributed table created AS otel_traces copies the structure once, so an
	// upgraded cluster needs the columns added there too
	dist := strings.Join(distributedTables, "\n")
	assert.Contains(t, dist, "ALTER TABLE otel_traces_distributed ON CLUSTER @cluster ADD COLUMN IF NOT EXISTS RequestBody String")
	assert.Contains(t, dist, "ALTER TABLE otel_traces_distributed ON CLUSTER @cluster ADD COLUMN IF NOT EXISTS ResponseBody String")
	assert.Greater(t, strings.Index(dist, "ADD COLUMN IF NOT EXISTS RequestBody"), strings.Index(dist, "CREATE TABLE IF NOT EXISTS otel_traces_distributed"))
}
```

- [ ] **Step 2: Run it (expect fail).** `go test ./ch -run TestTraceBodyColumns -count=1`

- [ ] **Step 3: Implement.**
  - In `CREATE TABLE IF NOT EXISTS otel_traces`, after `StatusMessage`: the two columns, and after `idx_duration`: the two indexes, exactly as asserted. This follows `3ad479e`, which moved new columns into the `CREATE`.
  - After the existing `NetSockPeerAddr` alter: the four `ALTER TABLE otel_traces @on_cluster ...` statements (two `ADD COLUMN IF NOT EXISTS`, two `ADD INDEX IF NOT EXISTS ... TYPE tokenbf_v1(262144, 3, 0) GRANULARITY 1`).
  - In `distributedTables`, right after the `otel_traces_distributed` create: two `ALTER TABLE otel_traces_distributed ON CLUSTER @cluster ADD COLUMN IF NOT EXISTS ... String`. No index on the Distributed table (it holds no data).
  - `ADD INDEX` on an existing table applies to new parts only; old parts stay unindexed until they expire by TTL. Do not add `MATERIALIZE INDEX` (it rewrites every part at startup).

- [ ] **Step 4: Run the package.** `go test ./ch -count=1 -v`

- [ ] **Step 5: Verify against a real ClickHouse**, both paths:
  - Fresh: `scripts/dev/dev.sh` on an empty volume, then `DESCRIBE TABLE otel_traces` lists both columns with `ZSTD(1)` and `SHOW CREATE TABLE otel_traces` lists both indexes.
  - Upgrade: start the current `main` build first, then this build on the same volume. Same checks. Restart once more and confirm a clean startup (the replay is where a non-idempotent statement fails).

---

### Task 2: Collector write path

**Files:** modify `collector/traces.go`, create `collector/traces_test.go`

- [ ] **Step 1: Write the failing tests.**

```go
func TestExtractBodiesMovesThemOutOfAttributes(t *testing.T) {
	attrs := map[string]string{
		"http.method":        "POST",
		"http.request.body":  `{"id":1}`,
		"http.response.body": `{"ok":true}`,
	}
	req, resp := extractBodies(attrs)
	require.Equal(t, `{"id":1}`, req)
	require.Equal(t, `{"ok":true}`, resp)
	require.NotContains(t, attrs, "http.request.body", "a body left in SpanAttributes is read by every attribute filter")
	require.NotContains(t, attrs, "http.response.body")
	require.Equal(t, "POST", attrs["http.method"], "other attributes are untouched")
}

func TestExtractBodiesAbsent(t *testing.T) {
	attrs := map[string]string{"http.method": "GET"}
	req, resp := extractBodies(attrs)
	require.Empty(t, req)
	require.Empty(t, resp)
	require.Len(t, attrs, 1)
}

func TestExtractBodiesCapsSize(t *testing.T) {
	attrs := map[string]string{"http.request.body": strings.Repeat("x", maxSpanBodyBytes+100)}
	req, _ := extractBodies(attrs)
	require.Len(t, req, maxSpanBodyBytes)
}

func TestTracesBatchWritesBodyColumns(t *testing.T) {
	var got ch.Query
	b := NewTracesBatch(1, time.Hour, func(q ch.Query) error { got = q; return nil })
	defer b.Close()
	b.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
			TraceId: make([]byte, 16), SpanId: make([]byte, 8),
			Attributes: []*commonv1.KeyValue{
				{Key: "http.request.body", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "req"}}},
			},
		}}}},
	}}})
	var names []string
	for _, c := range got.Input {
		names = append(names, c.Name)
	}
	require.Contains(t, names, "RequestBody")
	require.Contains(t, names, "ResponseBody")
}
```

`collector/traces.go` imports the request type as `v1` (`go.opentelemetry.io/proto/otlp/collector/trace/v1`); import the span and common packages under new aliases (`tracev1`, `commonv1`). With `limit` 1, `Add` calls `save()` synchronously, so the query is captured before `Add` returns; the timer goroutine `NewTracesBatch` starts does not interfere.

- [ ] **Step 2: Run them (expect fail).** `go test ./collector -run 'TestExtractBodies|TestTracesBatchWritesBodyColumns' -count=1`

- [ ] **Step 3: Implement** in `collector/traces.go`:

```go
const (
	attrHTTPRequestBody  = "http.request.body"
	attrHTTPResponseBody = "http.response.body"

	// Any OTLP client can send these, not only the node agent (which captures
	// at most 4 KB per message), so the collector caps them itself.
	maxSpanBodyBytes = 16 * 1024
)

// Bodies go to their own columns: a Map column is read whole for any key, so a
// body left in SpanAttributes is read by every attribute filter.
func extractBodies(attrs map[string]string) (string, string) {
	req, resp := attrs[attrHTTPRequestBody], attrs[attrHTTPResponseBody]
	delete(attrs, attrHTTPRequestBody)
	delete(attrs, attrHTTPResponseBody)
	return capBody(req), capBody(resp)
}

func capBody(s string) string {
	if len(s) > maxSpanBodyBytes {
		return s[:maxSpanBodyBytes]
	}
	return s
}
```

  - Add `RequestBody` and `ResponseBody` (`*chproto.ColStr`) to `TracesBatch`, initialise them in `NewTracesBatch`.
  - In `Add`, call `reqBody, respBody := extractBodies(spanAttributes)` right after `attributesToMap(s.GetAttributes())`, before the scope-name keys are added and before `b.SpanAttributes.Append(spanAttributes)`; append the two values next to the other per-span columns.
  - Add both to the `chproto.Input` list in `save()`.
  - The cut is by bytes and can split a UTF-8 sequence; bodies are not guaranteed to be text anyway, and readers must tolerate that (Task 3 returns them as Go strings; `encoding/json` replaces invalid UTF-8).

  Compatibility: the insert names its columns, so an older collector keeps working against the migrated table (the new columns default to `''`). The new collector against an un-migrated table fails the insert; migrations run at startup in the same binary, so this only bites a cluster where `ON CLUSTER` DDL lags during a rolling upgrade.

- [ ] **Step 4: Run the package.** `go test ./collector -count=1 -v`

---

### Task 3: Read path and model

**Files:** modify `clickhouse/traces.go`, `model/trace.go`, `api/views/tracing/tracing.go`, `api/views/overview/traces.go`; add a test in `clickhouse`

Every span query goes through `querySpans` (via `getSpans`): the span list, `GetInboundSpans`, `GetParentSpans`, `GetSpansByTraceId` and its internal use in `getTrace`; traces for the error view go through `getTraceSpans`. Adding the bodies to those SELECTs unconditionally would read them in every list, which is the cost this plan exists to avoid. So the column list is shared and the bodies are opt-in.

- [ ] **Step 1: Write the failing test.**

```go
func TestSpanColumnsBodiesOnlyOnRequest(t *testing.T) {
	assert.NotContains(t, spanColumns(false), "RequestBody")
	assert.NotContains(t, spanColumns(false), "ResponseBody")
	assert.Contains(t, spanColumns(true), "RequestBody")
	assert.Contains(t, spanColumns(true), "ResponseBody")
	// the list replaces the two hand-maintained copies in querySpans and getTraceSpans
	assert.True(t, strings.HasPrefix(spanColumns(false), "Timestamp, TraceId, SpanId, ParentSpanId"))
}
```

- [ ] **Step 2: Run it (expect fail).** `go test ./clickhouse -run TestSpanColumnsBodiesOnlyOnRequest -count=1`

- [ ] **Step 3: Implement.**
  - `model.TraceSpan`: add `RequestBody string` and `ResponseBody string`.
  - `clickhouse/traces.go`: `func spanColumns(withBodies bool) string` returning the existing column list (from `Timestamp` to `Events.Attributes`) plus `, RequestBody, ResponseBody` when asked. Use it in `querySpans` and `getTraceSpans` instead of the two literals (`traces.go:414` and `:541`).
  - Add `WithBodies bool` to `SpanQuery`. In `querySpans`, select `spanColumns(q.WithBodies)` and scan the two extra fields only when `q.WithBodies`. `getTraceSpans` stays without bodies.
  - `GetSpansByTraceId(ctx, traceId string, withBodies bool)`: sets `WithBodies` on the query it builds. Update its callers: `api/views/tracing/tracing.go:131` and `api/views/overview/traces.go:227` pass `true` (these build the single-trace view); the internal call in `getTrace` (`clickhouse/traces.go:215`) passes `false`.

- [ ] **Step 4: Run the packages.** `go test ./clickhouse ./model ./api/... -count=1`

- [ ] **Step 5: End-to-end, with a producer.** No producer exists yet, so send a span by hand with an OTLP client (for example `otel-cli` or a 20-line Go program using `otlptracehttp`) carrying both attributes, one of them over 16 KB. Then:

```sql
SELECT SpanName, length(RequestBody) AS req, length(ResponseBody) AS resp,
       mapContains(SpanAttributes, 'http.request.body') AS leaked
FROM otel_traces WHERE RequestBody != '' ORDER BY Timestamp DESC LIMIT 20
```

Expected: `req` capped at 16384, `leaked = 0` on every row. Open the trace by id in both views that call `GetSpansByTraceId` (the application tracing view and the overview traces view, `trace_id` parameter) and confirm their JSON carries the bodies; a span list response must not.

- [ ] **Step 6: Confirm the list stays light.** With a few thousand spans with bodies, run the span list view and check `system.query_log` for its query: `read_bytes` must not include the body columns (the query text must not contain `RequestBody`).

- [ ] **Step 7: Confirm the index on real bodies.**

```sql
EXPLAIN indexes = 1
SELECT count() FROM otel_traces WHERE hasToken(RequestBody, '<a token from one body>')
```

Expected: `idx_request_body` drops almost all granules. Compare with `SELECT round(sum(secondary_indices_compressed_bytes) / sum(data_compressed_bytes) * 100, 1) FROM system.parts WHERE table = 'otel_traces' AND active`. If the drop is poor, the bodies have more distinct tokens per granule than the measured ~920-byte JSON; raise the filter size (512 KB measured at 19% of data) and record the numbers here.

---

## Self-review

1. **The reason is measured, not assumed.** Moving bodies out of the map cut an attribute filter from 1.90 GiB to 174 MiB read. The weaker argument from the first draft (spoiling the map-values bloom filter) is called out as weak instead of being kept.
2. **Lists do not pay for bodies.** The first draft added the columns to the shared SELECTs, which every list goes through; that would have recreated the read cost. Bodies are now opt-in and only the single-trace view asks for them, with a test and a query-log check.
3. **The index is chosen from measurements.** The `otel_logs` parameters skip a third of granules on bodies and `ngrambf` skips none; 256 KB is the knee. The plan also records that `LIKE '%x%'` cannot use the index, so a search built later uses `hasToken`.
4. **The collector defends itself.** A 16 KB cap applies to any OTLP producer, not only the node agent.
5. **Both install paths are covered.** Fresh installs get the columns from `CREATE TABLE`, upgrades from `ALTER`, clusters also on `otel_traces_distributed`, and the replay is tested by a restart.
6. **What is not done is named.** No producer exists, nothing renders bodies, and nothing protects sensitive data in them; the last is a deliberate decision for now and must be revisited before bodies are captured from services that handle credentials or personal data.
