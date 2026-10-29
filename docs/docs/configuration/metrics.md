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

## Data delay

Pages show data up to about 60–75 seconds ago, not up to now. The delay is by design:

- A 15-second step is evaluated only two steps after it ends (30 seconds), when the agents' remote write and the collector's
  batches have landed. A step evaluated earlier would be stored incomplete, and stored steps are never recomputed.
- The application-level aggregates (connections between applications, traffic, SLIs, log patterns) are computed two steps
  later again (another 30 seconds), when the Kubernetes metadata of a pod that has just started has arrived. Computed
  earlier, a connection to a new pod would be stored as one to an external service.
- The evaluation runs every 15 seconds, which adds up to one more step.

All charts end at the same moment, the last step the aggregates are computed for, so that no chart drops to zero at its
right edge.
