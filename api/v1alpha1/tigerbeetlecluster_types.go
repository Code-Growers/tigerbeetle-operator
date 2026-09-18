package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodAntiAffinityMode controls how strictly replicas are spread across nodes.
// +kubebuilder:validation:Enum=Required;Preferred
type PodAntiAffinityMode string

const (
	// PodAntiAffinityRequired never schedules two replicas on the same node.
	PodAntiAffinityRequired PodAntiAffinityMode = "Required"
	// PodAntiAffinityPreferred spreads replicas across nodes when possible.
	PodAntiAffinityPreferred PodAntiAffinityMode = "Preferred"
)

// RecoveryPolicy controls what happens when a replica's data file is missing after bootstrap.
// +kubebuilder:validation:Enum=Automatic;Manual
type RecoveryPolicy string

const (
	// RecoveryPolicyAutomatic runs `tigerbeetle recover` without human approval.
	RecoveryPolicyAutomatic RecoveryPolicy = "Automatic"
	// RecoveryPolicyManual waits for the tigerbeetle.codegrowers.com/approve-recovery annotation
	// before recovering.
	RecoveryPolicyManual RecoveryPolicy = "Manual"
)

// MetricsSpec configures metrics for the replicas.
type MetricsSpec struct {
	// enabled runs TigerBeetle with `--experimental --statsd` and adds a statsd_exporter sidecar
	// that serves the metrics in Prometheus format on `port` of every replica pod, behind the
	// headless Service <name>-metrics. Changing it restarts every replica.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// exporterImage is the statsd_exporter image.
	// +kubebuilder:default="prom/statsd-exporter:v0.31.0"
	// +optional
	ExporterImage string `json:"exporterImage,omitempty"`

	// port is the container port serving Prometheus metrics.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=9102
	// +optional
	Port int32 `json:"port,omitempty"`

	// resources are the compute resources for the exporter container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// StorageSpec defines the persistent storage requirements for a TigerBeetle replica.
// +kubebuilder:validation:XValidation:rule="has(self.storageClassName) == has(oldSelf.storageClassName) && (!has(self.storageClassName) || self.storageClassName == oldSelf.storageClassName)",message="storageClassName is immutable"
type StorageSpec struct {
	// size is the requested size of each replica's persistent volume.
	// Immutable: StatefulSet volume claim templates cannot be changed.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="size is immutable"
	// +required
	Size resource.Quantity `json:"size"`

	// storageClassName is the name of the StorageClass to use for the persistent volume.
	// If unset, the default StorageClass is used. Immutable.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// accessMode is the access mode for the persistent volume. Immutable.
	// +kubebuilder:validation:Enum=ReadWriteOnce;ReadWriteOncePod
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="accessMode is immutable"
	// +kubebuilder:default=ReadWriteOnce
	// +optional
	AccessMode corev1.PersistentVolumeAccessMode `json:"accessMode,omitempty"`
}

// TigerBeetleClusterSpec defines the desired state of TigerBeetleCluster.
// +kubebuilder:validation:XValidation:rule="self.clusterID != '0' || (has(self.development) && self.development)",message="clusterID 0 is reserved for testing; set development: true to use it"
// +kubebuilder:validation:XValidation:rule="self.replicas + (has(self.externalAddresses) ? size(self.externalAddresses) : 0) <= 6",message="replicas + externalAddresses must not exceed 6"
// +kubebuilder:validation:XValidation:rule="(has(self.externalAddresses) ? size(self.externalAddresses) : 0) == (has(oldSelf.externalAddresses) ? size(oldSelf.externalAddresses) : 0)",message="the number of externalAddresses is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.metrics) || !has(self.metrics.enabled) || !self.metrics.enabled || !has(self.metrics.port) || self.metrics.port != self.port",message="metrics.port must differ from port"
type TigerBeetleClusterSpec struct {
	// clusterID is the TigerBeetle cluster identifier: a 128-bit unsigned integer
	// as a decimal string. Use a random value; 0 is reserved for testing. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=39
	// +kubebuilder:validation:Pattern=`^(0|[1-9][0-9]*)$`
	// +kubebuilder:validation:XValidation:rule="size(self) < 39 || self <= '340282366920938463463374607431768211455'",message="clusterID must fit in an unsigned 128-bit integer"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterID is immutable"
	// +required
	ClusterID string `json:"clusterID"`

	// replicas is the number of TigerBeetle replicas to run in this Kubernetes cluster. Immutable.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=6
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="replicas is immutable"
	// +kubebuilder:default=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// image is the TigerBeetle server image to use.
	// +kubebuilder:default="ghcr.io/tigerbeetle/tigerbeetle:0.17.8"
	// +optional
	Image string `json:"image,omitempty"`

	// imagePullPolicy is the pull policy for the TigerBeetle image.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default=IfNotPresent
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// port is the port TigerBeetle listens on.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=3000
	// +optional
	Port int32 `json:"port,omitempty"`

	// development passes --development to TigerBeetle. Required where Direct IO
	// is unavailable or memory is small (e.g. kind). Never use it in production.
	// +optional
	Development bool `json:"development,omitempty"`

	// cacheGrid is the TigerBeetle grid cache size (e.g. "4GiB").
	// If unset, it is derived from the memory limit (limit - 3GiB) outside development mode.
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*(KiB|MiB|GiB)$`
	// +optional
	CacheGrid string `json:"cacheGrid,omitempty"`

	// storage describes the persistent volume claim for each replica.
	// +required
	Storage StorageSpec `json:"storage"`

	// resources are the compute resources for the TigerBeetle container.
	// The memory request is always set equal to the memory limit.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// podAntiAffinity controls replica spreading across nodes.
	// Use Preferred only for development clusters with fewer nodes than replicas.
	// +kubebuilder:default=Required
	// +optional
	PodAntiAffinity PodAntiAffinityMode `json:"podAntiAffinity,omitempty"`

	// minReadySeconds is how long a replica must stay ready before a rolling update
	// continues with the next one. A replica accepts connections before it has caught
	// up with the cluster, so rolling straight on can take down a second replica too early.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3600
	// +kubebuilder:default=30
	// +optional
	MinReadySeconds int32 `json:"minReadySeconds,omitempty"`

	// recoveryPolicy controls whether a replica with a missing data file is recovered
	// automatically or waits for the tigerbeetle.codegrowers.com/approve-recovery annotation.
	// +kubebuilder:default=Automatic
	// +optional
	RecoveryPolicy RecoveryPolicy `json:"recoveryPolicy,omitempty"`

	// metrics configures Prometheus metrics for the replicas.
	// +optional
	Metrics MetricsSpec `json:"metrics,omitempty"`

	// sidecars is a list of additional containers to run in the TigerBeetle pod.
	// Useful for VPN or mesh sidecars.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:XValidation:rule="self.all(c, c.name != 'tigerbeetle' && c.name != 'statsd-exporter')",message="sidecar names tigerbeetle and statsd-exporter are reserved"
	Sidecars []corev1.Container `json:"sidecars,omitempty"`

	// externalAddresses is an ordered list of external TigerBeetle replica addresses
	// (ip:port) appended after the in-cluster replicas. The order must match the
	// external replicas' --replica indices. The number of entries is immutable.
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:items:MaxLength=64
	// +optional
	ExternalAddresses []string `json:"externalAddresses,omitempty"`
}

// ReplicaState is the lifecycle state of a single replica.
type ReplicaState string

const (
	ReplicaStatePending          ReplicaState = "Pending"
	ReplicaStateFormatting       ReplicaState = "Formatting"
	ReplicaStateFormatted        ReplicaState = "Formatted"
	ReplicaStateRecovering       ReplicaState = "Recovering"
	ReplicaStateAwaitingApproval ReplicaState = "AwaitingApproval"
	ReplicaStateFailing          ReplicaState = "Failing"
	ReplicaStateRunning          ReplicaState = "Running"
)

// ReplicaStatus is the observed state of a single replica.
type ReplicaStatus struct {
	// index is the replica index (StatefulSet ordinal).
	Index int32 `json:"index"`

	// state is the lifecycle state of the replica.
	State ReplicaState `json:"state"`

	// message explains a replica that is failing or waiting for human action.
	// +optional
	Message string `json:"message,omitempty"`
}

// Phase is a high-level summary of the cluster state, derived from conditions.
type Phase string

const (
	PhaseBootstrapping Phase = "Bootstrapping"
	PhaseStarting      Phase = "Starting"
	PhaseReady         Phase = "Ready"
	PhaseDegraded      Phase = "Degraded"
	PhaseUpgrading     Phase = "Upgrading"
)

// TigerBeetleClusterStatus defines the observed state of TigerBeetleCluster.
type TigerBeetleClusterStatus struct {
	// observedGeneration is the metadata.generation last fully reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase is a high-level summary of the cluster state for display.
	// Tools should rely on conditions instead.
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// bootstrapped is true once every replica has been formatted. It never returns to false.
	// +optional
	Bootstrapped bool `json:"bootstrapped,omitempty"`

	// replicaCount is the total TigerBeetle replica count (replicas + externalAddresses).
	// +optional
	ReplicaCount int32 `json:"replicaCount,omitempty"`

	// readyReplicas is the number of in-cluster replicas that are ready.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// currentImage is the image every replica runs, updated once a rollout completes.
	// +optional
	CurrentImage string `json:"currentImage,omitempty"`

	// addresses is the comma-separated, ordered list of all replica addresses
	// (internal + external) that clients must use.
	// +optional
	Addresses string `json:"addresses,omitempty"`

	// serviceIPs are the ClusterIPs of the per-replica Services, by ordinal.
	// Used to re-pin a deleted Service to its previous IP.
	// +optional
	ServiceIPs []string `json:"serviceIPs,omitempty"`

	// replicaStatuses is the observed state of each in-cluster replica.
	// +optional
	ReplicaStatuses []ReplicaStatus `json:"replicaStatuses,omitempty"`

	// conditions represent the current state of the TigerBeetleCluster resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tbc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Addresses",type=string,JSONPath=`.status.addresses`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TigerBeetleCluster is the Schema for the tigerbeetleclusters API.
type TigerBeetleCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of TigerBeetleCluster
	// +required
	Spec TigerBeetleClusterSpec `json:"spec"`

	// status defines the observed state of TigerBeetleCluster
	// +optional
	Status TigerBeetleClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TigerBeetleClusterList contains a list of TigerBeetleCluster.
type TigerBeetleClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TigerBeetleCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TigerBeetleCluster{}, &TigerBeetleClusterList{})
}
