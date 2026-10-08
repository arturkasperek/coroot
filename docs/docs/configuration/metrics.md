---
sidebar_position: 4
---

# Metrics

Coroot stores metrics in ClickHouse, in a [TimeSeries](https://clickhouse.com/docs/engines/table-engines/special/time_series)
table, and queries them with ClickHouse's native PromQL support. There is no separate Prometheus to run or configure.

Agents (`coroot-node-agent` and `coroot-cluster-agent`) push metrics to Coroot using the Prometheus Remote Write protocol.
Coroot writes them to the ClickHouse database of the project the API key belongs to, so multi-tenancy works the same way as for
traces, logs and profiles.

## Requirements

ClickHouse 26.9 or newer, running as a cluster (a Keeper is required). Metrics live in a separate single-shard cluster named `coroot_metrics`.

## Evaluated metrics

Coroot evaluates the queries it needs for its pages and inspections every 15 seconds and keeps the results in the `world_*`
tables (raw points plus 5-minute and 1-hour rollups), so pages read pre-aggregated data. When several Coroot instances run
against the same ClickHouse, only one of them writes.
