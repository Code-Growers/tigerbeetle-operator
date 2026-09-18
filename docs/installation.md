# Installing the operator

The operator is distributed as a Helm chart, published as an OCI artifact next to its images.

> **Status:** alpha. The API is `tigerbeetle.codegrowers.com/v1alpha1` and may change between
> releases without a migration path. Do not run it for data you cannot afford to lose yet.

## Requirements

- Kubernetes 1.30 or newer. The CRD relies on CEL validation rules, including transition rules that
  compare a field with its previous value, to reject unsafe changes at admission.
- Helm 3.14 or newer (for `--reset-then-reuse-values`, see [Upgrading](#upgrading-the-operator)).
- Nodes and storage suitable for TigerBeetle: see [deploying.md](deploying.md).

## Install

```sh
helm install tigerbeetle-operator oci://ghcr.io/code-growers/charts/tigerbeetle-operator \
  --version <version> \
  --namespace tigerbeetle-operator-system --create-namespace
```

This installs the `TigerBeetleCluster` CRD, the operator Deployment, its RBAC, and a Service for the
operator's own metrics. The operator watches every namespace. The chart's defaults point at the
images of the same release, so there is nothing to set for a basic install.

Check that it runs:

```sh
kubectl -n tigerbeetle-operator-system rollout status deployment/tigerbeetle-operator
```

Commonly changed values (the full list is in [Helm chart values](chart-values.md)):

| Value | Default | Why you would change it |
|---|---|---|
| `resources` | 10m / 64Mi, limit 500m / 128Mi | The operator itself is small; this is not TigerBeetle's sizing |
| `logLevel`, `logFormat` | `info`, `json` | Debugging, or a text format for humans |
| `metrics.serviceMonitor.enabled` | `false` | You run the Prometheus Operator |
| `crds.install` | `true` | Something else (e.g. a GitOps tool) manages the CRD |
| `imagePullSecrets`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName` | empty | Cluster policy |

TigerBeetle replicas are sized in each `TigerBeetleCluster`, not in the chart.

## Upgrading the operator

```sh
helm upgrade tigerbeetle-operator oci://ghcr.io/code-growers/charts/tigerbeetle-operator \
  --version <new version> --namespace tigerbeetle-operator-system --reset-then-reuse-values
```

- The CRD is a template in this chart, so `helm upgrade` updates it together with the operator.
- Use `--reset-then-reuse-values`, not `--reuse-values`. `--reuse-values` keeps only the values of
  the previous release and drops defaults the new chart added, which can fail the render with
  errors such as `nil pointer evaluating interface {}.enabled`.
- Upgrading the operator can change the pod template it generates, which rolls the TigerBeetle
  replicas one at a time. Read the release notes first.
- Releases that installed the CRD from the chart's `crds/` directory (chart versions before the
  CRD became a template) need a one-off adoption before the first upgrade, since Helm only manages
  resources it knows about:

  ```sh
  kubectl label crd tigerbeetleclusters.tigerbeetle.codegrowers.com app.kubernetes.io/managed-by=Helm
  kubectl annotate crd tigerbeetleclusters.tigerbeetle.codegrowers.com \
    meta.helm.sh/release-name=tigerbeetle-operator \
    meta.helm.sh/release-namespace=tigerbeetle-operator-system
  ```

## Uninstalling

```sh
helm uninstall tigerbeetle-operator --namespace tigerbeetle-operator-system
```

The CRD carries `helm.sh/resource-policy: keep`, so uninstalling the chart leaves the CRD, every
`TigerBeetleCluster`, and their replicas in place. The replicas keep serving clients, but nothing
reconciles them until the operator is installed again.

To remove TigerBeetle entirely, delete the `TigerBeetleCluster` resources, then the data PVCs (the
operator never deletes them: see [operations.md](operations.md#restoring-a-tigerbeetlecluster)),
then the CRD:

```sh
kubectl delete crd tigerbeetleclusters.tigerbeetle.codegrowers.com
```

## GitOps

The chart installs cleanly from Argo CD or Flux. `TigerBeetleCluster` exposes kstatus conditions,
so Flux health checks work as they are; Argo CD needs the
[custom health check](argocd-health-check.md).
