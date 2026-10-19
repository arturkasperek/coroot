# One span in a thousand lost with body capture under load

Status: to investigate, nothing of it started. Repository: the node agent fork
`coroot-node-agent`. Found while finishing
`2026-10-16-ebpf-client-server-coverage.md` (short-lived processes).

## Symptom

The agent's docker e2e with body capture and every target at once
(`HTTP_BODIES=1 make docker-test-e2e`) fails now and then because one request
has no span. The e2e requires every request to have its span (ratio 1.0).

Runs on 2026-10-17/18, on the remote docker host (k3s node, host CPU about 44 %
busy during the load):

| Run | Result |
| --- | --- |
| bodies, all targets | pass |
| bodies, all targets | `python-fork` server spans 499/500, `GET /users` n=257 |
| bodies, all targets | pass |
| bodies, all targets | `python-fork` server spans 499/500, `POST /echo` n=53 |
| bodies, all targets, before the short-lived process fixes | `node-tls` client spans 499/500, `GET /error` n=369 |
| bodies, only `python-fork` (`ONLY=python-fork`) | pass |
| no bodies, all targets (5 runs) | pass |
| no bodies, all targets (2026-10-18, later) | `php-h1` client spans 499/500, then `node-tls` client spans 499/500 |
| latency (`make docker-test-latency`, 2026-10-18) | `node-tls` 3185 spans for ~3323 requests (4 % lost); every other case 3323 |
| no bodies, only `python-fork`, `N_REQUESTS=5000` | 5000/5000 |

A different request each time, never the first one. Later runs lost client
spans without bodies too (`php-h1`, `node-tls`), and the latency benchmark
loses 4 % of `node-tls` (Node over OpenSSL, HTTP/1, a new connection per
request) consistently: start there, it fails every time. It happens only with
bodies on and only with every target loaded at once. It happened on the server
side of a forking server (`python-fork`) and once on the client side of a TLS
target (`node-tls`, no fork). So it may not be about forking at all.

What the counters said in the failing runs:

- `node_ebpf_lost_events_total`: 0 for `l7_events`, `tcp_connect_events` and
  `perf`, so the eBPF side lost no event to a full ring.
- `node_traces_spans_dropped_total`: 0, so the export queue dropped nothing.
- `node_l7_inbound_dropped_no_process_total`: 0, so no inbound event lacked a
  process (the path the stand-in processes fixed).
- `node_l7_dropped_unknown_container_total` grows by 1000–3000 per run and
  `node_l7_http1_dropped_no_connection_total` by about 900. Neither tells
  which pid or target, so they cannot be ruled in or out.

## Steps

1. **Lost or misattributed?** `verify` counts a server span of a target only
   when its `server.address:server.port` is the target's and its
   `http.target` has the run's `r=`. Dump every span of the mock backend that
   has the missing `n=` (`verify` prints it) whatever its attributes. A span
   with the wrong address, a cut `http.target` or no `n=` means misattributed
   or truncated, not lost. Add this dump to `verify` for a failing target.
2. **Attribute the existing drop counters.** Log at `-v=2`, rate limited, or
   count by a label: the pid, its comm and whether the event is inbound for
   `node_l7_dropped_unknown_container_total` and
   `node_l7_http1_dropped_no_connection_total`. Then check whether the missing
   request's process (the `python-fork` child) or connection is among them.
3. **Count the places a parsed request can vanish** without a span, each
   with a counter:
   - an inbound parser replaced because `connTimestamp` changed while it held
     requests (`observeInboundL7`);
   - parsers deleted with requests still held: connection close
     (`onConnectionClose`, which looks up `c.processes[e.Pid]`, not
     `processOf`), `Container.gc` (`ReadFds`), `forgetGoneProcesses`;
   - a response with no request, or a request with no response, dropped by
     the HTTP/1 parser with bodies (`l7.Http1Parser`, the body options);
   - `node_l7_http_bodies_total{result="dropped_no_message"}`, which counts
     bodies whose message was not found.
4. **eBPF side with bodies on:** compare `node_l7_payloads_truncated_total`
   and the HTTP/1 body walk (`l7/http1.c`, `HTTP1_CAPTURE_MAX`) between
   passing and failing runs. A request whose head is cut can fail to parse,
   and the `node-tls` miss points at capture rather than process tracking.
5. **Make it fail more often:** raise the load (`N_REQUESTS=2000`, a higher
   `CONCURRENCY`) and run `HTTP_BODIES=1` several times. Record the failure
   rate before and after each fix.

## Reproduce

```
cd coroot-node-agent
DOCKER_CONTEXT=server HTTP_BODIES=1 make docker-test-e2e
# a failure prints e.g.:
#   FAIL   target=python-fork  server spans: 499 of 500 requests
#            POST   /echo  -> server spans MISSING n=[53]
```

The remote docker host drops ssh now and then (exit status 255): retry those.

## Done when

- The cause is found and fixed, or shown to be outside the agent (the
  harness, the mock backend).
- 10 runs of `HTTP_BODIES=1 make docker-test-e2e` in a row pass, plus a few
  runs at `N_REQUESTS=2000`.
- Every drop counter added in step 3 stays as a metric.
