---
title: "Operating a TigerBeetleCluster"
---

How the operator reports problems, and what it expects from you when it needs a decision.

## Status at a glance

```sh
kubectl get tbc -o wide                    # phase, ready replicas, addresses
kubectl describe tbc <name>                # conditions, replica states, events
```

- `Ready`, `Reconciling` and `Stalled` conditions follow kstatus, so Flux, `kubectl wait` and the
  [Argo CD health check](argocd-health-check.md) understand them.
- `Stalled=True` means the operator cannot make progress without you. Its message says what to do.
- `status.replicaStatuses` shows each replica's state (`Pending`, `Recovering`, `AwaitingApproval`,
  `Failing`, `Running`) and, for the last three, a message.

Event and condition reasons are fixed strings, so alerts can match on them:

| Reason | Type | Meaning |
|---|---|---|
| `ReplicaFailing` | Warning; Ready reason; Stalled after 5 min | A replica pod crash-loops, cannot pull its image, or cannot be scheduled |
| `StuckReplicaRestarted` | Normal | A failing pod blocked a rollout and was deleted (see below) |
| `RecoveryApprovalRequired` | Warning; Stalled | A replica lost its data file and `recoveryPolicy` is `Manual` |
| `RecoveryApproved` | Normal | The operator passed your approval to the waiting pod |
| `RecoveryDelayed` | Warning | A replica has waited over 10 min to recover; the other replicas must be healthy |
| `ReplicaDataLost` | Warning; Stalled | A single-replica cluster lost its data file |
| `BootstrapConfirmationRequired` | Warning; Stalled | Data PVCs exist that this TigerBeetleCluster did not format |
| `ExistingDataAdopted` | Normal | The cluster started on existing PVCs (`adopt-existing-data`) |
| `RolloutStarted`, `RolloutCompleted`, `UpgradeCompleted` | Normal | Spec and image changes rolling through the replicas |

## Metrics

```yaml
spec:
  metrics:
    enabled: true
    # exporterImage: prom/statsd-exporter:v0.31.0
    # port: 9102
```

TigerBeetle only emits StatsD metrics, and only with `--experimental --statsd=<ip:port>`. With
metrics enabled, the operator passes those flags and adds a
[statsd_exporter](https://github.com/prometheus/statsd_exporter) sidecar that receives the metrics
on the pod's loopback interface and serves them in Prometheus format on port 9102. A headless
Service `<name>-metrics` selects the replica pods. Enabling or disabling metrics restarts every
replica.

Each series is labelled with `cluster` and `replica`. The health gauges are:

- `tb_replica_status`: 0 normal, 1 view change, 2 recovering, 3 recovering head (corrupted state)
- `tb_replica_sync_stage`: non-zero while the replica state-syncs

Readiness stays a TCP probe on the replica port; the metrics do not affect it.

`--experimental` has a cost at upgrade time. TigerBeetle warns: "If the cluster upgrades
automatically, and incompatible experimental CLI arguments are set, it will crash." Before upgrading
a cluster with metrics enabled, check the release notes for changes to experimental flags such as
`--statsd`.

A Prometheus Operator `ServiceMonitor`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: tigerbeetle
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: tigerbeetle
      app.kubernetes.io/component: replica
  endpoints:
    - port: metrics
```

## Failing replicas

A replica pod that crash-loops (for example `OOMKilled`), cannot pull its image, or cannot be
scheduled is reported right away as `Ready=False` with reason `ReplicaFailing`, a Warning event and
the replica's message. If it is still failing after 5 minutes, the cluster becomes `Stalled`.

### Stuck rollouts

A StatefulSet never replaces a pod that does not become ready, so fixing the spec (say, raising a
memory limit that was too low) would not reach a crash-looping replica. The operator deletes such a
pod so the StatefulSet recreates it from the updated spec, only when all of these hold:

- a rollout is in progress (the StatefulSet has a newer revision),
- the pod is still on an older revision and is not ready,
- it has been failing (as above) for at least 5 minutes,
- no other replica pod is terminating: pods are replaced one at a time.

Ready pods are never deleted, and data PVCs are kept. Each deletion emits `StuckReplicaRestarted`.

## A replica lost its data file

When a replica starts without its data file (its PVC was deleted or replaced), its `recover` init
container rebuilds it from the other replicas with `tigerbeetle recover`. It never formats: that
could lose committed data. Recovery needs the rest of the cluster to be running and healthy.

- `recoveryPolicy: Automatic` (default) recovers right away. If recovery waits for more than
  10 minutes, a `RecoveryDelayed` event says so.
- `recoveryPolicy: Manual` waits for your approval. The cluster becomes `Stalled` with reason
  `RecoveryApprovalRequired`, and the message contains the command to approve, for example:

  ```sh
  kubectl annotate tigerbeetlecluster tb -n prod --overwrite \
    tigerbeetle.codegrowers.com/approve-recovery=5da56227-8d5f-4741-8ee4-e482b93c04f9
  ```

  The value lists the UIDs of the waiting pods. An approval applies only to those pods, so leaving
  the annotation in place never approves a later loss. The pod starts recovering once the kubelet
  refreshes its annotations, usually within a minute.

A single-replica cluster cannot recover: there is no other copy. It is `Stalled` with reason
`ReplicaDataLost` until the volume is restored.

## Restoring a TigerBeetleCluster

Deleting a TigerBeetleCluster keeps its data PVCs. If you create it again (from Git or a backup),
the operator finds PVCs it did not format and refuses to touch them: `Stalled`, reason
`BootstrapConfirmationRequired`. Then either:

- **Start on the existing data.** Annotate the TigerBeetleCluster:

  ```sh
  kubectl annotate tigerbeetlecluster tb tigerbeetle.codegrowers.com/adopt-existing-data=true
  ```

  Every replica's PVC (`data-<name>-<ordinal>`) must exist. The operator starts the replicas without
  formatting. TigerBeetle refuses to start on data files of another cluster ID or replica count,
  which shows up as `ReplicaFailing`.

  The per-replica Services were deleted with the old TigerBeetleCluster, so the new ones have new
  IPs: update the address list of your clients from `status.addresses`.

- **Start a new cluster and discard the data.** Delete the data PVCs. The operator then formats
  fresh data files.

## Upgrades

Change `spec.image` to a newer TigerBeetle release. The StatefulSet replaces the replicas one at a
time (waiting `minReadySeconds` after each) and the phase is `Upgrading`. `UpgradeCompleted` follows
once every replica runs the new binary. TigerBeetle then switches the cluster to the new release by
itself, a few seconds later (with metrics enabled, `tb_release` changes).

In a 3-replica cluster on kind, upgrading 0.17.8 to 0.17.9 took about 40 seconds to roll and 20 more
to switch releases, with an existing account intact and a 0.17.8 client still working.

`development: true` disables TigerBeetle's upgrade polling: the replicas run the new binary but keep
the old release. Test upgrades without development mode.

Each TigerBeetle release lists the oldest release it can upgrade from, so you cannot skip too far
ahead; follow the [release notes](https://github.com/tigerbeetle/tigerbeetle/releases). Clients must
not be newer than the replicas.
