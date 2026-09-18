package tigerbeetle

// Environment variables the operator sets on TigerBeetle containers and the init helper reads.
const (
	EnvClusterID      = "TB_CLUSTER_ID"
	EnvReplica        = "TB_REPLICA" // optional; derived from the pod hostname when unset
	EnvReplicaCount   = "TB_REPLICA_COUNT"
	EnvAddresses      = "TB_ADDRESSES"
	EnvPort           = "TB_PORT"
	EnvDataDir        = "TB_DATA_DIR"
	EnvDevelopment    = "TB_DEVELOPMENT"
	EnvCacheGrid      = "TB_CACHE_GRID"
	EnvRecoveryPolicy = "TB_RECOVERY_POLICY"
	EnvBinary         = "TB_BINARY" // optional; defaults to DefaultBinary
)

const (
	// DefaultBinary is the TigerBeetle binary path in the official image.
	DefaultBinary = "/tigerbeetle"
	// DefaultDataDir is where the data volume is mounted.
	DefaultDataDir = "/data"
)

// Metrics.
const (
	// EnvStatsD is the StatsD address TigerBeetle sends metrics to; unset disables metrics.
	EnvStatsD = "TB_STATSD"
	// StatsDAddress is where the statsd_exporter sidecar listens inside the pod.
	StatsDAddress = "127.0.0.1:9125"
)

// Manual recovery approval, passed from the operator to the waiting init container.
const (
	// AnnotationRecoveryApproved is set to "true" on a replica pod once its recovery is approved.
	AnnotationRecoveryApproved = "tigerbeetle.codegrowers.com/recovery-approved"
	// PodInfoDir is where the pod's downward API volume (its annotations) is mounted.
	PodInfoDir = "/etc/tigerbeetle/podinfo"
	// PodInfoAnnotationsFile is the file in PodInfoDir listing the pod's annotations.
	PodInfoAnnotationsFile = "annotations"
)
