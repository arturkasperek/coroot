# Traces page: request and latency charts instead of the heatmap

## Goal

Replace the latency and errors heatmap on the Traces page (`/p/:project/traces`) with two bar charts that read at a glance:

1. **Requests**: how many requests there were in each time bucket, split by status.
2. **Latency**: the p50 and p95 latency in each time bucket.

The heatmap's top "errors" row looked like a latency band above 10 s, and selecting a cell in it filtered the list to errors without saying so. Both charts below keep what the heatmap was used for (narrowing the traces and the analysis tabs to a part of the traffic) with selections that are visible and removable.

## What the user sees

### Requests chart

- Stacked columns over time, one series per status group, with a legend, the same way the Logs page colors its histogram by severity.
- Status groups and colors:

  | group | meaning | color |
  |---|---|---|
  | `2xx` | HTTP status 200–299 | green |
  | `3xx` | HTTP status 300–399 | blue |
  | `4xx` | HTTP status 400–499 | orange |
  | `5xx` | HTTP status 500–599 | red |
  | `error` | no HTTP status, span status is error (a failed database query, an RPC error) | dark red |
  | `other` | everything else: no HTTP status and not an error, or an unknown status | grey |

- Values are request counts per bucket, as on the Logs page (the heatmap showed rates per second).

### Latency chart

- Columns over time on the same time axis as the requests chart. In each bucket two columns are drawn on top of each other, not stacked: p95 (always the taller one, lighter color) behind, and p50 (darker color) in front.
- Values in milliseconds or seconds.

### Selections

- **Drag on either chart** selects a time range. It sets the existing `ts_from` and `ts_to` query parameters, which all tabs already honor.
- **Click a status group** in the requests chart, in the legend or on its part of a column, to narrow everything to that group. This sets a new `status` query parameter (one group, for example `5xx`). Clicking it again removes it.
- **Click p95** in the latency chart, in the legend or on a column, to narrow everything to requests slower than the p95 of the whole current view. This sets the existing `dur_from` parameter to that p95 value and leaves `dur_to` open. Clicking p50 does nothing.
- Every active selection appears as a removable chip in the Query bar: `Time 11:30–11:40`, `Status = 5xx`, `Duration ≥ 480 ms`. Removing a chip removes the selection.
- The tabs work as they do now, with the selection applied:
  - **Traces**: the list is filtered by the selection.
  - **Error causes**: still only errors, also filtered by the time range and status.
  - **Latency explorer** and **Compare attributes**: the selection (time range, status, duration) is compared with the rest of the traffic.
  - **Overview**: as today.
- A "Hide charts" toggle replaces "Hide heatmap".

## Data

### Status group of a span

Computed in ClickHouse from the span:

1. The HTTP status code: `SpanAttributes['http.status_code']`, and when it is empty the newer semantic convention `SpanAttributes['http.response.status_code']`.
2. A code from 200 to 599 gives `2xx` … `5xx` by its first digit.
3. Otherwise `error` when `StatusCode = 'STATUS_CODE_ERROR'`, else `other`.

One SQL expression implements this, used both by the materialized view and by queries on raw spans, so both give the same groups.

### `otel_traces_histogram` gets the status group

- New column `StatusClass LowCardinality(String)`.
- The table is a `SummingMergeTree`, and a string column outside the sorting key would be collapsed into an arbitrary value when rows are summed. So the column is added to the end of the sorting key, in the same `ALTER` that adds it, as ClickHouse requires: `ADD COLUMN …, MODIFY ORDER BY (…, Bucket, StatusClass)`.
- `otel_traces_histogram_mv` computes `StatusClass` with the expression above. A `CREATE MATERIALIZED VIEW IF NOT EXISTS` does not change an existing view, so the migration drops and recreates the view when the table does not yet have the column (detected through `system.columns`). Rows the old view wrote have an empty `StatusClass`: their `Failed` count is counted as `error` and `Total - Failed` as `other`, both in the chart and in the `status` filter.
- The migration runs on every cluster node (`@on_cluster`), like the existing schema statements, and is idempotent.

### Requests chart query

- Counts per time bucket and status group, root spans only (as the heatmap).
- From `otel_traces_histogram` when the filters allow it (the same rule as today's `useTracesHistogram`), otherwise from raw `otel_traces`.
- The step is the view's step rounded up to whole minutes, as today.

### Latency chart query

- Counts per time bucket and duration bucket (the existing buckets: 0, 5, 10, 25, 50, 100, 250, 500 ms, 1, 2.5, 5, 10 s, +Inf), from the same table as the requests chart.
- p50 and p95 per time bucket, and p50 and p95 of the whole view, are computed in Go from the cumulative bucket counts by linear interpolation inside the bucket that holds the quantile, as Prometheus' `histogram_quantile` does. Results are approximate to the bucket boundaries. A quantile in the +Inf bucket is reported as the last finite boundary (10 s).
- When the raw spans are read (filters the histogram table cannot serve), the quantiles are exact (`quantile` over `Duration`) instead.

### API

`GET /api/project/:project/overview/traces` (`api/views/overview/traces.go`):

- The response drops `heatmap` and adds:
  - `requests_chart`: a `model.Chart`, stacked columns, one series per status group present in the data, ordered `2xx`, `3xx`, `4xx`, `5xx`, `error`, `other`, with the colors above.
  - `latency_chart`: a `model.Chart` in a new overlaid-columns mode, series `p95` then `p50`.
  - `latency`: `{p50, p95}` in seconds for the whole view, for the p95 click and the duration chip.
- The query gets an optional `status` (one of the six groups), applied to every query of the page: the trace list, errors, latency explorer and compare attributes selections, and facet counts. On the histogram table it filters `StatusClass` (with the empty-class mapping above); on raw spans it uses the status expression.
- `dur_from` and `dur_to` keep their meaning, so the MCP tool `traces_outliers` and existing links keep working. The heatmap-only values `inf` and `err` are still accepted for old links.

## Code

- `ch/client.go`: the column, the sorting key change and the view recreation.
- `clickhouse/traces.go`: the status group expression; the two chart queries replacing `GetRootSpansHistogram` for this page; the `status` filter in `SpanQuery`; the quantile interpolation as a pure function.
- `clickhouse/traces_facets.go`: the `status` filter in facet counts.
- `model/chart.go`: the overlaid-columns option.
- `api/views/overview/traces.go`: the new response fields and the `status` parameter.
- `front/src/components/Chart.vue`: overlaid columns; a click on a column part or a legend item emits the series.
- `front/src/views/Traces.vue`: the two charts instead of `Heatmap`, the toggle, the drag and click handlers, the chips.
- `front/src/utils/traceQuery.js`: the `status` parameter and the selection chips.

## Out of scope

- The per-application Tracing report (`api/views/tracing`, its own page) keeps its heatmap. The Traces page does not use it.
- More than one status group selected at a time.
- Exact percentiles on the histogram table.

## Testing

- **Go unit tests**:
  - quantile interpolation (one bucket, all in +Inf, an empty series, p50 and p95 in different buckets);
  - the status group expression on a table of status codes and span statuses (both HTTP attributes, a non-numeric code, codes outside 200–599, error and ok without a code);
  - the SQL of the `status` filter for the histogram table and raw spans, including the empty-class mapping.
- **Storage e2e tests** (`chtest`, real ClickHouse):
  - the migration on a table created with the old schema, run twice;
  - requests per status group and p50/p95 from known spans, through the histogram table and through raw spans, giving the same groups;
  - the `status` filter on the trace list.
- **Front unit tests**: the `status` parameter in the query, and the chips for each selection, including removal.
- **Dev cluster**: the page in Tilt with the demo traffic (express, flask, nextjs, symfony demos, which produce 2xx, 5xx and database spans), checking both charts, each selection and its chip, and the four tabs with a selection.
