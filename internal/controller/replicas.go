package controller

import (
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	tbv1 "github.com/Code-Growers/tigerbeetle-operator/api/v1alpha1"
	"github.com/Code-Growers/tigerbeetle-operator/internal/tigerbeetle"
)

const (
	// replicaFailingStallAfter is how long a replica may fail before the cluster is Stalled.
	replicaFailingStallAfter = 5 * time.Minute
	// stuckPodRestartAfter is how long a failing pod on an outdated revision may block a
	// rollout before the operator deletes it.
	stuckPodRestartAfter = 5 * time.Minute
	// recoveryDelayedAfter is how long `tigerbeetle recover` may wait before a Warning event.
	recoveryDelayedAfter = 10 * time.Minute
)

// annotationTrue is the value of boolean annotations.
const annotationTrue = "true"

// podFailureReasons are container waiting reasons that do not resolve without a change.
var podFailureReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
}

// imageFailureReasons carry a useful kubelet message (the image and the registry error).
var imageFailureReasons = map[string]bool{
	"ImagePullBackOff": true,
	"ErrImagePull":     true,
	"InvalidImageName": true,
}

// replicaIssue is a replica pod that cannot run.
type replicaIssue struct {
	ordinal int
	pod     string
	message string
	// since is when the pod stopped being ready (or became unschedulable).
	since time.Time
}

// podFailure reports why a pod cannot run, if it is failing rather than starting.
func podFailure(pod *corev1.Pod) (message string, since time.Time, failing bool) {
	if !pod.DeletionTimestamp.IsZero() {
		return "", time.Time{}, false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return "Unschedulable: " + c.Message, c.LastTransitionTime.Time, true
		}
	}

	statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
	for _, s := range statuses {
		if s.State.Waiting == nil || !podFailureReasons[s.State.Waiting.Reason] {
			continue
		}
		msg := fmt.Sprintf("container %s %s", s.Name, s.State.Waiting.Reason)
		if imageFailureReasons[s.State.Waiting.Reason] && s.State.Waiting.Message != "" {
			msg += ": " + s.State.Waiting.Message
		}
		if t := s.LastTerminationState.Terminated; t != nil {
			msg += fmt.Sprintf(", last exit %s (code %d)", t.Reason, t.ExitCode)
		}
		return msg, unreadySince(pod), true
	}
	return "", time.Time{}, false
}

// unreadySince is when the pod last became unready, or when it was created.
func unreadySince(pod *corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status != corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time
		}
	}
	return pod.CreationTimestamp.Time
}

// stuckPod returns the pod blocking a StatefulSet rollout, if any: a pod not on the update
// revision that has been failing for stuckPodRestartAfter. A StatefulSet never replaces a pod
// that does not become ready, so a fix in the spec (say, a higher memory limit for an
// OOMKilled replica) would otherwise never reach it. Ready pods are never returned, and
// nothing is returned while any pod is terminating, so pods are replaced one at a time.
func stuckPod(sts *appsv1.StatefulSet, pods []corev1.Pod, now time.Time) *corev1.Pod {
	st := sts.Status
	// Compare each pod with updateRevision, not currentRevision with updateRevision: reverting a
	// bad change makes the update revision equal the current one again, while the pods created
	// from the bad change stay stuck on their own revision.
	if st.ObservedGeneration < sts.Generation || st.UpdateRevision == "" {
		return nil
	}
	var stuck *corev1.Pod
	stuckOrdinal := -1
	for i := range pods {
		pod := &pods[i]
		if !pod.DeletionTimestamp.IsZero() {
			return nil
		}
		if pod.Labels[appsv1.ControllerRevisionHashLabelKey] == st.UpdateRevision || podReady(pod) {
			continue
		}
		_, since, failing := podFailure(pod)
		if !failing || now.Sub(since) < stuckPodRestartAfter {
			continue
		}
		// Like the StatefulSet controller, replace the highest ordinal first.
		ordinal, err := tigerbeetle.OrdinalFromHostname(pod.Name)
		if err == nil && ordinal > stuckOrdinal {
			stuck, stuckOrdinal = pod, ordinal
		}
	}
	return stuck
}

// recoveryApproved reports whether the operator has approved the pod's recovery.
func recoveryApproved(pod *corev1.Pod) bool {
	return pod.Annotations[tigerbeetle.AnnotationRecoveryApproved] == annotationTrue
}

// approvedPodUIDs parses the approve-recovery annotation: a comma-separated list of pod UIDs.
func approvedPodUIDs(tbc *tbv1.TigerBeetleCluster) map[types.UID]bool {
	approved := map[types.UID]bool{}
	for uid := range strings.SplitSeq(tbc.Annotations[tbv1.AnnotationApproveRecovery], ",") {
		if uid = strings.TrimSpace(uid); uid != "" {
			approved[types.UID(uid)] = true
		}
	}
	return approved
}

// initContainerStartedAt returns when a running init container started.
func initContainerStartedAt(pod *corev1.Pod, name string) (time.Time, bool) {
	for _, s := range pod.Status.InitContainerStatuses {
		if s.Name == name && s.State.Running != nil {
			return s.State.Running.StartedAt.Time, true
		}
	}
	return time.Time{}, false
}
