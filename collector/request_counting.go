package collector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coroot/coroot/clickhouse"
)

const (
	// serverSpanMemory is how long a server is known to record its side of a
	// protocol after its last such span: long enough for a server called now
	// and then (a cron), short enough for one that stopped recording (its agent
	// replaced by an older one) to have its clients counted again soon.
	serverSpanMemory = time.Hour
	// serverSpanSourcesRefresh is how often the sources are read from
	// ClickHouse, which has those of every Coroot replica.
	serverSpanSourcesRefresh = time.Minute
	// serverSpanSourcesRetry is how often a batch tries to read them until it
	// has read them once: right after a start the tables may not be ready yet.
	serverSpanSourcesRetry = time.Second
	// serverSpanSourcesQueryTimeout bounds a read of the sources.
	serverSpanSourcesQueryTimeout = 10 * time.Second
	// serverSpanSourcesMaxHold bounds how long a new batch holds its spans
	// until the sources are read, so that a request recorded at both sides is
	// not counted twice right after a start; the agents never wait for it.
	// Past it the spans are counted with what is known.
	serverSpanSourcesMaxHold = 30 * time.Second
)

// requestProtocol is the protocol of the request of a span, as the client and
// the server spans of one request both tell it; "" for anything else.
func requestProtocol(span map[string]string) string {
	switch {
	case span["db.system"] != "":
		return span["db.system"]
	case span["rpc.system"] == "grpc" || span["rpc.grpc.status_code"] != "":
		return "grpc"
	case span["http.method"] != "" || span["http.request.method"] != "":
		return "http"
	}
	return ""
}

// serverSpanSources remembers the servers that record their side of requests:
// for each server (named as the Server column names it) and protocol, when the
// node agent and when an SDK last sent such a server span. It decides which
// span of a request the views count, so that a request recorded at both sides
// is counted once (the Counted column):
//   - a server span of an SDK always counts;
//   - a server span of the node agent counts unless the server's SDK records
//     the same requests;
//   - a client span counts unless the server records its side of that
//     protocol: a database, an external service, a server with an older agent
//     are counted by their clients.
//
// It learns from the spans it counts and from trace_server_sources, which
// ClickHouse fills from the stored server spans of every Coroot replica: a
// restarted collector knows the servers at once. A server not known yet (a
// new one, before its first server span) has its first requests counted at
// both sides: an overcount for a moment, never a request not counted.
type serverSpanSources struct {
	lock      sync.Mutex
	byAgent   map[serverProtocol]time.Time
	bySDK     map[serverProtocol]time.Time
	lastPrune time.Time
}

type serverProtocol struct {
	server   string
	protocol string
}

// serverSpanSource is a row of trace_server_sources: when a server last sent a
// server span of a protocol, from the node agent or from an SDK.
type serverSpanSource struct {
	server    string
	protocol  string
	fromAgent bool
	lastSeen  time.Time
}

func newServerSpanSources() *serverSpanSources {
	return &serverSpanSources{byAgent: map[serverProtocol]time.Time{}, bySDK: map[serverProtocol]time.Time{}}
}

// counted records a span and tells whether the views count it. fromAgent is a
// span of the node agent, not of an SDK.
func (s *serverSpanSources) counted(spanKind string, fromAgent bool, server, protocol string, now time.Time) bool {
	key := serverProtocol{server: server, protocol: protocol}
	s.lock.Lock()
	defer s.lock.Unlock()
	s.prune(now)
	recent := func(sources map[serverProtocol]time.Time) bool {
		at, ok := sources[key]
		return ok && now.Sub(at) < serverSpanMemory
	}
	switch spanKind {
	case "SPAN_KIND_SERVER":
		if protocol == "" {
			return true
		}
		if !fromAgent {
			s.bySDK[key] = now
			return true
		}
		s.byAgent[key] = now
		return !recent(s.bySDK)
	case "SPAN_KIND_CLIENT":
		if protocol == "" {
			return true
		}
		return !recent(s.byAgent) && !recent(s.bySDK)
	}
	return true
}

// remember adds the sources read from ClickHouse, keeping the later time of a
// source known both ways.
func (s *serverSpanSources) remember(sources []serverSpanSource) {
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, source := range sources {
		key := serverProtocol{server: source.server, protocol: source.protocol}
		known := s.bySDK
		if source.fromAgent {
			known = s.byAgent
		}
		if source.lastSeen.After(known[key]) {
			known[key] = source.lastSeen
		}
	}
}

// prune forgets, now and then, the servers not heard of for serverSpanMemory.
func (s *serverSpanSources) prune(now time.Time) {
	if now.Sub(s.lastPrune) < serverSpanMemory {
		return
	}
	s.lastPrune = now
	for _, sources := range []map[serverProtocol]time.Time{s.byAgent, s.bySDK} {
		for key, at := range sources {
			if now.Sub(at) >= serverSpanMemory {
				delete(sources, key)
			}
		}
	}
}

// loadServerSpanSources reads the sources of the last serverSpanMemory from
// trace_server_sources.
func loadServerSpanSources(ctx context.Context, client *clickhouse.Client) ([]serverSpanSource, error) {
	rows, err := client.Query(ctx, fmt.Sprintf(
		"SELECT Server, Protocol, FromAgent, max(LastSeen) FROM @@table_trace_server_sources@@ "+
			"WHERE LastSeen > now() - INTERVAL %d SECOND GROUP BY Server, Protocol, FromAgent",
		int(serverSpanMemory.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []serverSpanSource
	for rows.Next() {
		var source serverSpanSource
		var fromAgent uint8
		if err = rows.Scan(&source.server, &source.protocol, &fromAgent, &source.lastSeen); err != nil {
			return nil, err
		}
		source.fromAgent = fromAgent == 1
		sources = append(sources, source)
	}
	return sources, rows.Err()
}
