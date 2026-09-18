# Connecting applications

A TigerBeetle client needs two things: the **cluster ID** and the **addresses of every replica**, in
replica order. The operator publishes both.

## Where to find them

In the `<name>-config` ConfigMap, next to the `TigerBeetleCluster`:

| Key | Example |
|---|---|
| `clusterID` | `187654321098765432109876543210` |
| `addresses` | `10.96.12.1:3000,10.96.12.2:3000,10.96.12.3:3000` |
| `replicaCount` | `3` |
| `port` | `3000` |

And in the resource's status:

```sh
kubectl -n payments get tbc ledger -o jsonpath='{.status.addresses}'
```

## Rules the address list must follow

TigerBeetle is strict about addresses, and the operator is built around that:

- **Every replica, in order.** Pass the whole `addresses` value as-is. Do not sort it, and do not
  pass a subset: the client must be able to reach whichever replica is primary.
- **IPs only.** TigerBeetle rejects DNS names, so the addresses are the IPs of one ClusterIP
  Service per replica, not pod IPs. They survive pods being rescheduled; a deleted Service is
  recreated with its old IP.
- **Inside the cluster.** They are ClusterIPs, so clients must run in the Kubernetes cluster (or on
  a network that routes its Service range). There is no external exposure yet.
- **Read at startup.** Clients, like replicas, read the list once. If it changes, restart them.

The list only changes in rare cases, all of which show up on the `TigerBeetleCluster`: `spec.port`
changes, a deleted Service cannot get its old IP back (`Stalled`, reason `ServiceIPRepinFailed`), or
the resource is deleted and restored (see [operations.md](operations.md#restoring-a-tigerbeetlecluster)).

## Wiring a Deployment to the ConfigMap

For applications in the same namespace, reference the ConfigMap so the values never drift from what
the operator publishes:

```yaml
env:
  - name: TB_CLUSTER_ID
    valueFrom:
      configMapKeyRef:
        name: ledger-config
        key: clusterID
  - name: TB_ADDRESSES
    valueFrom:
      configMapKeyRef:
        name: ledger-config
        key: addresses
```

Split `TB_ADDRESSES` on commas and pass the list to the client, keeping the order. The cluster ID is
a 128-bit integer; parse it with your client library's 128-bit helper rather than a 64-bit integer
type. See the [TigerBeetle client docs](https://docs.tigerbeetle.com/coding/clients/) for each
language.

Environment variables are fixed when a pod starts, which matches how clients read the list anyway:
after an address change, restart the Deployment. Applications in other namespaces cannot reference
the ConfigMap directly; copy the values, or read `status.addresses`.

If the namespace has a default-deny NetworkPolicy, allow your clients to reach the replica pods on
`spec.port`, and allow the replicas to reach each other on the same port.

## Client versions

- A client must not be newer than the replicas: TigerBeetle refuses the connection with an error.
- Upgrade the replicas first (change `spec.image`, see [operations.md](operations.md#upgrades)), wait
  for `UpgradeCompleted`, then upgrade client libraries.
- Each TigerBeetle release lists the oldest client it still supports; check the release notes
  before skipping several releases.

## Trying it by hand

`tigerbeetle repl` is a client too. Run it in the cluster:

```sh
ADDRESSES=$(kubectl -n payments get tbc ledger -o jsonpath='{.status.addresses}')
CLUSTER_ID=$(kubectl -n payments get tbc ledger -o jsonpath='{.spec.clusterID}')
kubectl -n payments run tb-repl --rm -it --restart=Never \
  --image=ghcr.io/tigerbeetle/tigerbeetle:0.17.9 --command -- \
  /tigerbeetle repl --cluster="$CLUSTER_ID" --addresses="$ADDRESSES"
```

Use the same TigerBeetle version as the replicas (or older). Note that every client session counts
toward TigerBeetle's limit of 64 sessions per cluster; the oldest are evicted beyond that, so do not
use the REPL as a health probe.
