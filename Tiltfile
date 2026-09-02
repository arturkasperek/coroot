# Coroot inner loop: build *:dev images, deploy into kind, live-update
# Go / Vue sources. Cluster bootstrap (kind + inotify) stays in `make dev`.
# Images are kind-loaded onto the current kind cluster (including SSH Docker).

allow_k8s_contexts('kind-coroot-dev')
update_settings(max_parallel_updates=4)

docker_build(
    'coroot-backend',
    '.',
    dockerfile='docker/coroot/Dockerfile.dev',
    ignore=['front/node_modules', 'docs', 'data-dev', 'static'],
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

k8s_yaml([
    'deploy/kind/prometheus.yaml',
    'deploy/kind/clickhouse.yaml',
    'deploy/kind/coroot.yaml',
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
