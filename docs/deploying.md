---
title: "Deploying a TigerBeetle cluster"
---

How to size and create a `TigerBeetleCluster`. Several decisions here cannot be changed later,
because TigerBeetle writes them into every data file: read the whole page before creating one.

## Decide before you create it

These fields are **immutable**; the API server rejects changes to them.

| Field | Why it cannot change |
|---|---|
| `clusterID` | Written into every data file; clients address the cluster by it |
| `replicas` | TigerBeetle fixes the replica count at `format` time and cannot grow or shrink a cluster ([upstream roadmap](https://github.com/tigerbeetle/tigerbeetle/issues/259)). A bigger cluster means a new cluster and an application-level migration |
| `storage.size`, `storage.storageClassName`, `storage.accessMode` | StatefulSet volume claim templates are immutable; volumes are not resized |

### Cluster ID

A random 128-bit unsigned integer, as a decimal string. It must be unique: TigerBeetle uses it to
make sure replicas and clients of different clusters never talk to each other.

```sh
python3 -c 'import secrets; print(secrets.randbits(128))'
```

`0` is reserved for testing and only accepted together with `development: true`.

### Replica count

| `replicas` | Replicas that can fail with the cluster still available |
|---|---|
| 1 | None. Losing its volume loses the data: there is no copy to recover from |
| 3 | 1 |
| 6 | 2 (the view-change quorum is 4 of 6) |

TigerBeetle [recommends six replicas](https://docs.tigerbeetle.com/operating/cluster/) for
production, spread over three sites with two replicas each, so that losing a whole site leaves a
quorum. Six is also the maximum the CRD accepts. For other sizes, see TigerBeetle's cluster
recommendations.

## Memory

TigerBeetle allocates all of its memory at startup. The operator therefore always sets the memory
**request equal to the limit**, even if you only set the limit, so the scheduler reserves what the
replica will actually use and the replica is never killed for exceeding a request it was promised.

The limit is split between TigerBeetle itself (about 3 GiB) and the **grid cache**, TigerBeetle's
page cache, which should be as large as possible. Unless you set `cacheGrid`, the operator passes
`--cache-grid = memory limit - 3GiB`:

| `resources.limits.memory` | Derived `--cache-grid` |
|---|---|
| 4Gi | 1 GiB (TigerBeetle's own default) |
| 8Gi | 5 GiB |
| 16Gi | 13 GiB |
| 32Gi | 29 GiB |

- The limit must exceed 3 GiB outside development mode; smaller limits are rejected with
  `Stalled=True`, reason `InvalidSpec`.
- An explicit `cacheGrid` must fit in the limit minus 3 GiB, or it is rejected the same way.
- TigerBeetle asks for at least 6 GiB of RAM per replica machine and recommends a 16–32 GiB cache
  or more in production, on ECC memory ([hardware](https://docs.tigerbeetle.com/operating/hardware/)).
- Leave about 1 GiB per node for the kubelet and system, which TigerBeetle's own guidance budgets
  for outside the replica. With metrics enabled, the exporter sidecar adds up to 128 MiB.

`development: true` shrinks TigerBeetle's caches and batches for small machines such as kind. A
replica still needs a 2Gi limit: 1Gi is OOMKilled at startup, because the journal alone takes 1 GiB.
Development mode is not for production: it also disables TigerBeetle's upgrade polling, so a
development cluster never switches to a new release.

## Storage

- **Minimum 2Gi.** A freshly formatted data file is already 1.06 GiB, before any data (measured on
  0.17.8). A 1Gi volume cannot hold it. Storage classes that do not enforce capacity, such as kind's
  `local-path`, hide this.
- **Size for growth up front.** The data file grows as data is written; TigerBeetle reports about
  16 TiB for 40 billion transfers, roughly 400 bytes per transfer. Volumes are not resized, so pick
  a size that covers the cluster's life.
- **Use fast local storage.** TigerBeetle recommends local NVMe; network-attached storage adds
  latency to every commit. ext4 limits a file to 16 TiB; XFS is also supported.
- Each replica gets its own PVC (`data-<name>-<ordinal>`). The operator never deletes them, not even
  when the `TigerBeetleCluster` is deleted.

## CPU

TigerBeetle uses a single core per replica. Request one core, and avoid a tight CPU limit: CFS
throttling shows up directly as commit latency.

```yaml
resources:
  requests:
    cpu: "1"
  limits:
    memory: 16Gi
```

## Placement

With the default `podAntiAffinity: Required`, no two replicas share a node, so the cluster needs at
least as many schedulable nodes as replicas. Each replica should also have its own disk and machine
(TigerBeetle's fault domains).

The operator does not yet spread replicas across zones: `Required` separates nodes, not zones. On a
multi-zone cluster, check where the replicas landed. `Preferred` exists for development clusters
with fewer nodes than replicas and weakens fault tolerance.

The operator creates a PodDisruptionBudget allowing one replica down at a time (for `replicas > 1`),
so node drains cannot take out a quorum.

## A production example

```yaml
apiVersion: tigerbeetle.codegrowers.com/v1alpha1
kind: TigerBeetleCluster
metadata:
  name: ledger
  namespace: payments
spec:
  clusterID: "187654321098765432109876543210"   # from the command above; never reuse one
  replicas: 6
  image: ghcr.io/tigerbeetle/tigerbeetle:0.17.9   # pin a version rather than the CRD default
  storage:
    size: 500Gi
    storageClassName: local-nvme
  resources:
    requests:
      cpu: "1"
    limits:
      memory: 24Gi            # --cache-grid 21GiB
  metrics:
    enabled: true
  recoveryPolicy: Manual      # a human approves rebuilding a lost replica
```

Then watch it come up:

```sh
kubectl -n payments get tbc ledger -w
kubectl -n payments describe tbc ledger
```

It goes `Bootstrapping` (one format Job per replica) → `Starting` → `Ready`. If it stops at
`Stalled=True`, the condition message says what it needs; [operations.md](operations.md) explains
each reason. Next, [connect your applications](clients.md).
