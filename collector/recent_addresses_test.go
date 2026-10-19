package collector

import (
	"net"
	"testing"
	"time"

	"github.com/coroot/coroot/model"
	"github.com/stretchr/testify/assert"
)

// testDirectoryWithNode is testApplicationDirectory with the node of the dev
// cluster: its IP and the IP of its CNI bridge, which kubelet probes come from.
func testDirectoryWithNode() *ApplicationDirectory {
	directory := testApplicationDirectory()
	directory.world.Nodes = append(directory.world.Nodes, &model.Node{NetInterfaces: []*model.InterfaceStats{
		{Name: "eth0", Addresses: []string{"192.168.100.76"}},
		{Name: "cni0", Addresses: []string{"10.42.0.1"}},
	}})
	return NewApplicationDirectory(directory.world)
}

// A pod started a moment ago is not in the world yet: the spans of its node
// agent tell its addresses (the server side tells where it received a request,
// the client side where it sent one from), and the collector names it by them
// until the world knows it.
func TestRecentAddressesNameAPodTheWorldDoesNotKnowYet(t *testing.T) {
	now := time.Now()
	directory := testDirectoryWithNode()
	addresses := newRecentAddresses()

	addresses.learn(directory, "SPAN_KIND_SERVER", "coroot-dev/catalog", map[string]string{"server.address": "10.42.0.200", "server.port": "8080", "client.address": "10.42.0.150"}, now)
	addresses.learn(directory, "SPAN_KIND_CLIENT", "coroot-dev/basket", map[string]string{"server.address": "10.42.0.140", "server.port": "3000", "network.local.address": "10.42.0.201"}, now)

	name, ok := addresses.lookup("10.42.0.200:8080", now)
	assert.True(t, ok)
	assert.Equal(t, "coroot-dev/catalog", name, "where it received a request")
	name, _ = addresses.lookup("10.42.0.200:9090", now)
	assert.Equal(t, "coroot-dev/catalog", name, "a pod IP is the pod's on every port")
	name, _ = addresses.lookup("10.42.0.201", now)
	assert.Equal(t, "coroot-dev/basket", name, "where it sent a request from")

	_, ok = addresses.lookup("10.42.0.200:8080", now.Add(recentAddressMemory+time.Second))
	assert.False(t, ok, "by then the world knows it")
	assert.ElementsMatch(t, []string{"10.42.0.200", "10.42.0.201"}, addresses.clients(now), "pods with a node agent: traced clients")
}

// A node IP is shared by the processes of the node's network namespace (and
// is where kubelet probes and SNATed requests come from): it is never taken
// for one application's. A port of it is one process's.
func TestRecentAddressesNeverTakeANodeIpForAnApplication(t *testing.T) {
	now := time.Now()
	directory := testDirectoryWithNode()
	addresses := newRecentAddresses()

	addresses.learn(directory, "SPAN_KIND_SERVER", "monitoring/node-exporter", map[string]string{"server.address": "192.168.100.76", "server.port": "9100"}, now)
	addresses.learn(directory, "SPAN_KIND_CLIENT", "kube-system/kube-proxy", map[string]string{"network.local.address": "10.42.0.1"}, now)

	name, ok := addresses.lookup("192.168.100.76:9100", now)
	assert.True(t, ok)
	assert.Equal(t, "monitoring/node-exporter", name)
	_, ok = addresses.lookup("192.168.100.76:22", now)
	assert.False(t, ok)
	_, ok = addresses.lookup("10.42.0.1", now)
	assert.False(t, ok)
	assert.Empty(t, addresses.clients(now))

	// every pod has its own loopback: it is no one's
	for _, loopback := range []string{"127.0.0.1", "::1"} {
		addresses.learn(directory, "SPAN_KIND_CLIENT", "coroot-dev/basket", map[string]string{"network.local.address": loopback}, now)
		addresses.learn(directory, "SPAN_KIND_SERVER", "coroot-dev/basket", map[string]string{"server.address": loopback, "server.port": "8080"}, now)
		_, ok = addresses.lookup(net.JoinHostPort(loopback, "8080"), now)
		assert.False(t, ok, loopback)
	}
	assert.Empty(t, addresses.clients(now))

	// before the first world the node IPs are not known: no IP is learned
	addresses = newRecentAddresses()
	addresses.learn(nil, "SPAN_KIND_CLIENT", "coroot-dev/basket", map[string]string{"network.local.address": "10.42.0.201"}, now)
	_, ok = addresses.lookup("10.42.0.201", now)
	assert.False(t, ok)
}

// Only what a span's own side is known as is learned: an application of a
// namespace, not a process of a node (named differently by the world), and
// not the peer of a span.
func TestRecentAddressesLearnOnlyPods(t *testing.T) {
	now := time.Now()
	directory := testDirectoryWithNode()
	addresses := newRecentAddresses()
	addresses.learn(directory, "SPAN_KIND_SERVER", "/system.slice/k3s.service", map[string]string{"server.address": "10.42.0.210", "server.port": "6443"}, now)
	addresses.learn(directory, "SPAN_KIND_INTERNAL", "coroot-dev/catalog", map[string]string{"server.address": "10.42.0.211", "server.port": "80"}, now)
	_, ok := addresses.lookup("10.42.0.210:6443", now)
	assert.False(t, ok)
	_, ok = addresses.lookup("10.42.0.211:80", now)
	assert.False(t, ok)
}

// The addresses the spans received by other Coroot replicas taught, read from
// trace_addresses, name pods as those learned here do, with the same rules: a
// node IP is no one's, and the pod seen last has an address.
func TestRecentAddressesFromOtherReplicas(t *testing.T) {
	now := time.Now()
	directory := testDirectoryWithNode()
	addresses := newRecentAddresses()
	addresses.learn(directory, "SPAN_KIND_CLIENT", "coroot-dev/basket", map[string]string{"network.local.address": "10.42.0.201"}, now.Add(-3*time.Minute))
	addresses.remember(directory, []recentAddressRow{
		{address: "10.42.0.200:8080", application: "coroot-dev/catalog", lastSeen: now.Add(-time.Minute)},
		{address: "10.42.0.201", application: "coroot-dev/orders", lastSeen: now.Add(-time.Minute)}, // basket's IP, now orders'
		{address: "192.168.100.76", application: "kube-system/kube-proxy", lastSeen: now},
		{address: "10.42.0.202", application: "coroot-dev/payments", lastSeen: now.Add(-recentAddressMemory - time.Minute)},
	})

	name, _ := addresses.lookup("10.42.0.200:8080", now)
	assert.Equal(t, "coroot-dev/catalog", name)
	name, _ = addresses.lookup("10.42.0.200", now)
	assert.Equal(t, "coroot-dev/catalog", name, "the IP of the pod")
	name, _ = addresses.lookup("10.42.0.201", now)
	assert.Equal(t, "coroot-dev/orders", name, "the pod seen last")
	_, ok := addresses.lookup("192.168.100.76", now)
	assert.False(t, ok, "a node IP")
	_, ok = addresses.lookup("10.42.0.202", now)
	assert.False(t, ok, "too old")

	addresses.remember(directory, []recentAddressRow{{address: "10.42.0.201", application: "coroot-dev/basket", lastSeen: now.Add(-2 * time.Minute)}})
	name, _ = addresses.lookup("10.42.0.201", now)
	assert.Equal(t, "coroot-dev/orders", name, "an older row does not take it back")
}
