package collector

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"golang.org/x/exp/maps"
	"k8s.io/klog"
)

// TracedClientsResponse answers a node agent with the IPs of the pods whose
// requests a node agent records at the client (the pods on the nodes that run
// one): a server skips the bodies of their requests, which their client spans
// carry. Ready is false while the collector does not know the world of the
// project yet; the agent then captures every body.
type TracedClientsResponse struct {
	Ready   bool     `json:"ready"`
	Clients []string `json:"clients"`
}

// TracedClients serves /v1/traced-clients from the application directory the
// collector keeps for the project, built from its world every few seconds, so
// that the agents of every node can ask often without loading the world, and
// from the pods the world does not know yet whose agents sent spans.
func (c *Collector) TracedClients(w http.ResponseWriter, r *http.Request) {
	project, err := c.getProject(r.Header.Get(ApiKeyHeader))
	if err != nil {
		klog.Errorln(err)
		if errors.Is(err, ErrProjectNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	var res TracedClientsResponse
	if directory, _ := c.getApplicationDirectory(project); directory != nil {
		res.Ready = true
		clients := map[string]bool{}
		for _, ip := range directory.TracedClients() {
			clients[ip] = true
		}
		for _, ip := range c.getTracesBatch(project).RecentClients() {
			clients[ip] = true
		}
		res.Clients = maps.Keys(clients)
		sort.Strings(res.Clients)
	}
	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(res); err != nil {
		klog.Errorln(err)
	}
}
