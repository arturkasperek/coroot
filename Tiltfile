# Coroot inner loop: build *:dev images, deploy into kind, live-update
# Go / Vue sources. Cluster bootstrap (kind + inotify) stays in `make dev`.
# Images are kind-loaded onto the current kind cluster (including SSH Docker).

allow_k8s_contexts('kind-coroot-dev')
update_settings(max_parallel_updates=4)

docker_build(
    'coroot-backend',
    '.',
    dockerfile='docker/coroot/Dockerfile.dev',
    ignore=['front/node_modules', 'docs', 'data-dev', 'static', 'deploy/kind/demo'],
    live_update=[
        sync('.', '/app'),
    ],
)

docker_build(
    'coroot-frontend',
    '.',
    dockerfile='docker/frontend/Dockerfile.dev',
    only=['front'],
    ignore=['front/node_modules'],
    live_update=[
        sync('front', '/app/front'),
    ],
)

# Demo apps: production images, built once on tilt up (manual trigger, no live_update).
docker_build(
    'express-demo',
    'deploy/kind/demo/express',
    dockerfile='deploy/kind/demo/express/Dockerfile',
)
docker_build(
    'nextjs-demo',
    'deploy/kind/demo/nextjs',
    dockerfile='deploy/kind/demo/nextjs/Dockerfile',
)

k8s_yaml([
    'deploy/kind/prometheus.yaml',
    'deploy/kind/clickhouse.yaml',
    'deploy/kind/coroot.yaml',
    'deploy/kind/agents.yaml',
    'deploy/kind/demo/apps.yaml',
])

k8s_resource(
    'prometheus',
)
k8s_resource(
    'clickhouse',
)
k8s_resource(
    'coroot',
    port_forwards=['18080:8080'],
    resource_deps=['prometheus', 'clickhouse'],
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
