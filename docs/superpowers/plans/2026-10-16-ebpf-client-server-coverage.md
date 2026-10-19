# eBPF traces: both sides of every request, bodies stored once

Status: phases 1–6 implemented (not committed), tests green, checked on dev.
Repositories: `coroot` and the node agent fork `coroot-node-agent`.

Implemented differently than planned:

- Phase 2: instead of `ServerHasAgent` (from the directory), the collector
  decides `Counted` from the server spans (`serverSpanSources`,
  `collector/request_counting.go`): a server known to record its side of a
  protocol in the last hour is counted by its SERVER spans, an SDK's SERVER
  span wins over the agent's. The views filter `Counted = 1`. The sources are
  kept in ClickHouse (`trace_server_sources`, a materialized view of the
  SERVER spans by `Server`, `Protocol`, `FromAgent`), read by each collector at
  start and every minute: a restart or another replica counts no request
  twice. `Client`, `Server` and `Counted` are decided when a batch is written,
  not when the spans arrive: right after a start the batch holds its spans
  (at most 30 s, at most 10 batches) until it has read the sources and the
  first application directory is built; the agents never wait.
- New pods: the application directory (from the world) stays what names an
  address; an address it does not know yet is named from the spans of the
  node agents (`collector/recent_addresses.go`): a SERVER span tells where its
  pod received a request (`server.address`/`server.port`), a CLIENT span where
  it sent one from (`network.local.address`, added by the agent). Only pods
  are learned, never a node IP, a loopback or a link-local one; for 10
  minutes, by when the world knows the pod. These IPs are traced clients too.
  Several Coroot replicas share them through ClickHouse (`trace_addresses`, a
  materialized view of the agents' spans: address → application, `LastSeen`),
  which each collector reads every 5 s; the rules above are applied when it is
  read.
  The directory gives a reused IP to the pod alive and seen last.
- Phase 5: the covered IPs come from a separate endpoint,
  `/v1/traced-clients`, refreshed every 30 s; the agent adds the IPs of its own
  node's pods every 5 s. HTTP/1 bodies are skipped in the kernel by the body
  walk (`l7/http1.c`); HTTP/2 DATA frames are kept as their 9-byte headers
  only (length 0, for their END_STREAM) by the frame walker (`l7/http2.c`,
  `http2_trim_args.no_bodies`).
- Phase 6: DB server spans are one per read syscall (as the client side); a
  pipeline is not split. Planned per protocol in
  `2026-10-18-pipelined-protocols.md`.
- A server that forks after accept (Postgres: the postmaster accepts, the
  backend it forks reads): the eBPF side finds the socket of a connection the
  process did not open or accept from its file descriptor
  (`task->files->fdt->fd[fd]`, `socket_of_fd` in `tcp/state.c`), within the
  syscall on it, and announces it with its addresses, flagged as not seen open
  (its requests are not numbered, no `RequestKey`). Its direction is the one
  the first syscall tells (read: inbound).
- A process forked for one connection lives a few milliseconds, gone before
  the agent reads its `/proc`: the eBPF side records the parent of each process
  (`process_parents`, read by `Tracer.ParentPid`), a gone process belongs to
  the container of its parent (`goneProcessContainer`), and its container
  makes a stand-in process with the parent's flags and network namespace
  (`processOf`), forgotten after `RecentlyExitedTTL` (looked at every 30 s,
  `goneProcessesInterval`). Whether a container has eBPF traces disabled is
  counted as processes come and go (`tracesDisabledProcesses`), not looked up
  among them at every event. `node_l7_inbound_dropped_no_process_total` counts
  the inbound events with no process to record them on. The exit of a process
  (from its own ring) can be handled before its last L7 events: a process with
  inbound connections leaves their state to such a stand-in. The agent's
  docker e2e requires a server span for every request of `python-fork`, with
  and without bodies.
- A batch learns the addresses of all its spans before it names peers: an
  agent sends the spans of each container together, so the server spans of a
  short-lived client (psql run once) come before its client spans. A pod with
  no owner, which the agent names by its container id, is named after the pod
  (`unknownApplicationName`), as the world names it.

## Where we are (done, not committed)

- `otel_traces` and `otel_traces_histogram` have `Client` and `Server`, filled by
  the collector at ingestion from the world of the project, as `namespace/name`
  (`coroot-dev/express-demo`), the name alone with no namespace (`k3s`,
  `api.stripe.com:443`), the address when nothing knows it (`10.99.0.1:5432`).
  An application the world does not know yet is named in the same format from
  what its span says.
- `Server` leads the sorting keys. Namespace on the Traces page is the part of
  `Server` before the slash. `ServiceName` stays: the OTel standard field, who
  recorded the span.
- Traces page: facets Source, Status, Namespace, Server, Client, API Route, Root
  span name; table column Client → Server; requests and latency charts.
- Node agent: Kubernetes resource attributes from the container id, the HTTP
  address attributes of an instrumented server, a span of an HTTP call is an
  error on 5xx only (as Coroot's SLIs count failed requests).
- `ApiRoute` also derives the route from `http.url`.

## The model

1. **Both sides record.** The node agent of the client makes a CLIENT span for
   every outbound call, as today; the node agent of the server makes a SERVER
   span for every inbound request. A request between two pods with agents has
   two spans: two measurements (the client's time with the network, the
   server's time), told apart by `SpanKind`. Nothing is lost when one side has
   no agent: a request from an ALB, the internet or a pod on Fargate has its
   SERVER span; a call to RDS or an external API has its CLIENT span. No rule
   decides which side records, so no list error or delay can lose a request.
   Databases get both spans too.
2. **Each view counts one kind per request.** Requests of a service with an
   agent are counted by its SERVER spans; calls to a server with no agent by the
   CLIENT spans. The collector marks each CLIENT span at ingestion: its `Server`
   is a pod on a node with an agent, or not (`ServerHasAgent`). For a pod too new
   to be known the mark is wrong for a few dozen seconds and such a request is
   counted from both sides: a short overcount, never a loss.
3. **Bodies are stored once, by their hash.** The collector hashes each request
   and response body and writes it to `trace_bodies (Hash, Body, LastSeen)`
   (ReplacingMergeTree by `Hash`, TTL from `LastSeen` longer than the spans');
   spans keep only the hashes. The two sides of a request carry the same bytes,
   and repeated bodies (the same JSON answers, health checks, errors) are stored
   once as well. Saves disk, not the network nor the ring buffer.
4. **The server skips the bodies a client agent captures, in the kernel.**
   `--http-body-capture` is the same on every agent (all or nothing). Coroot
   gives each agent, with the config it already fetches (`/v1/config`), the IPs
   of the pods of its cluster that run on nodes with an agent — not the node IPs
   (a SNATed request from outside comes from a node IP), not other clusters
   (pod ranges overlap). The agent adds the pods of its own node at once. The
   list goes to an eBPF map; at accept a connection from a listed IP is flagged,
   and the read/write probes (TLS uprobes too) copy only the start of the
   messages of a flagged connection: enough for the span and the inbound metrics
   the SLIs are computed from, without the bodies. No list, or an IP not on it,
   means the server captures the body too: stored once anyway thanks to the hash.
5. **A SERVER span finds the body of its request in the CLIENT span, when it is
   read.** Both agents compute the same `RequestKey` without talking to each
   other: for HTTP/1 `hash(client IP, client port, server IP, server port, number
   of the request on the connection, method, path)`; for HTTP/2 and gRPC the
   stream id instead of the number. A side that did not see the connection open
   (it was open before the agent started) writes no key. The details of a SERVER
   span with no body look up the CLIENT span with the same key within a few
   seconds (a 4-tuple is not reused for 60 s, `TIME_WAIT`), check its method,
   path and time, and show its bodies; on any doubt they show none — no body
   rather than another request's. No buffer in the collector: it stays stateless
   and works with several Coroot replicas. Databases need no key: each side
   captures the query itself.

Rejected or postponed: deciding which side records from a list (loses requests
when the list is late); synthesizing a SERVER span from a CLIENT one; holding
spans in the collector to join them; rewriting stored spans; a Kubernetes
informer in Coroot; removing `ServiceName`; a union of the lists of several
clusters.

## Phases

In order of risk, each one useful on its own, its tests green and checked on the
dev cluster; the user commits between phases.

### Phase 1 — Bodies by hash (Coroot)

- Schema: `trace_bodies`; `otel_traces` gets `RequestBodyHash` and
  `ResponseBodyHash` (bloom filter indexes) instead of `RequestBody` and
  `ResponseBody`.
- Collector: hash the bodies (xxHash), write each new body once per batch to
  `trace_bodies`, the hashes to the spans.
- The single-trace view loads the bodies by hash; the body search moves to
  `trace_bodies` (the text index goes there) and comes back to the spans by
  hash.
- Tests: storage e2e (a body twice stored once, the search finds the spans of a
  body, TTL by `LastSeen`), the existing body round-trip tests, the API test of
  the trace view.

### Phase 2 — Every view counts one kind per request (Coroot)

Before the agent sends SERVER spans, so that the counts never double.

- Collector: `ServerHasAgent` on CLIENT spans, from the directory (the server is
  a pod on a node with an agent).
- Count SERVER spans, plus CLIENT spans whose server has no agent, in: the trace
  list, the requests and latency charts, the facets, the overview, error causes,
  latency explorer, compare attributes, the Tracing report of application pages,
  the traces of SLO violations, the MCP trace tools. The list of places is
  checked against the code before starting.
- Tests: an e2e fixture with the CLIENT and SERVER spans of the same requests
  and CLIENT spans to an external server: every view counts each request once.

### Phase 3 — The agent records the server side of HTTP/1

- From the inbound parsing that already feeds the inbound metrics
  (`observeInboundL7`): a SERVER span with the same attributes as a client span,
  `client.address`/`client.port`, the 5xx error rule, the span of the receiving
  container, its body.
- Both sides: the number of the request on the connection, whether the agent saw
  the connection open, `RequestKey`; the CLIENT span also gets its local port.
- Measure on dev how many more spans this means.
- Tests: agent unit tests (kind, attributes, keys of the requests of a
  keep-alive connection, no key for a connection open before the agent);
  docker e2e (a request between containers has a CLIENT and a SERVER span with
  the same key; a request from outside has a SERVER span). Dev: the demo
  traffic counts once per request; `curl` from the host to a NodePort appears.

### Phase 4 — The body of a SERVER span from its CLIENT span (Coroot)

- The details of a SERVER span with no body look up the CLIENT span by
  `RequestKey` in a window of a few seconds, check method, path and time, and
  show its bodies.
- Tests: keep-alive with the same path many times, a connection open before the
  agent, a lost event (numbers out of step), a missing CLIENT span.

### Phase 5 — The server skips covered bodies in the kernel (agent + Coroot)

- Coroot: the covered pod IPs of the cluster in `/v1/config`.
- Agent: the list (plus its own node's pods) in an eBPF map; the flag at accept;
  the probes copy only the start of the messages of flagged connections, TLS
  included.
- Measure first how much of the ring buffer the inbound bodies of covered pods
  take.
- Tests: the inbound metrics are the same with and without the flag; a flagged
  connection's SERVER span has no body and phase 4 shows it; a listed IP's
  connection open before the list arrived still captures; the agent's docker
  e2e and the latency tests.

### Phase 6 — HTTP/2, gRPC and the databases on the server side (agent)

- Check how the inbound parsers split what arrives together — a Redis pipeline
  of 10 commands must give 10 spans, the extended protocol of Postgres one span
  per query — and fix them before turning their SERVER spans on.
- HTTP/2 and gRPC keys by stream id.

### Later, if needed

- Join the two spans of a request into one trace by `RequestKey` (the SERVER span
  a child of the CLIENT span).
- Database keys, once the parsers count single queries on both sides.

## Open points

- ~~Facets count up to `Ctx.To` while the charts reach one step further.~~
  Done: the charts, the facets and the list count one window
  (`clickhouse.TracesWindow`): steps of seconds up to an hour of view, from
  the start of a bucket, to now for a live view or to the end of a bucket.
- Kubelet probes come from the node IP (`10.42.0.1`), which no application has.
- `Server = external:9000` on dev is the port-forward to ClickHouse.
- Several Coroot replicas: the model needs no affinity (no state in the
  collector), but the directory is built per replica.
