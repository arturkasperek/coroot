package collector

import (
	"testing"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

// A world like the demo of the dev cluster: express-demo and flask-demo are
// instrumented with OpenTelemetry and called by demo-traffic, express-demo
// calls flask-demo through its Kubernetes service, and stripe.com is an
// external service.
func testApplicationDirectory() *ApplicationDirectory {
	w := model.NewWorld(0, 0, 0, 0)
	addPod := func(name, pod, ip, containerName string) *model.Application {
		app := w.GetOrCreateApplication(model.NewApplicationId("project", "coroot-dev", model.ApplicationKindDeployment, name), false)
		instance := app.GetOrCreateInstance(pod, nil)
		instance.Pod = &model.Pod{IP: ip}
		instance.GetOrCreateContainer("/k8s/coroot-dev/"+pod+"/"+containerName, containerName)
		return app
	}
	addPod("demo-traffic", "demo-traffic-75b97975f8-vmwbw", "10.42.0.150", "traffic")
	express := addPod("express-demo", "express-demo-6c678767cd-5b9w7", "10.42.0.140", "express")
	express.Settings = &model.ApplicationSettings{Tracing: &model.ApplicationSettingsTracing{Service: "express-api"}}
	flask := addPod("flask-demo", "flask-demo-796c985c74-z2qdd", "10.42.0.139", "flask")
	flask.KubernetesServices = append(flask.KubernetesServices, &model.Service{Name: "flask-demo", Namespace: "coroot-dev", ClusterIP: "10.43.12.7"})
	addPod("nextjs-demo", "nextjs-demo-7869b4cc86-7cmnx", "10.42.0.141", "nextjs")

	stripe := w.GetOrCreateApplication(model.NewApplicationId("project", "external", model.ApplicationKindExternalService, "api.stripe.com:443"), false)
	stripe.GetOrCreateInstance("54.187.174.169:443", nil).TcpListens[model.Listen{IP: "54.187.174.169", Port: "443"}] = true

	// a process of the node (no namespace) and a pod with no owner
	w.GetOrCreateApplication(model.NewApplicationId("project", "_", model.ApplicationKindUnknown, "k3s"), false).
		GetOrCreateInstance("k3s@server", nil).TcpListens[model.Listen{IP: "10.42.0.1", Port: "6443"}] = true
	addPodOf(w, model.NewApplicationId("project", "coroot-dev", model.ApplicationKindPod, "debug-pod"), "debug-pod", "10.42.0.160", "shell")

	return NewApplicationDirectory(w)
}

func addPodOf(w *model.World, id model.ApplicationId, pod, ip, containerName string) {
	instance := w.GetOrCreateApplication(id, false).GetOrCreateInstance(pod, nil)
	instance.Pod = &model.Pod{IP: ip}
	instance.GetOrCreateContainer("/k8s/"+id.Namespace+"/"+pod+"/"+containerName, containerName)
}

// Values are namespace/name for an application of the cluster, and the name
// alone for one with no namespace: a process of a node, an external service.
func TestSpanApplicationsNames(t *testing.T) {
	directory := testApplicationDirectory()
	resource := map[string]string{"container.id": "/k8s/coroot-dev/debug-pod/shell"}
	client, server := directory.SpanApplications("/k8s/coroot-dev/debug-pod", "SPAN_KIND_CLIENT", resource,
		map[string]string{"server.address": "10.42.0.1", "server.port": "6443"})
	assert.Equal(t, "coroot-dev/debug-pod", client, "a pod with no owner is named after the pod")
	assert.Equal(t, "k3s", server)
}

const (
	demoTraffic = "coroot-dev/demo-traffic"
	expressDemo = "coroot-dev/express-demo"
	flaskDemo   = "coroot-dev/flask-demo"
	nextjsDemo  = "coroot-dev/nextjs-demo"
	stripe      = "api.stripe.com:443"
)

func TestSpanApplicationsOfAgentSpan(t *testing.T) {
	directory := testApplicationDirectory()
	resource := map[string]string{"container.id": "/k8s/coroot-dev/demo-traffic-75b97975f8-vmwbw/traffic", "k8s.namespace.name": "coroot-dev"}

	client, server := directory.SpanApplications("/k8s/coroot-dev/demo-traffic", "SPAN_KIND_CLIENT", resource,
		map[string]string{"server.address": "10.42.0.140", "server.port": "3000", "net.peer.name": "10.42.0.140", "net.peer.port": "3000"})
	assert.Equal(t, demoTraffic, client)
	assert.Equal(t, expressDemo, server, "the pod IP of the server")

	_, server = directory.SpanApplications("/k8s/coroot-dev/demo-traffic", "SPAN_KIND_CLIENT", resource,
		map[string]string{"net.peer.name": "54.187.174.169", "net.peer.port": "443"})
	assert.Equal(t, stripe, server, "an external service by its address")

	_, server = directory.SpanApplications("/k8s/coroot-dev/demo-traffic", "SPAN_KIND_CLIENT", resource,
		map[string]string{"net.peer.name": "10.99.0.1", "net.peer.port": "5432"})
	assert.Equal(t, "10.99.0.1:5432", server, "an address no application has")
}

func TestSpanApplicationsOfOtelServerSpan(t *testing.T) {
	directory := testApplicationDirectory()

	// in Kubernetes the SDK reports the pod name as host.name
	client, server := directory.SpanApplications("nextjs-demo", "SPAN_KIND_SERVER",
		map[string]string{"service.name": "nextjs-demo", "k8s.namespace.name": "coroot-dev", "host.name": "nextjs-demo-7869b4cc86-7cmnx"},
		map[string]string{"net.peer.ip": "10.42.0.150", "net.peer.port": "58932", "http.target": "/products/42"})
	assert.Equal(t, demoTraffic, client, "the client by its pod IP")
	assert.Equal(t, nextjsDemo, server)

	_, server = directory.SpanApplications("flask-demo", "SPAN_KIND_SERVER",
		map[string]string{"service.name": "flask-demo", "k8s.namespace.name": "coroot-dev", "k8s.pod.name": "flask-demo-796c985c74-z2qdd"},
		map[string]string{"client.address": "10.42.0.140"})
	assert.Equal(t, flaskDemo, server, "the pod of k8s.pod.name")

	client, server = directory.SpanApplications("express-api", "SPAN_KIND_SERVER",
		map[string]string{"service.name": "express-api"}, map[string]string{"net.sock.peer.addr": "192.168.1.20"})
	assert.Equal(t, expressDemo, server, "the tracing service set in the application settings")
	assert.Equal(t, "192.168.1.20", client, "a client outside the cluster")

	_, server = directory.SpanApplications("flask-demo", "SPAN_KIND_SERVER", map[string]string{"service.name": "flask-demo"}, nil)
	assert.Equal(t, flaskDemo, server, "the application whose name the service has")

	client, server = directory.SpanApplications("unknown-service", "SPAN_KIND_SERVER", map[string]string{"service.name": "unknown-service"}, nil)
	assert.Equal(t, "unknown-service", server, "the service name when no application has it and no namespace")
	assert.Equal(t, "", client, "no client address")
}

func TestSpanApplicationsOfOtelClientSpan(t *testing.T) {
	directory := testApplicationDirectory()
	resource := map[string]string{"service.name": "express-api", "k8s.namespace.name": "coroot-dev"}

	for _, span := range []map[string]string{
		{"http.url": "http://flask-demo:3000/api/hello"},                                      // the service in the namespace of the client
		{"net.peer.name": "flask-demo.coroot-dev.svc.cluster.local", "net.peer.port": "3000"}, // its whole name
		{"server.address": "10.43.12.7", "server.port": "3000"},                               // its ClusterIP
	} {
		client, server := directory.SpanApplications("express-api", "SPAN_KIND_CLIENT", resource, span)
		assert.Equal(t, expressDemo, client)
		assert.Equal(t, flaskDemo, server, "%v", span)
	}
}

func TestSpanApplicationsOfInternalSpan(t *testing.T) {
	client, server := testApplicationDirectory().SpanApplications("flask-demo", "SPAN_KIND_INTERNAL", map[string]string{"service.name": "flask-demo"}, nil)
	assert.Equal(t, "", client)
	assert.Equal(t, flaskDemo, server)
}

// Before the first world is loaded the spans keep what they say themselves.
func TestSpanApplicationsWithoutDirectory(t *testing.T) {
	var directory *ApplicationDirectory
	client, server := directory.SpanApplications("/k8s/coroot-dev/demo-traffic", "SPAN_KIND_CLIENT", nil,
		map[string]string{"server.address": "10.42.0.140", "server.port": "3000"})
	assert.Equal(t, "coroot-dev/demo-traffic", client)
	assert.Equal(t, "10.42.0.140:3000", server)
}

// An application the world does not know yet (a pod started a moment ago) is
// named in the same format from what its span says.
func TestSpanApplicationsOfAnUnknownApplication(t *testing.T) {
	directory := testApplicationDirectory()
	for _, directory := range []*ApplicationDirectory{directory, nil} {
		client, _ := directory.SpanApplications("/k8s/coroot-dev/coroot", "SPAN_KIND_CLIENT",
			map[string]string{"container.id": "/k8s/coroot-dev/coroot-7f4ccb4bbf-vt9rs/coroot", "k8s.namespace.name": "coroot-dev"}, nil)
		assert.Equal(t, "coroot-dev/coroot", client, "the agent's service name")

		client, _ = directory.SpanApplications("/k8s-cronjob/default/backup", "SPAN_KIND_CLIENT", nil, nil)
		assert.Equal(t, "default/backup", client, "a cron job of the agent")

		client, _ = directory.SpanApplications("/k8s/coroot-dev/psql-burst/psql-burst", "SPAN_KIND_CLIENT", nil, nil)
		assert.Equal(t, "coroot-dev/psql-burst", client, "a pod with no owner: the agent names it by its container, the world by the pod")

		_, server := directory.SpanApplications("orders", "SPAN_KIND_SERVER", map[string]string{"service.name": "orders", "k8s.namespace.name": "shop"}, nil)
		assert.Equal(t, "shop/orders", server, "an SDK service in its namespace")

		_, server = directory.SpanApplications("orders", "SPAN_KIND_SERVER", map[string]string{"service.name": "orders"}, nil)
		assert.Equal(t, "orders", server, "an SDK service with no namespace")

		client, _ = directory.SpanApplications("/system.slice/k3s.service", "SPAN_KIND_CLIENT", nil, nil)
		assert.Equal(t, "/system.slice/k3s.service", client, "a process the world does not know")
	}
}

// The clients whose requests a node agent records: the pods on the nodes that
// run one. Their servers skip the bodies of their requests.
func TestTracedClients(t *testing.T) {
	w := model.NewWorld(0, 0, 0, 0)
	withAgent := &model.Node{}
	withAgent.AgentVersion.Update(timeseries.NewWithData(0, 15, []float32{1}), "1.2.3")
	withoutAgent := &model.Node{}
	pod := func(name, ip string, node *model.Node) {
		app := w.GetOrCreateApplication(model.NewApplicationId("project", "shop", model.ApplicationKindDeployment, name), false)
		instance := app.GetOrCreateInstance(name+"-0", nil)
		instance.Node = node
		instance.Pod = &model.Pod{IP: ip}
	}
	pod("checkout", "10.42.0.5", withAgent)
	pod("orders", "10.42.0.6", withAgent)
	pod("fargate", "10.42.1.9", withoutAgent)
	pod("pending", "", withAgent)
	pod("unscheduled", "10.42.0.7", nil)

	assert.Equal(t, []string{"10.42.0.5", "10.42.0.6"}, NewApplicationDirectory(w).TracedClients())
}

// An IP of a deleted pod is given to a new one within the window of the world:
// the IP is the new pod's, whichever the directory meets first, and so is its
// place on the traced clients.
func TestDirectoryGivesAReusedIpToTheLatestPod(t *testing.T) {
	withAgent := &model.Node{}
	withAgent.AgentVersion.Update(timeseries.NewWithData(0, 15, []float32{1}), "1.2.3")
	for attempt := 0; attempt < 20; attempt++ {
		w := model.NewWorld(0, 0, 0, 0)
		pod := func(name, phase string, lifeSpan []float32, node *model.Node) {
			instance := w.GetOrCreateApplication(model.NewApplicationId("project", "shop", model.ApplicationKindDeployment, name), false).
				GetOrCreateInstance(name+"-0", nil)
			instance.Node = node
			instance.Pod = &model.Pod{IP: "10.42.0.9", Phase: phase, LifeSpan: timeseries.NewWithData(0, 15, lifeSpan)}
		}
		nan := timeseries.NaN
		pod("orders", "", []float32{1, 1, nan, nan}, withAgent)    // deleted
		pod("payments", "Running", []float32{nan, nan, 1, 1}, nil) // started after, on a node without an agent
		directory := NewApplicationDirectory(w)

		_, server := directory.SpanApplications("/k8s/shop/checkout", "SPAN_KIND_CLIENT", nil, map[string]string{"server.address": "10.42.0.9", "server.port": "8080"})
		assert.Equal(t, "shop/payments", server)
		assert.Empty(t, directory.TracedClients(), "the IP is no longer of a pod with an agent")
	}
}
