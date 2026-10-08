# Simplifying metric labels for a Kubernetes-only Coroot (design note)

Status: idea, not scheduled. Nothing in this note is implemented.

## Why this note exists

Coroot (server and node-agent) is built to run on Kubernetes, plain Docker, Docker Swarm, Nomad
and bare hosts with systemd. That generality is paid for on every sample we store:
every series carries a set of labels that exist so those environments can be told apart,
and several code paths branch on the environment.

If we decide that **Coroot only runs on Kubernetes**, most of that can be removed. This note
records what the labels are today, what each one is for, what a Kubernetes-only version could
look like, and what has to change together. It is a decision aid, not a plan.

Why now: the metrics storage moved to a ClickHouse TimeSeries table (see
`plans/2026-10-05-clickhouse-promql-metrics.md`): the labels of a series are stored once, in the
engine's tags table (`timeSeriesTags`), and the samples in its data table. A shorter, more regular label set makes
the tags table smaller and the lookups by label cheaper, and it is the cheapest moment to
agree on the label contract.

## What a series looks like today

Example from a real dev cluster (agent -> Coroot, remote write):

```
app_id            /k8s/coroot-dev/tabix
application_type  nginx
container_id      /k8s/coroot-dev/tabix-6b64cff8f6-pchnr/tabix
instance          17693b1e56b1a09aae4b7ffc404445f7
job               coroot-node-agent
machine_id        2842f6d957b04cb2820293e4469b7e4a
system_uuid       38e8c980-7a6d-11f1-98b6-92f7207b6400
```

`MetricHash` is `LabelsToSignature` over all of them plus `__name__`, so every label below
multiplies the number of series whenever its value changes.

### Where each label comes from

| label | set by | how it is built | used by the server for |
| --- | --- | --- | --- |
| `container_id` | node-agent, `containers/registry.go` `calcId` | path string that depends on the runtime: `/k8s/<ns>/<pod>/<container>`, `/k8s-cronjob/<ns>/<job>/<container>`, `/swarm/<ns>/<service>/<n>`, `/nomad/<ns>/<job>/<group>/<alloc>/<task>`, `/docker/<name>`, `/system.slice/<unit>.service` (systemd), Talos runtime | `constructor/containers.go` `getInstanceAndContainer` parses the string back into namespace, pod and container and creates the Instance/Container model |
| `app_id` | node-agent, `common.ContainerIdToOtelServiceName` | for `/k8s/...` pods owned by a Deployment/DaemonSet/StatefulSet/CronJob it is the controller path `/k8s/<ns>/<controller>` (pod suffix stripped by regexes); for everything else it is empty | `getInstanceByAppId` (only `k8s` and `k8s-cronjob` accepted, anything else logs "unknown app") |
| `machine_id` | node-agent, `host.MachineID()` | `/etc/machine-id`, then `/var/lib/dbus/machine-id`, then `product_uuid` | node identity (`model.NodeId`) |
| `system_uuid` | node-agent, `host.SystemUUID()` | `/sys/devices/virtual/dmi/id/product_uuid` | node identity (`model.NodeId`) |
| `instance` | node-agent, `prom/remote_writer.go` | `machine_id`, or `md5(machine_id + system_uuid)` when the uuid differs | not the container; Prometheus-style target label, also used for `up`, restarts and OOM queries |
| `job` | node-agent, `prom/remote_writer.go` | constant `coroot-node-agent` | joins with `up{job=...}` |
| `application_type` | node-agent, `apptype` | detected from the process command line | `container_application_type` metric |

Two facts worth knowing:

* `instance` is **fully derived from `machine_id` and `system_uuid`**. It carries no extra
  information, it only exists so the series looks like a scraped Prometheus target.
  `job` is a constant. Together they add two labels to every series for no information.
* `container_id` and `app_id` are **strings that encode structure**, and the server parses
  them back (`strings.Split(containerId, "/")`, `len(parts) == 5`). That is the part that
  makes the contract fragile: a label value is acting as a record.

The same string convention leaks into other storage:

* `otel_logs.ServiceName` for agent logs is the container id / app id, and
  the generated columns `Namespace` and `Application` are derived from it in SQL
  (`if(startsWith(ServiceName, '/k8s'), splitByChar('/', ServiceName)[3], ...)`).
  The "Source" facet distinguishes agent from OTel logs with `startsWith(ServiceName, '/')`.
* Trace spans from the agent use the same ids as service names.

## What a Kubernetes-only contract could look like

Principle: **one label per fact, with the names Kubernetes already uses, no encoded paths.**

Proposed labels on container-level series:

| new label | replaces | notes |
| --- | --- | --- |
| `namespace` | the 2nd path segment of `container_id` / `app_id` | already a conventional name (`possibleNamespaceLabels` in `constructor/queries.go` accepts it) |
| `pod` | the 3rd segment of `container_id` | |
| `container` | the 4th segment | |
| `workload` + `workload_kind` (or `owner_kind` / `owner_name`) | `app_id` | kind is `Deployment`, `StatefulSet`, `DaemonSet`, `CronJob`, `Job`, ...; no more regexes on pod names |
| `node` | `machine_id` + `system_uuid` + `instance` | the Kubernetes node name (or node UID), one label instead of three |
| (dropped) | `job`, `instance` | Coroot owns both ends; the server can stop joining on `up{job=...}` by using a dedicated agent-health metric |

`application_type` stays; it is a property of the process, not of the environment.

Effects on storage:

* Series carry five short labels instead of seven, two of which were long hex strings, and
  none of them is a path that has to be split in SQL or Go.
* The tags of every series shrink. Because the hash is over the label set, **every series is
  re-created once** after the change (a one-time churn), so the change should be made
  together with other breaking changes.
* The Namespace and Application generated columns in `otel_logs` / `otel_traces` become plain
  columns read from `k8s.namespace.name` and a workload attribute, instead of SQL that parses
  `ServiceName`. The `Source` facet would need its own marker (an attribute or a column)
  instead of `startsWith(ServiceName, '/')`.

## What can be deleted on the server

If the contract above is adopted, in `coroot`:

* `constructor/containers.go`: the `swarm`, `nomad`, `docker`, systemd and Talos branches of
  `getInstanceAndContainer`; `ApplicationKindDockerSwarmService`, `ApplicationKindNomadJobGroup`
  and the "unknown app" handling in `getInstanceByAppId`. The path parsing disappears
  entirely: the instance is looked up by `(namespace, pod, node)` straight from labels.
* `model`: the non-Kubernetes `ApplicationKind` values and the `"_"` placeholder namespace for
  apps that have none.
* Custom-application naming for non-k8s instances (`project.GetCustomApplicationName`) if it is
  only used there (to be checked).
* Fargate, ECS and other cloud-specific branches (`constructor/fargate.go`) only if they are
  also out of scope; they are a separate decision.
* The mixed `possibleNamespaceLabels` / `possiblePodLabels` / `possibleDBInstanceLabels`
  alias lists in `constructor/queries.go` could become fixed names for metrics that Coroot
  itself produces. They still serve third-party exporters (kube-state-metrics, DB exporters),
  so they only shrink, they do not disappear.

In `coroot-node-agent`:

* `calcId` loses every branch but Kubernetes; `containers/dockerd.go`, `systemd.go`,
  `journald.go` (unit-based ids) and the Swarm and Nomad metadata handling go away if the agent
  is also Kubernetes-only. The cgroup parsing for plain Docker and systemd is still needed to
  find pods' processes in some distributions, so removal needs a check per file.
* `ContainerIdToOtelServiceName` regexes (pod name -> controller name) are replaced by reading
  owner references from the container runtime metadata (labels `io.kubernetes.pod.*`) or from
  the Kubernetes API. Regexes on pod names are heuristic and wrong for custom controllers.
* The Windows agent (`cmd/coroot-windows-agent`) is out of scope for a Kubernetes-only
  product and could be removed.

## Open questions

1. **Who knows the owner of a pod?** The agent today only sees container runtime labels
   (`io.kubernetes.pod.name`, namespace, container name). `workload` / `workload_kind` need
   owner references. Options: (a) keep the regexes as they are, (b) agent asks the
   Kubernetes API (needs RBAC), (c) the server enriches from kube-state-metrics, which it
   already requires (`IntegrationStatus.KubeStateMetrics.Required`). (c) is the least
   invasive, and then `app_id` is not needed on the agent side at all.
2. **Node identity.** Kubernetes node name is stable and human-readable but can be reused after
   a node is replaced; `machine_id` is unique per OS installation. Keeping one opaque id
   (`machine_id`) plus the node name resolved on the server may be better than replacing
   both.
3. **Agent logs.** Agent logs and traces use the container id as the service name. The new
   contract needs `k8s.namespace.name`, `k8s.pod.name`, `k8s.container.name` resource
   attributes on them and a rewrite of the generated columns and the rollup keys
   (`otel_logs_rollup` is keyed by `ServiceName, Namespace, Application`).
4. **Non-Kubernetes users.** This is a product decision, not a technical one: bare-metal,
   Swarm and Nomad users would lose support. A middle path is to keep the agent
   able to run elsewhere but have it map such environments onto the Kubernetes-shaped labels
   (namespace `_`, pod = unit name), which keeps one contract and drops the server-side
   branches.
5. **Migration.** With no backwards-compatibility requirement (current stance for the new
   ClickHouse tables) the change is: new labels, new series, old series expire by TTL after
   30 days. Dashboards and alert rules that select by `container_id` or `app_id` have to be
   found and rewritten (`grep` for `container_id` and `app_id` in `constructor/queries.go`,
   `api/mcp.go`, recording rules, and the front end).

## Suggested order, if it is ever done

1. Agree the label contract (table above) and the answer to question 1.
2. Server first: accept both old and new labels in `constructor/containers.go` behind one
   function that returns `(namespace, pod, container, workload)`, so the parsing lives in one
   place.
3. Agent: emit the new labels (and keep the old ones for one release).
4. Switch logs/traces ids and the generated columns.
5. Delete the old labels, the non-Kubernetes branches and the regexes.

Steps 2 and 3 can ship independently, which keeps the change reviewable.
