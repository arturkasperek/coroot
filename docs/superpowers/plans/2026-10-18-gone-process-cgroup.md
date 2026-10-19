# The container of a gone process, when it is not its parent's

Status: plan for later, nothing of it started. Repository: the node agent fork
`coroot-node-agent`. Follows `2026-10-16-ebpf-client-server-coverage.md`.

## Where we are

A process that lives a few milliseconds (one a server forks for a connection)
is gone before the agent reads its `/proc/<pid>/cgroup`. The agent then puts it
in the container of its parent: the eBPF side records the parent of each
process at `task_newtask` (`process_parents`), and `goneProcessContainer`
(`containers/registry.go`) resolves the parent's container. A fork inherits the
cgroup of its parent, so this is right for every plain fork.

## The gap

A process can start in another cgroup than its parent's:

- `clone3` with `CLONE_INTO_CGROUP` (Linux 5.7+, cgroup v2): the child starts
  in the cgroup given by a file descriptor. systemd uses it to start units;
  crun and Go programs (`SysProcAttr.UseCgroupFD`, Go 1.20+) can.
- A process moved right after fork, before the agent reads its `/proc`
  (writing `cgroup.procs`): runc and containerd move the processes of
  `kubectl exec` / `docker exec` into the container's cgroup this way.

If such a process is gone before the agent reads its `/proc`, it lands in the
container of its parent. The parent is the runtime (containerd-shim, runc,
systemd), on the host or in another container. Its spans, metrics and
connections are counted for the wrong application.

What is affected: a short-lived `kubectl exec` command (a probe of the exec
kind, a `psql -c` run by an operator, a cron run through exec), or a short-lived
service started by systemd. A long-running process is not affected: the agent
reads its `/proc` while it lives.

## The fix

Record the cgroup the process actually had, not only its parent:

1. eBPF: at `sched_process_exit` (the dying task is still in its cgroup),
   store `bpf_get_current_cgroup_id()` per pid in an LRU map
   `process_cgroups` (pid → cgroup v2 id). Store it at the first L7/TCP
   event of a process too, which covers a process whose exit the agent has
   not seen yet. Check first whether `task_newtask` already sees the final
   cgroup with `CLONE_INTO_CGROUP` (`trace_task_newtask` versus
   `cgroup_post_fork` in `copy_process`). If it does, recording it there is
   enough for that case, but not for a process moved after fork.
2. User space: a container knows its cgroup v2 id, which is the inode of its
   directory under `/sys/fs/cgroup` (kernfs id), taken when the container is
   created. The registry keeps `containersByCgroupInode`.
3. `goneProcessContainer`: first `earlyContainer` (cgroup read while the
   process was alive), then the recorded cgroup id → container, and the
   parent only as the last resort. The parent stays the rule on cgroup v1,
   where there is no cgroup id helper.
4. A gone process with no recorded cgroup and a parent that is a container
   runtime (containerd-shim, runc, conmon, systemd): no container rather than
   the runtime's. Its events are dropped and counted
   (`node_l7_dropped_unknown_container_total`), as before the parent rule.

## Tests

- Unit (containers): a gone pid whose recorded cgroup is another container's
  than its parent's resolves to that container; with no recorded cgroup it
  resolves to its parent's; a parent that is a runtime gives no container.
- Agent docker e2e: a Go service that starts, per connection, a short-lived
  child into another cgroup with `SysProcAttr{UseCgroupFD: true}`
  (`CLONE_INTO_CGROUP`). The test container is privileged and can create the
  cgroup. Its server spans must be those of the child's cgroup's container,
  not of the parent's.
- Dev: `kubectl exec` of a short `psql -c` into the postgres pod in a loop.
  Its client spans must be those of `coroot-dev/postgres`, not of the node.

## Open points

- Measure the cost of the extra map write at exit and at the first event.
  Process churn on busy nodes is high.
- The size of `process_cgroups` (as `process_parents`, 32768): enough for the
  `RecentlyExitedTTL` window (60 s) on a node that starts many processes?
- cgroup v1 hosts keep only the parent rule.
