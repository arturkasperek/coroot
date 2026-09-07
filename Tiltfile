# Coroot inner loop: build *:dev images, deploy into KUBERNETES_CONTEXT_NAME
# from .env, live-update Go / Vue sources. Cluster lifecycle is outside Tilt.

def _kubernetes_context():
    ctx = os.getenv('KUBERNETES_CONTEXT_NAME', '').strip()
    if ctx:
        return ctx
    if os.path.exists('.env'):
        for line in str(read_file('.env')).splitlines():
            line = line.strip()
            if not line or line.startswith('#') or '=' not in line:
                continue
            k, _, v = line.partition('=')
            if k.strip() == 'KUBERNETES_CONTEXT_NAME':
                return v.strip().strip("'\"")
    fail('set KUBERNETES_CONTEXT_NAME in .env (see make dev)')

def cluster_docker_build(ref, context, dockerfile, ignore=None, deps=None, live_update=None):
    ignore = ignore or []
    live_update = live_update or []
    deps = deps or [context]
    cmd = (
        'docker build -f %s -t "$EXPECTED_REF" %s && ' +
        'bash scripts/dev/cluster-load-image.sh "$EXPECTED_REF"'
    ) % (dockerfile, context)
    custom_build(
        ref,
        cmd,
        deps=deps,
        ignore=ignore,
        live_update=live_update,
        disable_push=True,
    )

_k8s_context = _kubernetes_context()
allow_k8s_contexts(_k8s_context)
print('Tilt: kubernetes context', _k8s_context)
update_settings(max_parallel_updates=4)

cluster_docker_build(
    'coroot-backend',
    '.',
    'docker/coroot/Dockerfile.dev',
    ignore=['front/node_modules', 'docs', 'data-dev', 'static', 'deploy/kind/demo'],
    live_update=[
        sync('.', '/app'),
    ],
)

cluster_docker_build(
    'coroot-frontend',
    '.',
    'docker/frontend/Dockerfile.dev',
    deps=['front'],
    ignore=['front/node_modules'],
    live_update=[
        sync('front', '/app/front'),
    ],
)

# Optional sibling checkout: ../coroot-node-agent (see scripts/dev/node-agent-local-dir.sh).
# Tilt rebuilds that image on Go changes (eBPF is pre-baked into the binary; no live_update).
_node_agent_dir = os.path.abspath('../coroot-node-agent')
_use_local_node_agent = (
    os.path.exists(os.path.join(_node_agent_dir, 'go.mod'))
    and os.path.exists(os.path.join(_node_agent_dir, 'Dockerfile'))
)
if _use_local_node_agent:
    print('Tilt: building node-agent from', _node_agent_dir)
    cluster_docker_build(
        'ghcr.io/coroot/coroot-node-agent',
        _node_agent_dir,
        'docker/node-agent/Dockerfile.dev',
        ignore=['.git', 'windows'],
    )
else:
    print('Tilt: node-agent image ghcr.io/coroot/coroot-node-agent:latest (no ../coroot-node-agent)')

# Demo apps: production images, built once on tilt up (manual trigger, no live_update).
cluster_docker_build(
    'express-demo',
    'deploy/kind/demo/express',
    'deploy/kind/demo/express/Dockerfile',
)
cluster_docker_build(
    'nextjs-demo',
    'deploy/kind/demo/nextjs',
    'deploy/kind/demo/nextjs/Dockerfile',
)

load('ext://helm_resource', 'helm_resource', 'helm_repo')

helm_repo(
    'metrics-server',
    'https://kubernetes-sigs.github.io/metrics-server/',
    resource_name='metrics-server-helm-repo',
)
helm_resource(
    'metrics-server',
    'metrics-server/metrics-server',
    namespace='kube-system',
    flags=[
        '--version=3.14.0',
        '--values=deploy/kind/metrics-server-values.yaml',
    ],
    deps=['deploy/kind/metrics-server-values.yaml'],
    resource_deps=['metrics-server-helm-repo'],
)

k8s_yaml([
    'deploy/kind/clickhouse.yaml',
    'deploy/kind/postgres.yaml',
    'deploy/kind/tabix.yaml',
    'deploy/kind/coroot.yaml',
    'deploy/kind/agents.yaml',
    'deploy/kind/demo/apps.yaml',
])

k8s_resource(
    'clickhouse',
    port_forwards=['18123:8123'],
)
k8s_resource(
    'postgres',
    port_forwards=['15432:5432'],
)
k8s_resource(
    'tabix',
    port_forwards=['18081:80'],
    resource_deps=['clickhouse'],
)
k8s_resource(
    'coroot',
    port_forwards=['18080:8080'],
    resource_deps=['clickhouse', 'postgres'],
)
k8s_resource(
    'coroot-node-agent',
    resource_deps=['coroot'],
)
k8s_resource(
    'coroot-cluster-agent',
    resource_deps=['coroot'],
)

k8s_resource(
    'express-demo',
    port_forwards=['13001:3000'],
    resource_deps=['coroot'],
    trigger_mode=TRIGGER_MODE_MANUAL,
)
k8s_resource(
    'nextjs-demo',
    port_forwards=['13000:3000'],
    resource_deps=['coroot', 'express-demo'],
    trigger_mode=TRIGGER_MODE_MANUAL,
)
k8s_resource(
    'demo-traffic',
    resource_deps=['express-demo', 'nextjs-demo'],
    trigger_mode=TRIGGER_MODE_MANUAL,
)
