# Coroot with several replicas: what is left

Status: plan for later, nothing of it started. Repository: `coroot`.

## Where we are (done 2026-10-19, not committed)

Coroot runs 2 replicas by default (`deploy/kind/coroot.yaml`), sharing one
Postgres (required now, SQLite removed) and one ClickHouse cluster. These were
fixed, each with a test that fails without the fix:

- the primary lock (`db/lock.go`): one stable leader, no races, released on
  SIGTERM;
- projects created or reconfigured on another replica get their ClickHouse
  tables (`collector/migration.go`, `collector.setProjects`);
- `GetTo` on a replica that is not the primary (`world/client.go`), and the
  scan of `world_points` it caused (`world/store.go`);
- notifications sent by the primary only, serialized (`notifications/`);
- MCP sessions across replicas (`api/mcp_sessions.go`, `db/mcp_session.go`);
- migrations of Postgres and ClickHouse one replica at a time (`db.WithLock`).

Tests: `e2e/replicas_test.go` (fresh pages on every replica, a project created
on one replica ingesting on the other, an MCP session across replicas, the
primary failing over with `COROOT_E2E_FAILOVER=1`), `e2e/storage/primary_lock_test.go`,
`e2e/storage/mcp_session_test.go`, `notifications/replicas_test.go`.

What follows was found by the same audit and left for later.

## 0. Leader election by a lease in Postgres (first)

### Why

The primary lock is a session advisory lock (`db/lock.go`): it ends when
Postgres sees the session end.

- **Clean shutdown:** a stop, a rollout or a deleted pod closes the
  connection, so the lock goes at once. `TestPrimaryFailsOver` shows this:
  about 26 s, mostly the new pod starting.
- **Hard failure:** the node dies, loses power or is cut from the network.
  Nothing closes the TCP connection. Postgres finds the dead session only by
  TCP keepalive, which is about 2 hours on a default Linux.
  - Until then there is no primary: no world evaluation (pages go stale), no
    alerts, no incidents, no notifications.
- Server keepalive settings (`tcp_keepalives_*`) would shorten that, but they
  are the database owner's configuration, not ours.

A Kubernetes Lease (`client-go` leaderelection) was considered and rejected:
- it works in Kubernetes only, and needs RBAC on `leases`;
- the leader is not tied to the database its jobs need, so a leader that lost
  Postgres keeps the Lease and nobody runs the jobs;
- it gives no fencing.

### Design

A row with a time to live, in the database every replica already shares.

```sql
CREATE TABLE IF NOT EXISTS leader (
    id         TEXT NOT NULL PRIMARY KEY,   -- 'primary'
    holder     TEXT NOT NULL,               -- the instance: pod name + a random id per start
    expires_at TIMESTAMPTZ NOT NULL
)
```

**One goroutine per instance (the elector)** every `leaseRenew` (5 s) runs one
statement:

```sql
INSERT INTO leader (id, holder, expires_at) VALUES ('primary', $me, now() + $ttl)
ON CONFLICT (id) DO UPDATE SET holder = $me, expires_at = now() + $ttl
WHERE leader.holder = $me OR leader.expires_at < now()
RETURNING holder
```

- A row returned means this instance is the leader until `now() + ttl`.
  - `leaseTTL` is 20 s.
  - Only Postgres's clock is used (`now()`), never the replicas'.
- No row returned means another instance holds the lease. The elector notes
  when that lease expires and keeps trying.

**State in memory.** The elector stores "leader until T" in a mutex or atomic
value, where T is measured on the instance's monotonic clock from the moment
the statement was sent, minus a safety margin, e.g. `ttl - 2*renew`.
- `GetPrimaryLock(ctx)` keeps its signature and only reads that state: true
  while now < T. No database round trip per call, and no race, since the
  elector is the only writer.
- A leader whose renewal fails (Postgres is unreachable) stops being the
  leader by itself at T. That is before the lease expires in Postgres, so two
  leaders never overlap if the margin holds.

**Release.** On SIGTERM, `db.Close()` sets `expires_at = now()` for
`holder = $me`, so the next instance takes over within one renewal (≤ 5 s).

**Fencing in the jobs.** The jobs check `GetPrimaryLock` before each cycle
(the evaluator, watchers, notifications), as they do now.
- A long cycle (an evaluation of up to 2 minutes, `runProject`) also checks
  between projects. It stops if the instance is no longer the leader.
- Optional: writes of the evaluator carry the holder, so a duplicate can be
  traced.

**Advisory locks.**
- `GetPrimaryLock` no longer uses one.
- `db.WithLock` stays for the migrations, which are short. It gets a
  `SET lock_timeout` (e.g. 2 minutes) on its connection, so that a replica
  does not wait forever behind a migration of a node that died mid-way: it
  fails, backs off and retries.

### Timings

| | |
|---|---|
| leaseRenew | 5 s |
| leaseTTL | 20 s |
| local margin | the instance stops acting as leader 10 s before the TTL ends |
| clean failover | ≤ 5 s (released at SIGTERM) |
| hard failover | ≤ 20 s + 5 s |

### Tests

- **Storage (two `db.DB` on one test database, as in
  `e2e/storage/primary_lock_test.go`):**
  - exactly one leader, stable over many renewals and calls from several
    goroutines (`-race`);
  - the leader stops renewing (its elector is stopped without releasing, like
    a hard crash): the other becomes leader after the TTL and not before;
  - a clean `Close` hands over within one renewal;
  - a leader that cannot reach Postgres stops being the leader before its
    lease expires: no moment with two leaders.
- **Unit:** the "leader until T" arithmetic with a fake clock.
- **Dev:**
  - `TestPrimaryFailsOver` (clean);
  - a new hard-failure test: a NetworkPolicy that cuts the primary pod from
    Postgres, or `kubectl exec` killing its network. The other replica must
    become primary within about 25 s and keep the pages fresh.
  - after both, no duplicate points in `world_points`
    (`count() - uniqExact(Query, SeriesHash, Timestamp)` over the window).

### Migration

- The `leader` table is created by `db.Migrate`.
- `GetPrimaryLock`'s callers do not change.
- No data to migrate (no compatibility to keep).

## 1. The `For` of alerting rules restarts after a failover

- **What:** `watchers/alerts.go` keeps `pendingAlerts` (when a rule with
  `For` was first seen firing) and `initializedProjects` in memory, on the
  primary only.
- **When it bites:** when the primary changes, the new one starts every `For`
  timer again, so an alert fires up to `For` later. If leadership comes back
  to the old primary within `cleanupPendingAlerts` (30 minutes), its old
  `firstSeen` can make an alert that resolved and fired again fire at once,
  without waiting its `For`.
- **Why it is rare now:** the lock no longer flips; this needs a real failover
  (a pod restart, a rollout, a lost Postgres connection). With the lease of
  section 0, failovers become faster but not rarer.
- **Fix:** keep the pending state in Postgres: a pending row per (project,
  rule, application) with its first-seen time, read by whichever replica is
  the primary and deleted when the alert fires or the condition clears.
- **Tests:** a storage test with two `db.DB` (two replicas): the first
  primary sees a rule firing, the second takes over and fires it exactly
  `For` after the first sighting; plus `TestPrimaryFailsOver` with a `For`
  rule on dev.

## 2. Saving a project on the "no API key" path can lose a settings change

- **What:** `collector/collector.go` `getProject` with an empty API key
  appends a default key to the cached project (a pointer shared by readers,
  modified under a read lock: a data race) and saves the whole settings blob,
  which can be up to 10 s old (the project cache refresh).
- **When it bites:** a user changes the project's settings on one replica
  while an agent with no API key reaches the other in the same 10 s: the
  change is overwritten.
- **Fix:** copy the project, re-read it inside a transaction
  (`SELECT ... FOR UPDATE`) and add the key only if it is still missing; do
  not touch the cached pointer.
- **Tests:** a storage test with two replicas, and `go test -race`.

## 3. Agents are not spread over the replicas

- **What:** OTLP gRPC and HTTP keep-alive connections stay on the pod the
  Service picked when they were opened, so one replica can get most of the
  agents. Correct (any replica ingests any project), but uneven.
- **Fix options:** a max connection age on the gRPC server
  (`keepalive.ServerParameters{MaxConnectionAge}`) and `Connection: close`
  after N requests or M minutes on HTTP; or an L7 load balancer in front.
- **Tests:** on dev, the request counts of both replicas over 10 minutes
  (a metric of the collector per replica) within 30 % of each other.

## 4. The Helm chart and the operator must require Postgres

- **What:** they live in other repositories. Their default is SQLite with a
  persistent volume, which this Coroot no longer starts with.
- **Fix:** make `postgres` mandatory (or bring a Postgres by default), switch
  `DATA_DIR` to an `emptyDir` (nothing in it needs to survive now: the
  instance uuid and the cloud pricing cache are rebuilt), and allow
  `replicas > 1` without conditions.

## 5. Smaller things

- **Readiness while migrating:** `/health` answers 200 while a replica has
  not created a project's ClickHouse tables yet (it rejects that project's
  telemetry with "tables not ready"). Make readiness wait for the first
  migration of every project, or answer 503 to ingestion so that agents retry
  on the other replica.
- **Startup races on settings:** `auth_secret` (`api/auth.go`) and
  `GetDeploymentUuid` are generated by every replica when missing;
  `SetSetting` does UPDATE then INSERT, and the second INSERT fails, so that
  replica exits and restarts once. Use `INSERT ... ON CONFLICT DO NOTHING`
  and read the value back. The same for the default project and the admin
  user (`config/bootstrap.go`, `db/user.go`): unique constraints keep them
  right, at the cost of one crash-restart.
- **Bootstrap re-saves project settings on every start** (`config/bootstrap.go`):
  during a rolling update it can overwrite a change made on the other replica
  in the same moment. Save only what the config defines, or only when it
  differs.
- **Counting of a new server across replicas:** `collector/request_counting.go`
  reads the server span sources every minute (`serverSpanSourcesRefresh`). A
  server first seen by one replica is unknown to the other for up to a minute,
  so the other counts the client spans of its requests too (a short
  overcount). Shorten to 10 s if it shows.
- **The evaluator's last error** (`world/client.go` `lastError`) is in memory
  of the primary only: `GetStatus` shows an evaluation error only when the
  request hits the primary; on the other replica a failing project shows a
  growing lag with no message, and after a failover the new primary has no
  error until its first cycle.
  - Store the status of the last cycle in Postgres, written by the primary at
    the end of `runProject`: a goose migration with a table
    `world_status (project_id PRIMARY KEY, error TEXT, evaluated_to BIGINT,
    duration_ms INTEGER, updated_at TIMESTAMPTZ)`, upserted per cycle, rows
    deleted with the project (`DeleteProject`).
  - `GetStatus` reads the error from it on every replica (a cache of a few
    seconds, like `lastEvaluatedTTL` in `world/store.go`, is enough); an `updated_at` older than a few
    periods means no replica evaluates the project (no primary), worth
    showing as such.
  - The data of the world itself needs nothing: every replica reads it from
    ClickHouse (`world_points*`, `world_series`); only where a page ends
    differs, by up to `lastEvaluatedTTL` (5 s).
  - Test (`e2e/storage`): a cycle failing on one evaluator shows its error in
    the status read through another evaluator on the same databases.
- **Per-replica duplicates that are only waste:** the application directory
  (a `LoadWorld` of 10 minutes every 30 s per project, per replica), the cloud
  pricing download, the usage statistics (each replica reports with its own
  instance uuid; the stats reporter is to be removed anyway,
  `2026-10-07-remove-usage-statistics.md`).
- **PgBouncer:** `WithLock` uses session advisory locks, which do not hold
  behind PgBouncer in transaction mode. The lease of section 0 does not need
  a session, so it works there. Document that the migrations need a direct
  connection (or session mode), or move them to the lease too.
- **TTL of existing ClickHouse tables:** `@ttl_*` is filled from the config
  when a table is created (version 1 of `ch.Migrations`); a changed
  `*_TTL` later does not reach existing tables. Reconcile at the collector's
  migration pass: compare `system.tables.engine_full` with the config and run
  `ALTER TABLE ... @on_cluster MODIFY TTL ...` when they differ.

## Order

0 (the lease: without it a hard failure of the primary's node leaves Coroot
without a primary for hours), then 1, then 5 (readiness, startup races), then
2, then 3. 4 is in other repositories and goes with the first production
deployment.
