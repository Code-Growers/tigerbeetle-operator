---
title: "Multi-cluster TigerBeetle: research notes (undecided)"
---

Status: **open**. Parked on 2026-09-15; revisit before Phase 4. Nothing here is implemented.

## Why the current Phase 4 design is wrong

`plan.md` Phase 4 joins replica groups across Kubernetes clusters with independent operators and
`spec.externalAddresses`. Research into other operators (below) shows three rules it breaks:

1. **Identical addresses.** Each side would list its own replicas by ClusterIP and remote replicas by
   a routable IP, so `--addresses` differs per side. TigerBeetle requires it to be identical for all
   replicas and clients.
2. **Topology known everywhere.** `externalAddresses` gives each side a partial view, and the user
   must order it complementarily by hand.
3. **Coordinated bootstrap.** Each side formats on its own, with no shared marker. If one side
   loses all its PVCs while the other runs, its local checks say "not bootstrapped" and it would
   format replicas of a cluster that holds committed data.

## Quorums and number of sites

Printed by `tigerbeetle start` 0.17.8 (`init: replica_count=... quorum_view_change=... quorum_replication=...`):

| Replicas | Replication quorum | View-change quorum |
|---|---|---|
| 3 | 2 | 2 |
| 6 | 3 | 4 |

- **Two sites never survive losing a site.** A 3/3 split leaves 3 replicas, below the view-change
  quorum of 4. A commit needs only 3 copies, which can all be on the lost side.
- **Three sites do.** TigerBeetle docs agree: "For mission critical availability, the optimal number
  of sites is 3", with 2 replicas per site.

## How other operators do it

| Operator | Coordination | Topology knowledge | Cross-cluster network | Clusters |
|---|---|---|---|---|
| **Redpanda** (`StretchCluster`) | One operator per cluster; the operators form their own Raft group over TLS; peer list fixed at install; each operator caches kubeconfigs of its peers | Identical `StretchCluster` in every cluster + local `RedpandaBrokerPool` (`spec.clusterRef`) | Required mode: `mesh` (Cilium ClusterMesh/Istio), `flat` (routable pod IPs), or `mcs` (MCS API, `clusterset.local`); per-pod Services; latency < 50 ms | ≥ 3 ("two-cluster deployments are not recommended") |
| **K8ssandra** | Central control-plane operator; remote clusters registered as `ClientConfig` (kubeconfig) | One `K8ssandraCluster` on the control-plane cluster; each datacenter sets `k8sContext` | "routable network connectivity among the pods in each cluster" | 1 control plane + N data planes |
| **CockroachDB** | One operator per region, no operator-to-operator link | A `CrdbCluster` per cluster whose `regions` lists **all** regions (`nodes`, `domain`, `namespace`) | VPC peering + CoreDNS forwarding; shared CA | Topology decides; region survival needs ≥ 3 |
| **TiDB** | One operator per cluster | First cluster initializes; later clusters reference it via `spec.cluster` (`name`, `namespace`, `clusterDomain`) and set `acrossK8s: true` | Pod IPs and pod FQDNs reachable across clusters | Topology decides |
| **Strimzi** | Not shipped; roadmap via node pools | Prototype spreads node pools across clusters | Submariner / Cilium | – |

What they all have in common:
- a cross-cluster network layer, so every member uses the same addresses
- full topology in every cluster (the same resource everywhere) or in one central resource
- coordinated bootstrap: one side initializes, a central operator decides, or the operators agree through Raft

## Sketch of a TigerBeetle design

- **Shared topology resource.** The same `TigerBeetleCluster` is applied in every Kubernetes cluster
  with an ordered site list, e.g. `sites: [{name: a, replicas: 2}, {name: b, replicas: 2}, {name: c, replicas: 2}]`.
  Each operator knows which site it is (operator flag or field). Replica indices follow from site
  order, so nobody orders addresses by hand.
- **Identical, stable IPs.** TigerBeetle accepts IPs only, no DNS names. Candidates: MCS cluster-set IPs,
  LoadBalancer IPs pinned in the spec, Service IPs mirrored by a mesh. Requires a declared network mode like Redpanda's.
- **Bootstrap coordination.** Still to choose:
  - central operator with kubeconfigs (K8ssandra): simpler, but the central cluster is a single point of failure for management
  - peer operators agreeing through Raft (Redpanda): no central cluster, much more complex
  - "first site initializes, others join" (TiDB): requires a TigerBeetle-level answer to how late sites format safely
- **At least 3 Kubernetes clusters**, matching TigerBeetle's 3 sites × 2 replicas.

## Proposed interim direction (not yet agreed)

1. Support one Kubernetes cluster spread across 3 zones, each zone a TigerBeetle site (2 replicas per
   zone). Needs a zone `topologySpreadConstraint` (Phase 2).
2. Remove `externalAddresses` from `v1alpha1` until the stretch design exists.
3. Replace Phase 4 with a stretch-cluster design following the notes above.

## Open questions

- What is the real use case: region failure, multiple clouds, or migrating between Kubernetes clusters?
- Central or peer coordination for bootstrap?
- Which network modes to support first (MCS, mesh, LoadBalancer IPs)?

## Sources

- [TigerBeetle: Cluster recommendations](https://docs.tigerbeetle.com/operating/cluster/)
- [Redpanda: Deploy a Stretch Cluster on Kubernetes](https://docs.redpanda.com/streaming/current/deploy/redpanda/kubernetes/k-stretch-clusters/)
- [K8ssandra Operator](https://docs.k8ssandra.io/components/k8ssandra-operator/)
- [CockroachDB: Deploy with the CockroachDB Operator](https://docs.cockroachlabs.com/docs/stable/deploy-cockroachdb-with-cockroachdb-operator)
- [TiDB: Deploy a TiDB Cluster across Multiple Kubernetes Clusters](https://docs.pingcap.com/tidb-in-kubernetes/stable/deploy-tidb-cluster-across-multiple-kubernetes/)
- [Strimzi: Stretch Kafka cluster over multiple Kubernetes clusters (#3697)](https://github.com/strimzi/strimzi-kafka-operator/issues/3697)
