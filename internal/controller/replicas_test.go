package controller

import (
	"maps"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	tbv1 "github.com/Code-Growers/tigerbeetle-operator/api/v1alpha1"
)

var testNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

type podOption func(*corev1.Pod)

func testPod(name, revision string, opts ...podOption) corev1.Pod {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:              name,
		UID:               types.UID(name + "-uid"),
		CreationTimestamp: metav1.NewTime(testNow.Add(-time.Hour)),
		Labels:            map[string]string{appsv1.ControllerRevisionHashLabelKey: revision},
	}}
	for _, opt := range opts {
		opt(&pod)
	}
	return pod
}

func ready(pod *corev1.Pod) {
	pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
}

func unreadyFor(d time.Duration) podOption {
	return func(pod *corev1.Pod) {
		pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
			Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(testNow.Add(-d)),
		})
	}
}

func waiting(container, reason string) podOption {
	return func(pod *corev1.Pod) {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
			Name:  container,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "details"}},
		})
	}
}

func oomKilled(pod *corev1.Pod) {
	s := &pod.Status.ContainerStatuses[len(pod.Status.ContainerStatuses)-1]
	s.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}
}

func terminating(pod *corev1.Pod) {
	now := metav1.NewTime(testNow)
	pod.DeletionTimestamp = &now
}

func TestPodFailure(t *testing.T) {
	tests := []struct {
		name        string
		pod         corev1.Pod
		wantFailing bool
		wantMessage string
	}{
		{name: "ready", pod: testPod("tb-0", "a", ready)},
		{name: "starting", pod: testPod("tb-0", "a", unreadyFor(time.Minute), waiting("tigerbeetle", "ContainerCreating"))},
		{
			name:        "crash loop after OOM",
			pod:         testPod("tb-0", "a", unreadyFor(time.Minute), waiting("tigerbeetle", "CrashLoopBackOff"), oomKilled),
			wantFailing: true,
			wantMessage: "container tigerbeetle CrashLoopBackOff, last exit OOMKilled (code 137)",
		},
		{
			name:        "image pull includes the kubelet message",
			pod:         testPod("tb-0", "a", waiting("tigerbeetle", "ImagePullBackOff")),
			wantFailing: true,
			wantMessage: "container tigerbeetle ImagePullBackOff: details",
		},
		{
			name: "unschedulable",
			pod: testPod("tb-0", "a", func(pod *corev1.Pod) {
				pod.Status.Conditions = []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
					Reason: corev1.PodReasonUnschedulable, Message: "0/3 nodes are available",
				}}
			}),
			wantFailing: true,
			wantMessage: "Unschedulable: 0/3 nodes are available",
		},
		{name: "terminating", pod: testPod("tb-0", "a", waiting("tigerbeetle", "CrashLoopBackOff"), terminating)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message, _, failing := podFailure(&tt.pod)
			if failing != tt.wantFailing || message != tt.wantMessage {
				t.Errorf("got (%q, %v), want (%q, %v)", message, failing, tt.wantMessage, tt.wantFailing)
			}
		})
	}
}

func TestStuckPod(t *testing.T) {
	rolling := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Status:     appsv1.StatefulSetStatus{ObservedGeneration: 2, CurrentRevision: "old", UpdateRevision: "new"},
	}
	crashing := func(d time.Duration) []podOption {
		return []podOption{unreadyFor(d), waiting("tigerbeetle", "CrashLoopBackOff")}
	}

	tests := []struct {
		name string
		sts  appsv1.StatefulSet
		pods []corev1.Pod
		want string
	}{
		{
			name: "every pod on the update revision",
			sts: appsv1.StatefulSet{Status: appsv1.StatefulSetStatus{
				CurrentRevision: "old", UpdateRevision: "old",
			}},
			pods: []corev1.Pod{testPod("tb-0", "old", crashing(time.Hour)...)},
		},
		{
			name: "status not observed yet",
			sts: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 3},
				Status:     rolling.Status,
			},
			pods: []corev1.Pod{testPod("tb-0", "old", crashing(time.Hour)...)},
		},
		{
			name: "failing on the old revision long enough",
			sts:  rolling,
			pods: []corev1.Pod{testPod("tb-0", "old", ready), testPod("tb-1", "old", crashing(10*time.Minute)...)},
			want: "tb-1",
		},
		{
			name: "spec reverted to the current revision while a pod is stuck on the bad one",
			sts: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Generation: 3},
				Status:     appsv1.StatefulSetStatus{ObservedGeneration: 3, CurrentRevision: "old", UpdateRevision: "old"},
			},
			pods: []corev1.Pod{testPod("tb-0", "old", ready), testPod("tb-1", "bad", crashing(10*time.Minute)...)},
			want: "tb-1",
		},
		{
			name: "highest ordinal first",
			sts:  rolling,
			pods: []corev1.Pod{
				testPod("tb-2", "old", crashing(10*time.Minute)...),
				testPod("tb-10", "old", crashing(10*time.Minute)...),
				testPod("tb-1", "old", crashing(10*time.Minute)...),
			},
			want: "tb-10",
		},
		{
			name: "not failing long enough",
			sts:  rolling,
			pods: []corev1.Pod{testPod("tb-0", "old", crashing(time.Minute)...)},
		},
		{
			name: "already on the new revision",
			sts:  rolling,
			pods: []corev1.Pod{testPod("tb-0", "new", crashing(time.Hour)...)},
		},
		{
			name: "unready but not failing, e.g. still recovering",
			sts:  rolling,
			pods: []corev1.Pod{testPod("tb-0", "old", unreadyFor(time.Hour))},
		},
		{
			name: "ready pods are never deleted",
			sts:  rolling,
			pods: []corev1.Pod{testPod("tb-0", "old", ready, waiting("statsd-exporter", "CrashLoopBackOff"))},
		},
		{
			name: "one at a time",
			sts:  rolling,
			pods: []corev1.Pod{
				testPod("tb-0", "old", append(crashing(time.Hour), terminating)...),
				testPod("tb-1", "old", crashing(time.Hour)...),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stuckPod(&tt.sts, tt.pods, testNow)
			gotName := ""
			if got != nil {
				gotName = got.Name
			}
			if gotName != tt.want {
				t.Errorf("got %q, want %q", gotName, tt.want)
			}
		})
	}
}

func TestApprovedPodUIDs(t *testing.T) {
	tbc := tbv1.TigerBeetleCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		tbv1.AnnotationApproveRecovery: " uid-1, ,uid-2 ",
	}}}
	want := map[types.UID]bool{"uid-1": true, "uid-2": true}
	if got := approvedPodUIDs(&tbc); !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := approvedPodUIDs(&tbv1.TigerBeetleCluster{}); len(got) != 0 {
		t.Errorf("no annotation: got %v", got)
	}
}

func TestNeedsAction(t *testing.T) {
	tbc := tbv1.TigerBeetleCluster{ObjectMeta: metav1.ObjectMeta{Name: "tb", Namespace: "ns"}}
	recent := replicaIssue{ordinal: 1, pod: "tb-1", message: "container tigerbeetle CrashLoopBackOff", since: testNow.Add(-time.Minute)}
	old := recent
	old.since = testNow.Add(-time.Hour)

	tests := []struct {
		name       string
		obs        observation
		wantReason string
		wantInMsg  string
	}{
		{name: "healthy"},
		{name: "failing briefly", obs: observation{failing: []replicaIssue{recent}}},
		{name: "failing for long", obs: observation{failing: []replicaIssue{old}}, wantReason: ReasonReplicaFailing, wantInMsg: "tb-1"},
		{
			name: "awaiting approval names the command",
			obs: observation{awaitingApproval: []corev1.Pod{
				testPod("tb-1", "a"), testPod("tb-2", "a"),
			}},
			wantReason: ReasonRecoveryApprovalRequired,
			wantInMsg:  tbv1.AnnotationApproveRecovery + "=tb-1-uid,tb-2-uid",
		},
		{name: "data lost", obs: observation{dataLost: true}, wantReason: ReasonReplicaDataLost, wantInMsg: "data-tb-0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.obs.needsAction(&tbc, testNow)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("unexpected stall: %v", err)
				}
				return
			}
			se, ok := err.(*stalledError)
			if !ok || se.reason != tt.wantReason || !strings.Contains(se.message, tt.wantInMsg) {
				t.Errorf("got %v, want reason %s containing %q", err, tt.wantReason, tt.wantInMsg)
			}
		})
	}
}
