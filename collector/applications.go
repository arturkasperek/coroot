package collector

import (
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
)

// ApplicationDirectory tells the applications on the two sides of a span: the
// client that sent the request and the server that handled it. A span says
// only one side by itself (its service), and the other by an address, so the
// directory maps the addresses, pods, containers, Kubernetes services and
// OpenTelemetry service names of the world to applications, named as
// applicationName names them.
type ApplicationDirectory struct {
	world *model.World

	byListen      map[string]string // ip:port the application listens on
	byIp          map[string]string // IP of a pod
	byContainerId map[string]string // the node agent's container id
	byPod         map[string]string // namespace/pod
	byService     map[string]string // name.namespace and ClusterIP of a Kubernetes service
	byTracing     map[string]string // the tracing service set in the application settings

	guessedLock sync.Mutex
	guessed     map[string]string // OpenTelemetry service name → application name, "" when none

	// tracedClients are the IPs of the pods on nodes with a node agent: the
	// agent records the client side of their requests.
	tracedClients []string

	// nodeIps are the IPs of the nodes: an IP shared by the processes of a
	// node in its network namespace, never one application's.
	nodeIps map[string]bool
}

// applicationName names an application in the Client and Server columns:
// namespace/name for an application of the cluster (a Deployment, a
// StatefulSet, a pod with no owner, ...), the name alone for one with no
// namespace: a process of a node, an external service. Kubernetes names have
// no slash, so the namespace is what comes before the first one.
func applicationName(id model.ApplicationId) string {
	if id.Namespace == "" || id.Namespace == "_" || id.Kind == model.ApplicationKindExternalService {
		return id.Name
	}
	return id.Namespace + "/" + id.Name
}

// addressOwner is an application that had an address in the window of the
// world. An address can have had several: the IP of a deleted pod is given to
// a new one, a port is listened on by a new process.
type addressOwner struct {
	application string
	alive       bool            // the pod is not deleted, the listen is active
	lastSeen    timeseries.Time // when the pod was last seen
	traced      bool            // a pod on a node with a node agent
}

// replaces tells whether the owner is the address's rather than other: the
// one alive, then the one seen last.
func (owner addressOwner) replaces(other addressOwner) bool {
	if owner.alive != other.alive {
		return owner.alive
	}
	return owner.lastSeen > other.lastSeen
}

func NewApplicationDirectory(w *model.World) *ApplicationDirectory {
	d := &ApplicationDirectory{
		world:         w,
		byListen:      map[string]string{},
		byIp:          map[string]string{},
		byContainerId: map[string]string{},
		byPod:         map[string]string{},
		byService:     map[string]string{},
		byTracing:     map[string]string{},
		guessed:       map[string]string{},
		nodeIps:       map[string]bool{},
	}
	ipOwners := map[string]addressOwner{}
	listenOwners := map[string]addressOwner{}
	own := func(owners map[string]addressOwner, address string, owner addressOwner) {
		if current, ok := owners[address]; !ok || owner.replaces(current) {
			owners[address] = owner
		}
	}
	for id, app := range w.Applications {
		name := applicationName(id)
		for _, instance := range app.Instances {
			if instance.Pod != nil {
				if instance.Pod.IP != "" {
					lastSeen, _ := instance.Pod.LifeSpan.LastNotNull()
					own(ipOwners, instance.Pod.IP, addressOwner{
						application: name,
						alive:       !instance.Pod.IsObsolete(),
						lastSeen:    lastSeen,
						traced:      instance.Node != nil && instance.Node.AgentVersion.Value() != "",
					})
				}
				d.byPod[id.Namespace+"/"+instance.Name] = name
			}
			for listen, active := range instance.TcpListens {
				owner := addressOwner{application: name, alive: active}
				if listen.Port == "0" {
					own(ipOwners, listen.IP, owner)
				} else {
					own(listenOwners, net.JoinHostPort(listen.IP, listen.Port), owner)
				}
			}
			for _, container := range instance.Containers {
				d.byContainerId[container.Id] = name
			}
		}
		for _, service := range app.KubernetesServices {
			d.byService[service.Name+"."+service.Namespace] = name
			if service.ClusterIP != "" {
				d.byService[service.ClusterIP] = name
			}
		}
		if app.Settings != nil && app.Settings.Tracing != nil && app.Settings.Tracing.Service != "" {
			d.byTracing[app.Settings.Tracing.Service] = name
		}
	}
	for ip, owner := range ipOwners {
		d.byIp[ip] = owner.application
		if owner.traced {
			d.tracedClients = append(d.tracedClients, ip)
		}
	}
	sort.Strings(d.tracedClients)
	for listen, owner := range listenOwners {
		d.byListen[listen] = owner.application
	}
	for _, node := range w.Nodes {
		for _, netInterface := range node.NetInterfaces {
			for _, address := range netInterface.Addresses {
				d.nodeIps[address] = true
			}
		}
	}
	return d
}

// TracedClients returns the IPs of the pods whose requests a node agent records
// at the client: the pods on the nodes that run one.
func (d *ApplicationDirectory) TracedClients() []string {
	return d.tracedClients
}

// SpanApplications returns the client and the server of a span, each an
// application name, or what the span says of that side when no application
// matches it: its service name, or the address of its peer. spanKind is the
// OTLP kind (SPAN_KIND_SERVER, ...). The directory may be nil, before the
// first world is loaded.
func (d *ApplicationDirectory) SpanApplications(serviceName, spanKind string, resource, span map[string]string) (client, server string) {
	own := d.ownApplication(serviceName, resource)
	namespace := resource["k8s.namespace.name"]
	switch spanKind {
	case "SPAN_KIND_SERVER", "SPAN_KIND_CONSUMER":
		return d.peerApplication(clientAddress(span), namespace), own
	case "SPAN_KIND_CLIENT", "SPAN_KIND_PRODUCER":
		return own, d.peerApplication(serverAddress(span), namespace)
	}
	return "", own
}

// ownApplication is the application of the service that made a span: the
// container of a node agent span, the pod an OpenTelemetry SDK reports (in
// Kubernetes host.name is the pod name), the tracing service of the
// application settings, or the application whose name the service has.
func (d *ApplicationDirectory) ownApplication(serviceName string, resource map[string]string) string {
	if d == nil {
		return unknownApplicationName(serviceName, resource)
	}
	if name, ok := d.byContainerId[resource["container.id"]]; ok {
		return name
	}
	if namespace := resource["k8s.namespace.name"]; namespace != "" {
		for _, pod := range []string{resource["k8s.pod.name"], resource["host.name"]} {
			if name, ok := d.byPod[namespace+"/"+pod]; pod != "" && ok {
				return name
			}
		}
	}
	if name, ok := d.byTracing[serviceName]; ok {
		return name
	}
	if name := d.guessApplication(serviceName); name != "" {
		return name
	}
	return unknownApplicationName(serviceName, resource)
}

// unknownApplicationName names, in the format of applicationName, the
// application of a span the world does not know yet, a pod started a moment
// ago, from what the span says: the node agent's service name is
// /k8s/<namespace>/<name> (/k8s-cronjob/ for a cron job), or its container id
// /k8s/<namespace>/<pod>/<container> for a pod with no owner, which the world
// names after the pod; an SDK's is the name in the namespace of its resource.
func unknownApplicationName(serviceName string, resource map[string]string) string {
	for _, prefix := range []string{"/k8s/", "/k8s-cronjob/"} {
		name, ok := strings.CutPrefix(serviceName, prefix)
		if !ok {
			continue
		}
		switch parts := strings.Split(name, "/"); {
		case len(parts) == 2:
			return name
		case len(parts) == 3 && prefix == "/k8s/":
			return parts[0] + "/" + parts[1]
		}
	}
	if namespace := resource["k8s.namespace.name"]; namespace != "" && serviceName != "" && !strings.HasPrefix(serviceName, "/") {
		return namespace + "/" + serviceName
	}
	return serviceName
}

// guessApplication finds the application an OpenTelemetry service belongs to
// by its name, as the application pages do (model.GuessService), once per
// service name.
func (d *ApplicationDirectory) guessApplication(serviceName string) string {
	if serviceName == "" || strings.HasPrefix(serviceName, "/") {
		return ""
	}
	d.guessedLock.Lock()
	defer d.guessedLock.Unlock()
	if name, ok := d.guessed[serviceName]; ok {
		return name
	}
	name := ""
	for _, app := range d.world.Applications {
		if model.GuessService([]string{serviceName}, d.world, app) == serviceName {
			name = applicationName(app.Id)
			break
		}
	}
	d.guessed[serviceName] = name
	return name
}

// peerApplication is the application at the address of the other side of a
// span: one that listens on it, the pod with its IP, or the Kubernetes service
// with its ClusterIP or name (a short name is in the namespace of the span).
func (d *ApplicationDirectory) peerApplication(address, namespace string) string {
	if d == nil || address == "" {
		return address
	}
	if name, ok := d.byListen[address]; ok {
		return name
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	if name, ok := d.byIp[host]; ok {
		return name
	}
	if name, ok := d.byService[host]; ok {
		return name
	}
	if net.ParseIP(host) == nil {
		parts := strings.Split(host, ".")
		serviceName := host + "." + namespace
		if len(parts) > 1 {
			serviceName = parts[0] + "." + parts[1]
		}
		if name, ok := d.byService[serviceName]; ok {
			return name
		}
	}
	return address
}

// serverAddress is the address a client span called, host:port when the
// port is known.
func serverAddress(span map[string]string) string {
	for _, keys := range [][2]string{{"server.address", "server.port"}, {"net.peer.name", "net.peer.port"}} {
		if host := span[keys[0]]; host != "" {
			if port := span[keys[1]]; port != "" {
				return net.JoinHostPort(host, port)
			}
			return host
		}
	}
	for _, key := range []string{"http.url", "url.full"} {
		if parsed, err := url.Parse(span[key]); err == nil && parsed.Host != "" {
			return parsed.Host
		}
	}
	return ""
}

// clientAddress is the IP the request of a server span came from; its port
// is of no use, a client picks a new one for each connection.
func clientAddress(span map[string]string) string {
	for _, key := range []string{"client.address", "net.sock.peer.addr", "net.peer.ip", "http.client_ip"} {
		if address := span[key]; address != "" {
			return address
		}
	}
	return ""
}
