package controller

// Condition types (kstatus-compatible).
const (
	ConditionReady        = "Ready"
	ConditionReconciling  = "Reconciling"
	ConditionStalled      = "Stalled"
	ConditionBootstrapped = "Bootstrapped"
)

// Event and condition reasons. Alerting rules match on these values; never use free text.
const (
	// Normal events.
	ReasonBootstrapStarted      = "BootstrapStarted"
	ReasonReplicaFormatted      = "ReplicaFormatted"
	ReasonBootstrapCompleted    = "BootstrapCompleted"
	ReasonRolloutStarted        = "RolloutStarted"
	ReasonRolloutCompleted      = "RolloutCompleted"
	ReasonUpgradeCompleted      = "UpgradeCompleted"
	ReasonServiceIPRepinned     = "ServiceIPRepinned"
	ReasonRecoveryApproved      = "RecoveryApproved"
	ReasonExistingDataAdopted   = "ExistingDataAdopted"
	ReasonStuckReplicaRestarted = "StuckReplicaRestarted"

	// Warning events and Stalled reasons.
	ReasonReconcileError                = "ReconcileError"
	ReasonInvalidSpec                   = "InvalidSpec"
	ReasonFormatJobFailed               = "FormatJobFailed"
	ReasonBootstrapConfirmationRequired = "BootstrapConfirmationRequired"
	ReasonServiceIPRepinFailed          = "ServiceIPRepinFailed"
	ReasonReplicaFailing                = "ReplicaFailing"
	ReasonRecoveryApprovalRequired      = "RecoveryApprovalRequired"
	ReasonReplicaDataLost               = "ReplicaDataLost"
	ReasonRecoveryDelayed               = "RecoveryDelayed"

	// Condition-only reasons.
	ReasonAllReplicasReady   = "AllReplicasReady"
	ReasonReplicaUnavailable = "ReplicaUnavailable"
	ReasonRolloutInProgress  = "RolloutInProgress"
	ReasonBootstrapping      = "Bootstrapping"
	ReasonStarting           = "Starting"
	ReasonRecovering         = "Recovering"
	ReasonFormatCompleted    = "FormatCompleted"
	ReasonReconciled         = "Reconciled"
	ReasonNotStalled         = "NotStalled"
	ReasonStalled            = "Stalled"
)
