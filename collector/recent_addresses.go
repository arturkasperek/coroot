package collector

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/coroot/coroot/clickhouse"
)

// recentAddressMemory is how long an address learned from a span is used: as
// long as the window of the world the application directory is built from
// (applicationDirectoryWindow), by when the world knows the pod.
const recentAddressMemory = 10 * time.Minute

// recentAddressesRefresh is how often a collector reads the addresses the
// spans of every Coroot replica taught.
const recentAddressesRefresh = 5 * time.Second

// recentAddresses are the addresses of the pods the world does not know yet (a
// pod started a moment ago), learned from the spans of their node agents: a
// server span tells where its pod received a request, a client span where it
// sent one from. The application directory, built from the world, stays what
// names an address: these only name the addresses it does not know, for the
// minute or two until the world has the pod. A collector learns them from the
// spans it receives and reads, every recentAddressesRefresh, those the spans
// of every Coroot replica taught (trace_addresses).
type recentAddresses struct {
	lock      sync.Mutex
	byListen  map[string]recentAddress // ip:port a pod received a request on
	byIp      map[string]recentAddress // the IP of a pod
	lastPrune time.Time
}

type recentAddress struct {
	application string
	seen        time.Time
}

// recentAddressRow is a row of trace_addresses: where a server span received
// a request (ip:port) or a client span was sent from (ip), the application of
// that side, and when last.
type recentAddressRow struct {
	address     string
	application string
	lastSeen    time.Time
}

func newRecentAddresses() *recentAddresses {
	return &recentAddresses{byListen: map[string]recentAddress{}, byIp: map[string]recentAddress{}}
}

// learn records the addresses of a span of a node agent. application is the
// span's own side, as the directory names it.
func (a *recentAddresses) learn(directory *ApplicationDirectory, spanKind, application string, span map[string]string, now time.Time) {
	switch spanKind {
	case "SPAN_KIND_SERVER":
		if ip, port := span["server.address"], span["server.port"]; ip != "" && port != "" {
			a.record(directory, net.JoinHostPort(ip, port), application, now)
		}
	case "SPAN_KIND_CLIENT":
		if ip := span["network.local.address"]; ip != "" {
			a.record(directory, ip, application, now)
		}
	}
}

// remember records the rows read from trace_addresses.
func (a *recentAddresses) remember(directory *ApplicationDirectory, rows []recentAddressRow) {
	for _, row := range rows {
		a.record(directory, row.address, row.application, row.lastSeen)
	}
}

// record records that a pod had an address (ip:port it received a request on,
// or ip it sent one from) when seen, unless an address seen later is known.
// Only a pod (an application of a namespace) is recorded, by an address of its
// network: not a loopback, which every pod has its own, nor a link-local one;
// an IP only when it is known not to be a node's, which the processes of the
// node share.
func (a *recentAddresses) record(directory *ApplicationDirectory, address, application string, seen time.Time) {
	if strings.HasPrefix(application, "/") || !strings.Contains(application, "/") {
		return
	}
	networkIp := func(ip string) bool {
		parsed := net.ParseIP(ip)
		return parsed != nil && !parsed.IsLoopback() && !parsed.IsUnspecified() && !parsed.IsLinkLocalUnicast()
	}
	known := recentAddress{application: application, seen: seen}
	set := func(addresses map[string]recentAddress, key string) {
		if current, ok := addresses[key]; !ok || seen.After(current.seen) {
			addresses[key] = known
		}
	}
	ip, _, err := net.SplitHostPort(address)
	listen := err == nil
	if !listen {
		ip = address
	}
	if !networkIp(ip) {
		return
	}
	a.lock.Lock()
	defer a.lock.Unlock()
	a.prune(time.Now())
	if listen {
		set(a.byListen, address)
	}
	if directory != nil && !directory.nodeIps[ip] {
		set(a.byIp, ip)
	}
}

// loadRecentAddresses reads the addresses of the last recentAddressMemory from
// trace_addresses, each with the application that had it last.
func loadRecentAddresses(ctx context.Context, client *clickhouse.Client) ([]recentAddressRow, error) {
	rows, err := client.Query(ctx, fmt.Sprintf(
		"SELECT Address, argMax(Application, LastSeen), max(LastSeen) FROM @@table_trace_addresses@@ "+
			"WHERE LastSeen > now() - INTERVAL %d SECOND GROUP BY Address",
		int(recentAddressMemory.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var addresses []recentAddressRow
	for rows.Next() {
		var row recentAddressRow
		if err = rows.Scan(&row.address, &row.application, &row.lastSeen); err != nil {
			return nil, err
		}
		addresses = append(addresses, row)
	}
	return addresses, rows.Err()
}

// lookup names the application at an address (ip:port or an IP) learned
// within recentAddressMemory.
func (a *recentAddresses) lookup(address string, now time.Time) (string, bool) {
	a.lock.Lock()
	defer a.lock.Unlock()
	known, ok := a.byListen[address]
	if !ok {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}
		known, ok = a.byIp[host]
	}
	if !ok || now.Sub(known.seen) >= recentAddressMemory {
		return "", false
	}
	return known.application, true
}

// clients are the IPs of the pods learned within recentAddressMemory: pods
// with a node agent, which records the client side of their requests.
func (a *recentAddresses) clients(now time.Time) []string {
	a.lock.Lock()
	defer a.lock.Unlock()
	var ips []string
	for ip, known := range a.byIp {
		if now.Sub(known.seen) < recentAddressMemory {
			ips = append(ips, ip)
		}
	}
	return ips
}

// prune forgets, now and then, the addresses not seen for recentAddressMemory.
func (a *recentAddresses) prune(now time.Time) {
	if now.Sub(a.lastPrune) < recentAddressMemory {
		return
	}
	a.lastPrune = now
	for _, addresses := range []map[string]recentAddress{a.byListen, a.byIp} {
		for address, known := range addresses {
			if now.Sub(known.seen) >= recentAddressMemory {
				delete(addresses, address)
			}
		}
	}
}
