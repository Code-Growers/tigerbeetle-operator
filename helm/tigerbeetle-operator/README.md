# tigerbeetle-operator

A Helm chart for the TigerBeetle Kubernetes operator

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.0](https://img.shields.io/badge/AppVersion-0.1.0-informational?style=flat-square)

## Install

```sh
helm install tigerbeetle-operator oci://ghcr.io/code-growers/charts/tigerbeetle-operator \
  --namespace tigerbeetle-operator-system --create-namespace
```

The chart installs the `TigerBeetleCluster` CRD as a template, so `helm upgrade` updates it. It
carries `helm.sh/resource-policy: keep`, so uninstalling the release leaves the CRD and every
TigerBeetleCluster in place. Set `crds.install=false` when something else manages the CRD.

Upgrading from a release that installed the CRD from the chart's `crds/` directory needs a one-off
adoption, since Helm only manages resources it knows about:

```sh
kubectl label crd tigerbeetleclusters.tigerbeetle.codegrowers.com app.kubernetes.io/managed-by=Helm
kubectl annotate crd tigerbeetleclusters.tigerbeetle.codegrowers.com \
  meta.helm.sh/release-name=tigerbeetle-operator \
  meta.helm.sh/release-namespace=tigerbeetle-operator-system
```

See [docs/operations.md](../../docs/operations.md) for running clusters once the operator is up.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity for the operator pod. |
| crds.install | bool | `true` | Install the TigerBeetleCluster CRD. They are templates, so `helm upgrade` updates them. |
| crds.keep | bool | `true` | Keep the CRD (and therefore every TigerBeetleCluster) when the release is uninstalled. |
| extraArgs | list | `[]` | Extra command line arguments for the manager. |
| fullnameOverride | string | `""` | Override the fully qualified release name. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/code-growers/tigerbeetle-operator"` | Operator image repository. |
| image.tag | string | `""` | Image tag. Defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets for both images. |
| initImage.repository | string | `"ghcr.io/code-growers/tigerbeetle-operator-init"` | Init helper image repository. |
| initImage.tag | string | `""` | Init helper image tag. Defaults to the chart appVersion. |
| leaderElection | bool | `true` | Run with leader election, so several operator replicas stay safe. |
| logFormat | string | `"json"` | Log format: json or text. |
| logLevel | string | `"info"` | Minimum log level: debug, info, warn or error. |
| metrics.enabled | bool | `true` | Serve controller-runtime metrics and create a Service for them. |
| metrics.port | int | `8443` | Port the metrics endpoint binds to. |
| metrics.secure | bool | `true` | Require authentication and authorization for metrics (`--metrics-secure`). The chart creates a metrics-reader ClusterRole for scrapers. |
| metrics.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor. Needs its CRD in the cluster. |
| metrics.serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| metrics.serviceMonitor.labels | object | `{}` | Extra labels, e.g. the release label your Prometheus selects on. |
| nameOverride | string | `""` | Override the chart name. |
| nodeSelector | object | `{}` | Node selector for the operator pod. |
| podAnnotations | object | `{}` | Annotations for the operator pod. |
| podLabels | object | `{}` | Labels for the operator pod. |
| podSecurityContext | object | `{"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod security context. |
| priorityClassName | string | `""` | Priority class for the operator pod. |
| replicaCount | int | `1` | Replicas of the operator itself. Leader election makes one of them active. |
| resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | Compute resources for the operator. TigerBeetle replicas are sized in the TigerBeetleCluster resource, not here. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true}` | Container security context. |
| serviceAccount.annotations | object | `{}` | Annotations for the ServiceAccount. |
| serviceAccount.automount | bool | `true` | Mount the ServiceAccount token in the pod. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount and the operator's RBAC. |
| serviceAccount.name | string | `""` | ServiceAccount name. Generated when empty. |
| tolerations | list | `[]` | Tolerations for the operator pod. |
