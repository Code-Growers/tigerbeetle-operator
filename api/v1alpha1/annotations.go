package v1alpha1

// Annotations users set on a TigerBeetleCluster to confirm actions that could lose data.
const (
	// AnnotationApproveRecovery approves recovering replicas whose data file is missing when
	// spec.recoveryPolicy is Manual. The value is a comma-separated list of the UIDs of the
	// waiting pods, which the operator reports in an event and the Stalled condition. An
	// approval applies only to that pod, so an annotation left in place never approves the
	// recovery of a later pod.
	AnnotationApproveRecovery = "tigerbeetle.codegrowers.com/approve-recovery"

	// AnnotationAdoptExistingData set to "true" starts the cluster on data PVCs that exist but
	// were not formatted by this TigerBeetleCluster, e.g. after the resource was deleted and
	// restored. Every replica's PVC must exist. Without it the operator refuses to format them.
	AnnotationAdoptExistingData = "tigerbeetle.codegrowers.com/adopt-existing-data"
)
