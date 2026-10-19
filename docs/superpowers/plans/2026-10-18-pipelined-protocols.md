# One span per request for pipelined and multiplexed protocols

Status: plan, nothing of it started. Repository: the node agent fork
`coroot-node-agent` (eBPF `ebpftracer/ebpf/l7/`, parsers `ebpftracer/l7/`,
spans `containers/container.go` `recordQuery`). Coroot needs no change once
each request is its own span.

## The problem

The agent makes one L7 event per request it matched to a response, and one
span per event. A client that sends several requests before it reads their
responses (pipelining), or many concurrent requests on one connection
(multiplexing), loses requests:

- **One pending request per connection.** `active_l7_requests` is keyed by
  (pid, fd, is_tls, stream_id), with stream_id -1 for every protocol but
  Cassandra and DNS. A request written while one is pending is not recorded
  (`BPF_NOEXIST`). Only ClickHouse replaces it (`BPF_ANY`).
- **One request per write.** The protocol is recognized from the start of the
  written buffer, and up to `MAX_PAYLOAD_SIZE` (1024) bytes of it are copied.
  A write with ten Redis commands is one request: the parser
  (`l7.ParseRedis`, `PostgresParser.Parse`, ...) reads the first command.
- **One response per read.** The response is recognized from the start of
  the read buffer (`is_redis_response`, `is_postgres_response`, ...). Its end
  ends the pending request. Further replies in the same read, or in later
  reads, have no request left, so they are dropped.
- **The duration** is from the write to the first read that looks like a
  response: for a pipeline, the time of the first reply, not of each.
- **A request with no response** (fire-and-forget: Mongo w:0, Kafka acks=0,
  Memcached `noreply`, MySQL `COM_STMT_CLOSE`) can stay pending and be
  matched to the response of a later request: a wrong duration and a wrong
  status.

Both sides are affected alike: the server spans of a database (inbound) use
the same matching and `recordQuery`.

What it costs: fewer requests than were made (Redis pipelines, Kafka
producers, Dubbo services under load), latencies that belong to the first
request of a batch, and errors of later requests not seen.

## What right means

For every request of a connection, one span:

- **its own statement or command:** the parser gets the bytes of that
  message, not of the batch;
- **start:** when the write that carried it was made, so the requests of
  one write share it;
- **end:** when the read that completed its response was made, so a reply
  that comes later than the first gets a longer duration;
- **its own status:** from its own response;
- **a request with no response:** a span with no duration (or none, per
  protocol below), and never matched to another request's response.

The counts must hold on both sides, and `Counted` keeps working: each side
records the same requests.

## Common building blocks

Each protocol below uses some of these. They are built once.

- **A. Frame walk of a write.** Walk the message headers of the written
  buffer with `bpf_loop`, as the HTTP/2 walker does (`l7/http2.c`): one
  pending record per message, with its offset, its type and, for multiplexed
  protocols, its id. Copy only a slice of each message (its head) for the
  parser: the whole write is not copied, `MAX_PAYLOAD_SIZE` stays the bound
  per message. A message cut by the end of the buffer continues in the next
  write: per-connection state with how many bytes of it are left.
- **B. In-order pending queue** per connection, for protocols that answer in
  order (Redis, PostgreSQL, Memcached text, ZooKeeper, MySQL multi-results):
  a bounded ring (for example 32) in the connection's map value or in its own
  map keyed by the connection. When it is full, count the requests not
  recorded (`node_l7_pipeline_overflow_total{protocol}`), never a wrong
  match.
- **C. Pending map by id** for protocols that tag requests (Kafka correlation
  id, Cassandra stream id, Dubbo request id, MongoDB requestID, FoundationDB
  token): `l7_request_key` gets a 64-bit request id in place of the 16-bit
  `stream_id`. Entries not answered within a timeout (or at close) are
  dropped and counted.
- **D. Frame walk of a read.** Find the reply boundaries in the read buffer:
  for ordered protocols, every complete reply pops the queue; for tagged
  ones, every reply header names its request. A reply cut by the end of the
  buffer is continued in the next read (per-connection skip state, as HTTP/2
  `h2_skip_*`). The event of a request is sent when its reply is complete,
  with the time of that read.
- **E. One event per request.** The event carries the request's own payload
  slice and its own status and duration. User space parsers then stay "one
  message in, one span out", which is what they do today.
- **F. No reply expected.** Per protocol, the requests flagged as having no
  response (below) are sent as events with no duration at once and are not
  queued.

### The decision to make first: where the framing is done

1. **In eBPF (A–D as above).** It is exact in time and copies only message
   heads, and the ring buffer carries what it carries today. But it adds
   verifier cost: every walker must fit the instruction budget, as
   `http2.c` shows, so the walkers should be written per protocol family
   (length-prefixed, RESP text, line-based).
2. **In user space.** eBPF sends every write and read of a connection whose
   protocol is pipelined (head of each, plus lengths and timestamps), and Go
   does the framing and matching. The eBPF code is simpler, but it sends many
   more events and needs per-connection reassembly in Go, with the read time
   of each buffer to keep durations.

Recommendation: 1 for the length-prefixed and tagged protocols (Kafka,
Cassandra, MongoDB, Dubbo, ZooKeeper, PostgreSQL, MySQL), where a header gives
the length. Redis and Memcached text need line scanning: measure the verifier
cost of a RESP walker first, and fall back to 2 for them if it does not fit.
Decide this with a spike on Redis and Kafka before the rest.

## Per protocol

### Redis (RESP2/RESP3)

- **Who pipelines:** every client's pipeline API; auto-pipelining clients
  (ioredis, Lettuce, go-redis with pipelines) under load; `MULTI` ... `EXEC`.
- **Framing:** a command is a RESP array `*N\r\n` of bulk strings
  `$len\r\n...\r\n`; inline commands (`PING\r\n`) are plain lines. Replies
  are in order. One reply can be nested (arrays of arrays, RESP3 maps).
- **Today:** one span per write, with the first command; the reply is checked
  from its first byte.
- **To do:** B + A (count and slice each command of a write) + D (walk the
  replies, a nested array with its element count). `MULTI`: each queued
  command answers `+QUEUED`, and `EXEC` answers an array of the real replies.
  Record the commands with the `EXEC` reply's time and the status of their
  element in it.
- **Edge cases:**
  - subscribed mode (`SUBSCRIBE`, RESP3 push): messages with no request, not
    queued, maybe consume events;
  - client-side caching invalidations (RESP3 push);
  - blocking commands (`BLPOP`) with long durations, which are right;
  - a reply larger than a read, continued by D.
- **Tests:**
  - a pipeline of 100 commands with one failing (`INCR` on a string), giving
    100 spans and one error;
  - `MULTI`/`EXEC`;
  - the async auto-pipelining of a Node client;
  - client and server spans equal in count.

### PostgreSQL

- **Who pipelines:**
  - libpq pipeline mode (PG 14+);
  - pgx `SendBatch`;
  - JDBC batches (`addBatch`, `reWriteBatchedInserts`);
  - asyncpg `executemany`;
  - Npgsql batching;
  - a simple query with several statements (`SELECT 1; SELECT 2`).
- **Framing:** messages are a type byte plus a 32-bit length. In the extended
  protocol a client sends Parse/Bind/Describe/Execute for many statements and
  one Sync. Replies are ParseComplete, BindComplete, RowDescription,
  DataRow..., CommandComplete per Execute, and ReadyForQuery per Sync. After
  an ErrorResponse the server skips everything until the Sync.
- **Today:** one span per write. `PostgresParser` keeps prepared statements
  by name, so one Parse/Bind/Execute is one query. The response is checked
  at the start of the read.
- **To do:**
  - A walks the messages of a write. Each Execute (and each simple Query) is
    a request with its statement, from Parse or from the named statement.
  - B queues them. D walks the replies: a CommandComplete or ErrorResponse
    ends the request in front.
  - After an error, the queued requests up to the Sync failed without
    running: record them with an error status and a note, or not at all.
    Decide which; recording them is closer to what the client sees.
  - A simple Query with several statements: one span per statement (one
    CommandComplete each). The parser splits the text.
- **Edge cases:**
  - COPY (CopyIn/CopyOut sub-protocol);
  - asynchronous NotificationResponse, ParameterStatus and NoticeResponse in
    the reply stream, which must be skipped, not matched;
  - portals with a row limit (Execute with max rows, PortalSuspended);
  - cancel requests on another connection.
- **Tests:**
  - pgx batch of 50 with one failing statement in the middle;
  - libpq pipeline mode;
  - JDBC `reWriteBatchedInserts`;
  - multi-statement simple query;
  - a notification arriving while queries are pending.

### MySQL / MariaDB

- **Who pipelines:**
  - the classic protocol is one command at a time, but multi-statements
    (`CLIENT_MULTI_STATEMENTS`, `SELECT 1; SELECT 2`) give several result
    sets (`SERVER_MORE_RESULTS_EXISTS`);
  - MariaDB Connector/J and the MariaDB C connector pipeline
    `COM_STMT_PREPARE` + `COM_STMT_EXECUTE` (+ `COM_STMT_CLOSE`) in one
    write, and bulk executes;
  - X Protocol (port 33060) pipelines but is not parsed at all: out of
    scope.
- **Framing:** a 3-byte length, a sequence id, then the payload. A write can
  carry several commands, each with sequence id 0.
- **Today:** one span per write. `COM_STMT_CLOSE` is sent as its own event.
  Multi-results end at the first result set.
- **To do:**
  - A splits the commands of a write; B queues those that answer
    (`COM_STMT_CLOSE` and `COM_STMT_SEND_LONG_DATA` do not answer, see F).
  - D follows the result sets of a command up to the last one without
    `SERVER_MORE_RESULTS_EXISTS`.
  - Multi-statements: one span per statement, split by the parser, ended by
    its result set.
- **Tests:**
  - MariaDB Connector/J with pipelining on;
  - a multi-statement query with an error in the second statement;
  - large result sets spanning many reads.

### Memcached

- **Who pipelines:** clients that batch (spymemcached, pymemcache
  `set_many` with `noreply`), and `get k1 k2 ...`, which is one command with
  many replies (`VALUE ...` lines then `END`).
- **Framing (text):** a command line, a data block for storage commands
  (`<bytes>` from the command), and one reply per command. `noreply` commands
  answer nothing. The binary protocol (quiet commands `getq`/`setq` followed
  by `noop`) is a different framing. Check what `is_memcached_query`
  recognizes; binary may not be parsed at all.
- **To do:** A (command lines, skipping data blocks by their length) + B +
  D (one reply per command, `END` closes a `get`) + F (`noreply`). Binary:
  a header with opaque ids gives C.
- **Tests:** a batch of 100 `set ... noreply` then a `get`; a multi-key
  `get`; binary quiet gets if binary is in scope.

### MongoDB

- **Who pipelines:** official drivers send one operation per connection at a
  time (a connection pool for concurrency), so ordering is not the issue.
  But:
  - unacknowledged writes (`w: 0`, OP_MSG flag `moreToCome` on the request)
    have no response: today they stay pending and are matched to the next
    request's reply;
  - exhaust cursors (flag `exhaustAllowed`) get many replies (each with
    `moreToCome`) for one request: today only the first is seen;
  - every message has `requestID` and `responseTo`.
- **To do:**
  - C keyed by `requestID` (a reply names its request in `responseTo`)
    replaces the single slot;
  - F for `moreToCome` requests;
  - an exhaust cursor is one span from the request to the last reply (the
    one without `moreToCome`), or one span per batch; decide which.
  - Today's partial-reply handling (`is_mongo_response` returning 2) moves
    into D.
- **Tests:** writes with `w: 0` between reads, so their durations must not
  be taken by the reads; an exhaust cursor (change streams use getMore, not
  exhaust; use `cursor.addOption(exhaust)` or a driver that supports it);
  compressed messages (OP_COMPRESSED) are not parseable without
  decompression, so count them as unknown.

### Kafka

- **Who pipelines:** producers keep up to `max.in.flight.requests.per.connection`
  (5 by default) Produce requests in flight. Consumers have Fetch requests
  in flight along with heartbeats and offset commits. Admin clients send
  Metadata.
- **Framing:** a 32-bit length, then api key, api version and correlation id.
  Responses are length plus correlation id, in order per connection.
- **Today:** the correlation id is checked, but there is one slot per
  connection (a new request keeps the earlier start time), so only one
  request in flight is recorded.
- **To do:** C keyed by correlation id (or B, since replies are in order;
  C also catches a reply with no request). F: Produce with `acks=0` has no
  response. A: a write can hold several requests.
- **Tests:** a producer with in-flight 5 and `linger.ms` 0 under load, giving
  one span per Produce; a consumer group with Fetch and heartbeats at once;
  `acks=0`.

### Cassandra (native protocol)

- **Who pipelines:** every driver multiplexes up to 32k concurrent requests
  per connection by stream id, and the DataStax drivers coalesce several
  requests into one write (write coalescing). Protocol v5 wraps frames in
  segments, which can carry several frames, have a checksum, and can be LZ4
  compressed.
- **Today:** keyed by stream id, so concurrency works, but only the first
  frame of a write and of a read is parsed.
- **To do:**
  - A and D over frames (a 9-byte header with the body length) to see every
    frame in a buffer;
  - v5 segments: walk the segment headers, but a compressed segment hides its
    frames, so count it as unknown;
  - v4 compression compresses only the body, so the header is still readable
    and the statement is not (as today).
- **Tests:** concurrent queries from the DataStax Java driver (coalescing on),
  giving one span per query on both sides; protocol v5 uncompressed.

### ZooKeeper

- **Who pipelines:** the client's async API and Curator send many requests
  without waiting. Replies come in order, each with the request's xid.
  Watches arrive with xid -1, pings use xid -2, auth -4, and set-watches -8.
- **Today:** one slot per connection, with partial replies kept
  (`is_zk_request`, `is_zk_response` returning 2).
- **To do:** B (or C by xid) + A + D. The negative xids are not requests:
  pings are skipped and watch events can become consume events.
- **Tests:** 100 async `exists` calls at once; a watch firing while requests
  are pending.

### Dubbo2

- **Who pipelines:** Dubbo shares one long-lived connection per provider
  among all concurrent calls, so many requests are in flight. A 16-byte
  header carries a 64-bit request id, request/response and two-way flags,
  and an event flag (heartbeats).
- **Today:** one slot per connection, so under concurrency most calls are
  lost.
- **To do:** C by request id + A + D. F: one-way calls (two-way flag not set)
  have no response. Heartbeats (event flag) are not calls.
- **Tests:** 50 concurrent calls on one connection; one-way calls;
  heartbeats.

### FoundationDB

- **Who pipelines:** the client multiplexes all requests to a server over one
  connection; packets are routed by endpoint token, and replies come back on
  a reply endpoint.
- **Today:** one slot per connection with partial replies, which is likely
  to miss most requests.
- **To do:** research first. Check what `is_foundationdb_request` matches
  and whether the reply carries something that names its request. If it does
  not, the honest count is per packet, not per request: say so and record
  packets as such. This one may not be worth it; decide after the research.

### NATS

- **Who pipelines:** clients buffer and flush many `PUB`/`HPUB` in one write.
  The server delivers many `MSG`/`HMSG` in one read.
- **Today:** one produce event per write, one consume event per read.
- **To do:** A and D as line-based walkers, counting every `PUB` and every
  `MSG` (each with its payload length to skip). No request/response matching
  is needed. A request-reply over NATS (`_INBOX` subjects) is two messages;
  keep it so.
- **Tests:** a client publishing 1000 messages without flush in between,
  giving 1000 produce events; a subscriber receiving batches.

### RabbitMQ (AMQP 0-9-1)

- **Who pipelines:** publishers write many `Basic.Publish`
  (method + content header + body frames) per write. Consumers get many
  `Basic.Deliver` per read (prefetch).
- **Today:** one produce event per write that starts with a publish method
  frame, and consume events by read.
- **To do:** A and D over AMQP frames (a 7-byte header with the size, then
  the frame end byte), counting every publish and every deliver.
  Optionally, time a publish to its `Basic.Ack` when publisher confirms are
  on (delivery tags in order).
- **Tests:** a publisher of 1000 messages in batches, and a consumer with
  prefetch 100.

### Checked, not affected

- **ClickHouse native:** one query at a time per connection. The streamed
  response is handled (the query is kept until EndOfStream, and a new query
  replaces an unanswered one). Nothing to do.
- **DNS:** keyed by id, one query per datagram. Nothing to do.
- **HTTP/1:** parsed in user space from the stream (`l7.Http1Parser`), which
  takes several requests in a row. Pipelined HTTP/1 is rare. Add a test of
  pipelined requests to confirm; it is not part of this plan.
- **HTTP/2, gRPC:** streams, handled by the HTTP/2 walker.

## Order of work

By how much is lost today and how many users have it:

1. The spike that settles the framing location (Redis and Kafka).
2. The building blocks B, C, F with an overflow metric, then Redis and
   Kafka.
3. PostgreSQL (batches, multi-statements), MongoDB (the `w: 0` mismatch is a
   correctness bug: do it early), Dubbo2.
4. Cassandra, ZooKeeper, MySQL/MariaDB, Memcached.
5. NATS, RabbitMQ (counts of messages).
6. FoundationDB research.

Each protocol is its own change with its tests, on both sides.

## Tests for every protocol

- **Parser unit tests** (`ebpftracer/l7`): a captured pipeline (testdata
  `.bin` files, as for ClickHouse) giving N messages with their statements.
- **Agent docker e2e:** a service per protocol with its pipelining client,
  checking that the number of spans equals the number of requests and that
  per-request errors are on the right spans, on the client and the server
  side.
- **Latency e2e** (`make docker-test-latency`): the walkers must not slow
  the traced applications. Compare p50/p99 with the agent off and on, before
  and after.
- **Verifier:** the programs load on the oldest kernel the agent supports
  (the CI kernel matrix, if there is one; the minimum kernel in the README).
- **Overflow:** a pipeline deeper than the queue gives the overflow counter
  and no mismatched span.
