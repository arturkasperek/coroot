# The "HTTP" protocol of the ClickHouse integration does not work

Status: plan for later, nothing of it started. Repository: `coroot`.

To be superseded by `2026-10-27-database-per-project.md` (decided
2026-10-28): the per-project ClickHouse form goes away and the global
ClickHouse is native only, which is option A below. Only if that plan is
dropped is this one needed.

## What was seen (2026-10-20)

A query through `clickhouse.NewClient` with `Protocol: "http"` against the
dev ClickHouse HTTP port (`127.0.0.1:18123`) fails:

```
[handshake] unexpected packet [72] from server
```

72 is `H`, the start of an HTTP response: a native-protocol handshake was
sent to the HTTP port. The error is the same with clickhouse-go 2.8.3 and
2.17.1, so it is not caused by the upgrade that came with goose (see
`2026-10-20-schema-versioning.md`).

The probe was a throwaway test in `e2e/storage`:

```go
config := &db.IntegrationClickhouse{Protocol: "http", Addr: "127.0.0.1:18123",
	Database: "default", Auth: utils.BasicAuth{User: "default"}}
client, err := clickhouse.NewClient(config, &db.Project{Name: "probe"})
rows, err := client.Query(ctx, "SELECT name FROM system.tables WHERE database = currentDatabase()")
```

## Why it can't work as it is

The integration form offers three protocols: Native, HTTP, Coroot
(`front/src/views/IntegrationClickhouse.vue:11-15`). Only the read path
looks at `Protocol`: `clickhouse.NewClient` (`clickhouse/clickhouse.go`, on
clickhouse-go) sets `opts.Protocol = clickhouse.HTTP`.

Everything else goes through `ch.NewLowLevelClient` (`ch/client.go`), which
uses ch-go. ch-go speaks only the native protocol and ignores `Protocol`:

- ingestion of every signal (`collector/collector.go:300`);
- the schema migrations (`collector/migration.go:78,87`);
- the world evaluator's metrics (`world/run.go:75`);
- the cluster discovery (`LowLevelClient.info`).

So a project set to HTTP with an HTTP port neither ingests, nor migrates,
nor evaluates. On top of that, the form's check
(`api/forms/forms.go:384`, `IntegrationFormClickhouse.Test`) only pings
through clickhouse-go. If that ping passes over HTTP, the user can save a
configuration that the collector then rejects in its logs only.

## To find out first

1. Why the read path sends a native handshake too, although `opts.Protocol`
   is `HTTP`:
   - Is it the query itself, or `ch.ReplaceTables`/`getClusterTopology` going
     through something native?
   - Does clickhouse-go's `Open` with `Protocol: HTTP` need `http://` in
     `Addr`, or another option in this version?
   - Run the probe with `Debug: true` in `clickhouse.Options`.
2. Whether anyone uses HTTP:
   - the project settings in production Postgres:
     `SELECT id FROM project WHERE settings::json->'integrations'->'clickhouse'->>'protocol' = 'http'`;
   - the config file / environment (`config/config.go`, `config/project.go`).

## Options

- **A (recommended): remove the HTTP option.**
  - Remove the radio button from the form.
  - `IntegrationFormClickhouse.Valid` rejects any protocol other than
    `native` and `coroot`.
  - `clickhouse.NewClient` drops the `"http"` case.
  - A stored `http` setting gets a clear error on the integration page and
    in the collector log ("the HTTP protocol is not supported, use the
    native port 9000"), instead of a handshake error.
  - Nothing else depends on HTTP, and before production this is a cheap
    breaking change.
- **B: support HTTP everywhere.** Replace ch-go in ingestion, migrations and
  the world evaluator with clickhouse-go over HTTP. This is a large change:
  the ingestion uses ch-go's columnar blocks for speed, and would need to be
  rewritten and benchmarked. Only worth it if a deployment can reach
  ClickHouse over HTTP only (e.g. behind an HTTP proxy or ClickHouse Cloud
  with the native port closed).

Whichever is chosen, make `IntegrationFormClickhouse.Test` check what the
collector will use: open a `ch.NewLowLevelClient` too (it runs the cluster
discovery), so that a configuration the collector can't use is rejected
when it is saved.

## Tests

- **Unit:** `IntegrationFormClickhouse.Valid` with `http` → false (option A).
- **Storage (`e2e/storage`):** `Test` of the form with the native protocol
  on the HTTP port fails with a readable error; with the native port it
  passes.
- **e2e:** saving an HTTP integration through the API returns an error and
  leaves the project's settings unchanged.
