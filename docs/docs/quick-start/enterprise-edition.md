---
sidebar_position: 2
slug: /quick-start/enterprise
---

# Enterprise Edition

This guide provides a quick overview of launching Coroot Enterprise Edition with default options. For more details and customization options check out the Installation section.

:::info
Coroot Enterprise Edition is a paid subscription (from $1 per CPU core/month) that offers extra features and priority support. 
To install the Enterprise Edition, you'll need a license. [Start](https://coroot.com/account) your free trial today.
:::


Add the Coroot helm chart repo:

```bash
helm repo add coroot https://coroot.github.io/helm-charts
helm repo update coroot
```

Next, install the Coroot Operator:

```bash
helm install -n coroot --create-namespace coroot-operator coroot/coroot-operator
```

Install the Coroot Enterprise Edition. This chart creates a minimal [Coroot Custom Resource](/installation/k8s-operator):

```bash
helm install -n coroot coroot coroot/coroot-ee \
  --set "licenseKey=COROOT-LICENSE-KEY-HERE,clickhouse.shards=2,clickhouse.replicas=2"
```

Forward the Coroot port to your machine:

```bash
kubectl port-forward -n coroot service/coroot-coroot 8080:8080
```

Then, you can access Coroot at http://localhost:8080
