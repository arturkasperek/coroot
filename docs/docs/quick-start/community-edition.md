---
sidebar_position: 1
slug: /
---

# Community Edition

This guide provides a quick overview of launching Coroot Community Edition with default options. For more details and customization options check out the Installation section.

Add the Coroot helm chart repo:

```bash
helm repo add coroot https://coroot.github.io/helm-charts
helm repo update coroot
```

Next, install the Coroot Operator:

```bash
helm install -n coroot --create-namespace coroot-operator coroot/coroot-operator
```

Install the Coroot Community Edition. This chart creates a minimal [Coroot Custom Resource](/installation/k8s-operator):

```bash
helm install -n coroot coroot coroot/coroot-ce \
  --set "clickhouse.shards=2,clickhouse.replicas=2"
```

Forward the Coroot port to your machine:

```bash
kubectl port-forward -n coroot service/coroot-coroot 8080:8080
```

Then, you can access Coroot at http://localhost:8080
