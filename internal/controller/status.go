package controller

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tbv1 "github.com/Code-Growers/tigerbeetle-operator/api/v1alpha1"
)

// setStatus derives conditions, phase and observedGeneration from the reconcile outcome and
// emits Warning events for problems that are new or whose message changed.
func (r *TigerBeetleClusterReconciler) setStatus(orig, tbc *tbv1.TigerBeetleCluster, obs *observation, err error) {
	st := &tbc.Status
	gen := tbc.Generation
	set := func(condType string, status metav1.ConditionStatus, reason, message string) {
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{
			Type: condType, Status: status, Reason: reason, Message: message, ObservedGeneration: gen,
		})
	}
	prev := func(condType string) *metav1.Condition {
		return meta.FindStatusCondition(orig.Status.Conditions, condType)
	}

	if st.Bootstrapped {
		set(ConditionBootstrapped, metav1.ConditionTrue, ReasonFormatCompleted, "All replicas formatted")
	} else {
		set(ConditionBootstrapped, metav1.ConditionFalse, ReasonBootstrapping, "Replicas are not formatted yet")
	}

	var se *stalledError
	switch {
	case errors.As(err, &se):
		if p := prev(ConditionStalled); p == nil || p.Status != metav1.ConditionTrue || p.Reason != se.reason || p.Message != se.message {
			r.Recorder.Event(tbc, corev1.EventTypeWarning, se.reason, se.message)
		}
		set(ConditionStalled, metav1.ConditionTrue, se.reason, se.message)
		set(ConditionReconciling, metav1.ConditionFalse, ReasonStalled, "Progress requires human action")
		set(ConditionReady, metav1.ConditionFalse, se.reason, se.message)
		st.Phase = tbv1.PhaseDegraded
		st.ObservedGeneration = gen
		return

	case err != nil:
		msg := err.Error()
		if !apierrors.IsConflict(err) {
			if p := prev(ConditionReconciling); p == nil || p.Reason != ReasonReconcileError || p.Message != msg {
				r.Recorder.Event(tbc, corev1.EventTypeWarning, ReasonReconcileError, msg)
			}
		}
		// Transient: keep Ready and phase as they were, so health does not flap.
		set(ConditionReconciling, metav1.ConditionTrue, ReasonReconcileError, msg)
		set(ConditionStalled, metav1.ConditionFalse, ReasonNotStalled, "")
		if meta.FindStatusCondition(st.Conditions, ConditionReady) == nil {
			set(ConditionReady, metav1.ConditionFalse, ReasonReconcileError, msg)
		}
		if st.Phase == "" {
			st.Phase = tbv1.PhaseBootstrapping
		}
		return
	}

	set(ConditionStalled, metav1.ConditionFalse, ReasonNotStalled, "")
	st.ObservedGeneration = gen

	switch {
	case !obs.bootstrapped:
		msg := "Waiting for per-replica Services"
		if obs.bootstrapProgress != "" {
			msg = "Formatting replicas: " + obs.bootstrapProgress
		}
		set(ConditionReconciling, metav1.ConditionTrue, ReasonBootstrapping, msg)
		set(ConditionReady, metav1.ConditionFalse, ReasonBootstrapping, msg)
		st.Phase = tbv1.PhaseBootstrapping

	case !obs.allReady:
		reason, readyReason, msg := r.notReadyReasons(orig, tbc, obs)
		set(ConditionReconciling, metav1.ConditionTrue, reason, msg)
		set(ConditionReady, metav1.ConditionFalse, readyReason, msg)
		st.Phase = tbv1.PhaseStarting
		if orig.Status.CurrentImage != "" && orig.Status.CurrentImage != tbc.Spec.Image {
			st.Phase = tbv1.PhaseUpgrading
		}

	default:
		if p := prev(ConditionReconciling); p != nil && p.Status == metav1.ConditionTrue && p.Reason == ReasonRolloutInProgress {
			r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonRolloutCompleted,
				"StatefulSet %s rollout completed", obs.statefulSetName)
		}
		msg := fmt.Sprintf("%d/%d replicas ready", st.ReadyReplicas, tbc.Spec.Replicas)
		set(ConditionReconciling, metav1.ConditionFalse, ReasonReconciled, msg)
		set(ConditionReady, metav1.ConditionTrue, ReasonAllReplicasReady, msg)
		st.Phase = tbv1.PhaseReady
	}
}

// notReadyReasons explains why not every replica is ready: the Reconciling and Ready reasons
// and their shared message. It emits Warning events for failing and slowly recovering
// replicas when their message changes.
func (r *TigerBeetleClusterReconciler) notReadyReasons(orig, tbc *tbv1.TigerBeetleCluster, obs *observation) (reason, readyReason, msg string) {
	prev := func(condType string) *metav1.Condition {
		return meta.FindStatusCondition(orig.Status.Conditions, condType)
	}
	msg = fmt.Sprintf("%d/%d replicas ready", tbc.Status.ReadyReplicas, tbc.Spec.Replicas)

	// currentImage is set after the first complete rollout, so until then replicas are
	// starting for the first time rather than rolling.
	readyReason = ReasonReplicaUnavailable
	if obs.rolloutInProgress && orig.Status.CurrentImage != "" {
		readyReason = ReasonRolloutInProgress
	}
	reason = readyReason

	switch {
	case len(obs.failing) > 0:
		// Escalates to Stalled once a replica has failed for replicaFailingStallAfter.
		readyReason, reason = ReasonReplicaFailing, ReasonReplicaFailing
		msg = fmt.Sprintf("%s; %s", msg, obs.failingMessage())
		if p := prev(ConditionReady); p == nil || p.Reason != ReasonReplicaFailing || p.Message != msg {
			r.Recorder.Event(tbc, corev1.EventTypeWarning, ReasonReplicaFailing, obs.failingMessage())
		}
	case len(obs.recovering) > 0:
		reason = ReasonRecovering
		msg = fmt.Sprintf("%s; recovering replicas %v", msg, obs.recovering)
		if len(obs.recoveryDelayed) > 0 {
			delayed := fmt.Sprintf("Replicas %v have waited over %s to recover their data file; "+
				"recovery needs the other replicas running and healthy", obs.recoveryDelayed, recoveryDelayedAfter)
			msg = fmt.Sprintf("%s; %s", msg, delayed)
			if p := prev(ConditionReconciling); p == nil || p.Message != msg {
				r.Recorder.Event(tbc, corev1.EventTypeWarning, ReasonRecoveryDelayed, delayed)
			}
		}
	case orig.Status.CurrentImage == "":
		reason = ReasonStarting
	}
	return reason, readyReason, msg
}
