package collector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRequestProtocol(t *testing.T) {
	assert.Equal(t, "http", requestProtocol(map[string]string{"http.method": "GET"}))
	assert.Equal(t, "http", requestProtocol(map[string]string{"http.request.method": "GET"}))
	assert.Equal(t, "grpc", requestProtocol(map[string]string{"http.method": "POST", "rpc.grpc.status_code": "0"}))
	assert.Equal(t, "grpc", requestProtocol(map[string]string{"rpc.system": "grpc"}))
	assert.Equal(t, "postgresql", requestProtocol(map[string]string{"db.system": "postgresql", "db.statement": "SELECT 1"}))
	assert.Equal(t, "", requestProtocol(map[string]string{"messaging.system": "kafka"}))
}

// A request recorded at both sides is counted once: by the server's span when
// the server records its side of that protocol, by the client's span when it
// does not (a database, an external service, an older agent).
func TestSpanCounted(t *testing.T) {
	now := time.Now()
	servers := newServerSpanSources()
	const client, server = "SPAN_KIND_CLIENT", "SPAN_KIND_SERVER"

	// nothing recorded at coroot-dev/express-demo yet: its clients count
	assert.True(t, servers.counted(client, true, "coroot-dev/express-demo", "http", now))

	// its agent records the server side of HTTP
	assert.True(t, servers.counted(server, true, "coroot-dev/express-demo", "http", now))
	assert.False(t, servers.counted(client, true, "coroot-dev/express-demo", "http", now), "the server's span counts the request")
	assert.False(t, servers.counted(client, false, "coroot-dev/express-demo", "http", now), "an SDK's client span too")
	assert.True(t, servers.counted(client, true, "coroot-dev/express-demo", "grpc", now), "not a protocol it records")
	assert.True(t, servers.counted(client, true, "coroot-dev/postgres", "postgresql", now), "a server that records nothing")
	assert.True(t, servers.counted(client, true, "api.stripe.com:443", "http", now))

	// the SDK of flask-demo records its server side: its agent's server spans
	// are a second record of the same requests
	assert.True(t, servers.counted(server, false, "coroot-dev/flask-demo", "http", now))
	assert.False(t, servers.counted(server, true, "coroot-dev/flask-demo", "http", now))
	assert.False(t, servers.counted(client, true, "coroot-dev/flask-demo", "http", now))

	// internal spans and messaging are never a second record
	assert.True(t, servers.counted("SPAN_KIND_INTERNAL", false, "coroot-dev/flask-demo", "", now))
	assert.True(t, servers.counted("SPAN_KIND_PRODUCER", false, "coroot-dev/kafka", "", now))
	assert.True(t, servers.counted("SPAN_KIND_CONSUMER", false, "coroot-dev/worker", "", now))

	// a server that stopped recording (its agent replaced by an older one) has
	// its clients counted again once it is forgotten
	later := now.Add(serverSpanMemory + time.Second)
	assert.True(t, servers.counted(client, true, "coroot-dev/express-demo", "http", later))
}

// The sources read from ClickHouse count as server spans seen at their time;
// a later span already seen here is not moved back by an older row.
func TestServerSpanSourcesRemember(t *testing.T) {
	now := time.Now()
	servers := newServerSpanSources()
	const client, server = "SPAN_KIND_CLIENT", "SPAN_KIND_SERVER"

	assert.True(t, servers.counted(server, false, "coroot-dev/flask-demo", "http", now), "flask-demo's SDK span, seen here now")
	servers.remember([]serverSpanSource{
		{server: "coroot-dev/express-demo", protocol: "http", fromAgent: true, lastSeen: now.Add(-50 * time.Minute)},
		{server: "coroot-dev/flask-demo", protocol: "http", fromAgent: false, lastSeen: now.Add(-50 * time.Minute)},
		{server: "coroot-dev/catalog", protocol: "grpc", fromAgent: true, lastSeen: now.Add(-2 * serverSpanMemory)},
	})
	assert.False(t, servers.counted(client, true, "coroot-dev/express-demo", "http", now), "its agent records the server side")
	assert.True(t, servers.counted(client, true, "coroot-dev/catalog", "grpc", now), "not recorded within serverSpanMemory")
	assert.False(t, servers.counted(server, true, "coroot-dev/flask-demo", "http", now.Add(15*time.Minute)),
		"the SDK span seen here a moment ago, not the older row, is what is remembered")
}
