package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	tbv1 "github.com/Code-Growers/tigerbeetle-operator/api/v1alpha1"
	"github.com/Code-Growers/tigerbeetle-operator/internal/tigerbeetle"
)

const (
	fieldOwner = "tigerbeetle-operator"

	requeueWaitingForIP = 2 * time.Second
	requeueNotReady     = 15 * time.Second
	requeueStalled      = time.Minute
)

// TigerBeetleClusterReconciler reconciles a TigerBeetleCluster object
type TigerBeetleClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// APIReader reads directly from the API server. The bootstrap safety gate uses it so a
	// stale cache can never make an already bootstrapped cluster look new.
	APIReader client.Reader

	Recorder record.EventRecorder

	// InitImage is the init helper image used by TigerBeetle pods and format Jobs.
	InitImage string
}

// stalledError means progress requires human action. It is reported as Stalled=True
// instead of being retried with backoff.
type stalledError struct {
	reason  string
	message string
}

func (e *stalledError) Error() string { return e.reason + ": " + e.message }

func stalled(reason, format string, args ...any) error {
	return &stalledError{reason: reason, message: fmt.Sprintf(format, args...)}
}

// observation collects what a reconcile found, for computing status conditions.
type observation struct {
	bootstrapped      bool
	bootstrapProgress string
	statefulSetName   string
	rolloutInProgress bool
	allReady          bool
	recovering        []int
	// recoveryDelayed are replicas whose recover step has waited longer than recoveryDelayedAfter.
	recoveryDelayed []int
	// failing are replica pods that cannot run (crash loops, image pulls, scheduling).
	failing []replicaIssue
	// awaitingApproval are pods waiting for the approve-recovery annotation.
	awaitingApproval []corev1.Pod
	// dataLost is set when a single-replica cluster lost its data file, which cannot be recovered.
	dataLost bool
	// adoptedExistingData is set when this reconcile started the cluster on existing PVCs.
	adoptedExistingData bool
}

// +kubebuilder:rbac:groups=tigerbeetle.codegrowers.com,resources=tigerbeetleclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tigerbeetle.codegrowers.com,resources=tigerbeetleclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tigerbeetle.codegrowers.com,resources=tigerbeetleclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=services;configmaps;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete

// Reconcile moves the cluster towards the TigerBeetleCluster spec and always reports the
// outcome through status conditions and events.
func (r *TigerBeetleClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var tbc tbv1.TigerBeetleCluster
	if err := r.Get(ctx, req.NamespacedName, &tbc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !tbc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	orig := tbc.DeepCopy()
	var obs observation
	result, err := r.reconcile(ctx, &tbc, &obs)

	r.setStatus(orig, &tbc, &obs, err)
	if patchErr := r.Status().Patch(ctx, &tbc, client.MergeFrom(orig)); patchErr != nil {
		log.Error(patchErr, "Failed to patch status")
		if err == nil {
			err = patchErr
		}
	}

	var se *stalledError
	if errors.As(err, &se) {
		log.Info("Reconcile stalled", "reason", se.reason, "message", se.message)
		return ctrl.Result{RequeueAfter: requeueStalled}, nil
	}
	return result, err
}

func (r *TigerBeetleClusterReconciler) reconcile(ctx context.Context, tbc *tbv1.TigerBeetleCluster, obs *observation) (ctrl.Result, error) {
	// CEL rejects invalid specs at admission; these checks cover what CEL cannot.
	if err := tigerbeetle.ValidateClusterID(tbc.Spec.ClusterID, tbc.Spec.Development); err != nil {
		return ctrl.Result{}, stalled(ReasonInvalidSpec, "%v", err)
	}
	cacheGrid, err := tigerbeetle.CacheGrid(tbc.Spec.CacheGrid, memoryLimit(tbc), tbc.Spec.Development)
	if err != nil {
		return ctrl.Result{}, stalled(ReasonInvalidSpec, "%v", err)
	}

	ips, err := r.ensureServices(ctx, tbc)
	if err != nil {
		return ctrl.Result{}, err
	}
	for _, ip := range ips {
		if ip == "" {
			return ctrl.Result{RequeueAfter: requeueWaitingForIP}, nil
		}
	}
	addresses := tigerbeetle.Addresses(ips, tbc.Spec.Port, tbc.Spec.ExternalAddresses)
	tbc.Status.ServiceIPs = ips
	tbc.Status.Addresses = addresses
	tbc.Status.ReplicaCount = int32(replicaCount(tbc))

	bootstrapped, err := r.isBootstrapped(ctx, tbc, r.Client)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("isBootstrapped: %w", err)
	}
	if !bootstrapped {
		// Confirm against the API server before doing anything that could format.
		if bootstrapped, err = r.isBootstrapped(ctx, tbc, r.APIReader); err != nil {
			return ctrl.Result{}, fmt.Errorf("isBootstrapped: %w", err)
		}
	}
	if err := r.applyConfigMap(ctx, tbc, addresses, bootstrapped); err != nil {
		return ctrl.Result{}, err
	}

	in := podSpecInput{initImage: r.InitImage, addresses: addresses, cacheGrid: cacheGrid}

	if !bootstrapped {
		done, err := r.bootstrap(ctx, tbc, obs, in)
		if err != nil || !done {
			return ctrl.Result{}, err
		}
		// Persist the marker before the StatefulSet exists.
		if err := r.applyConfigMap(ctx, tbc, addresses, true); err != nil {
			return ctrl.Result{}, err
		}
	}
	tbc.Status.Bootstrapped = true
	obs.bootstrapped = true

	sts, err := r.applyStatefulSet(ctx, tbc, in, !bootstrapped && !obs.adoptedExistingData)
	if err != nil {
		return ctrl.Result{}, err
	}
	obs.statefulSetName = sts.Name

	if err := r.applyPodDisruptionBudget(ctx, tbc); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.applyMetricsService(ctx, tbc); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.deleteFormatJobs(ctx, tbc); err != nil {
		return ctrl.Result{}, err
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(tbc.Namespace),
		client.MatchingLabels(selectorLabels(tbc, componentReplica))); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing replica pods: %w", err)
	}
	if err := r.approveRecoveries(ctx, tbc, pods.Items); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.restartStuckPod(ctx, tbc, &sts, pods.Items); err != nil {
		return ctrl.Result{}, err
	}

	r.observeReplicas(tbc, &sts, pods.Items, obs, time.Now())
	if err := obs.needsAction(tbc, time.Now()); err != nil {
		return ctrl.Result{}, err
	}
	if !obs.allReady {
		return ctrl.Result{RequeueAfter: requeueNotReady}, nil
	}
	return ctrl.Result{}, nil
}

// applyMetricsService creates the metrics Service while metrics are enabled and removes it after.
func (r *TigerBeetleClusterReconciler) applyMetricsService(ctx context.Context, tbc *tbv1.TigerBeetleCluster) error {
	svc := buildMetricsService(tbc)
	if tbc.Spec.Metrics.Enabled {
		if err := r.Patch(ctx, &svc, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
			return fmt.Errorf("applyMetricsService: %w", err)
		}
		return nil
	}

	var existing corev1.Service
	err := r.Get(ctx, client.ObjectKeyFromObject(&svc), &existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("applyMetricsService: %w", err)
	}
	if metav1.IsControlledBy(&existing, tbc) {
		if err := r.Delete(ctx, &existing); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("applyMetricsService: %w", err)
		}
	}
	return nil
}

// approveRecoveries marks waiting pods whose UID is listed in the approve-recovery annotation,
// which lets their recover init container proceed.
func (r *TigerBeetleClusterReconciler) approveRecoveries(ctx context.Context, tbc *tbv1.TigerBeetleCluster, pods []corev1.Pod) error {
	if tbc.Spec.RecoveryPolicy != tbv1.RecoveryPolicyManual || replicaCount(tbc) == 1 {
		return nil
	}
	approved := approvedPodUIDs(tbc)
	for i := range pods {
		pod := &pods[i]
		if !approved[pod.UID] || !initContainerRunning(pod, containerRecover) ||
			recoveryApproved(pod) {
			continue
		}
		patch := client.MergeFrom(pod.DeepCopy())
		metav1.SetMetaDataAnnotation(&pod.ObjectMeta, tigerbeetle.AnnotationRecoveryApproved, annotationTrue)
		if err := r.Patch(ctx, pod, patch); err != nil {
			return fmt.Errorf("approving recovery of pod %s: %w", pod.Name, err)
		}
		r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonRecoveryApproved,
			"Recovery of pod %s (UID %s) approved; it recovers once the kubelet refreshes its annotations", pod.Name, pod.UID)
	}
	return nil
}

// restartStuckPod deletes a failing pod that blocks a rollout (see stuckPod).
func (r *TigerBeetleClusterReconciler) restartStuckPod(ctx context.Context, tbc *tbv1.TigerBeetleCluster, sts *appsv1.StatefulSet, pods []corev1.Pod) error {
	pod := stuckPod(sts, pods, time.Now())
	if pod == nil {
		return nil
	}
	message, _, _ := podFailure(pod)
	uid := pod.UID
	if err := r.Delete(ctx, pod, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting stuck pod %s: %w", pod.Name, err)
	}
	r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonStuckReplicaRestarted,
		"Deleted pod %s so it restarts with the updated spec: it was failing on an outdated revision (%s), "+
			"which blocks the rollout", pod.Name, message)
	return nil
}

// needsAction turns replica states that cannot resolve on their own into a stall.
func (obs *observation) needsAction(tbc *tbv1.TigerBeetleCluster, now time.Time) error {
	if obs.dataLost {
		return stalled(ReasonReplicaDataLost,
			"The data file of the only replica is missing, and a single-replica cluster cannot recover it. "+
				"Restore the volume %s, or delete the TigerBeetleCluster and its PVCs to start a new cluster",
			pvcName(tbc, 0))
	}
	if len(obs.awaitingApproval) > 0 {
		names := make([]string, 0, len(obs.awaitingApproval))
		uids := make([]string, 0, len(obs.awaitingApproval))
		for _, pod := range obs.awaitingApproval {
			names = append(names, pod.Name)
			uids = append(uids, string(pod.UID))
		}
		return stalled(ReasonRecoveryApprovalRequired,
			"Pods %s have no data file and recoveryPolicy is Manual. Recovery rebuilds the data from the other "+
				"replicas, which must be healthy. To approve: kubectl annotate tigerbeetlecluster %s -n %s --overwrite %s=%s",
			strings.Join(names, ", "), tbc.Name, tbc.Namespace, tbv1.AnnotationApproveRecovery, strings.Join(uids, ","))
	}
	for _, issue := range obs.failing {
		if now.Sub(issue.since) >= replicaFailingStallAfter {
			return stalled(ReasonReplicaFailing, "%s", obs.failingMessage())
		}
	}
	return nil
}

func (obs *observation) failingMessage() string {
	parts := make([]string, 0, len(obs.failing))
	for _, issue := range obs.failing {
		parts = append(parts, fmt.Sprintf("replica %d (pod %s): %s", issue.ordinal, issue.pod, issue.message))
	}
	return strings.Join(parts, "; ")
}

// ensureServices applies one ClusterIP Service per replica and returns their IPs by ordinal
// ("" while not yet allocated). A deleted Service is recreated with its previous IP.
func (r *TigerBeetleClusterReconciler) ensureServices(ctx context.Context, tbc *tbv1.TigerBeetleCluster) ([]string, error) {
	ips := make([]string, tbc.Spec.Replicas)
	for i := range ips {
		var existing corev1.Service
		err := r.Get(ctx, client.ObjectKey{Namespace: tbc.Namespace, Name: serviceName(tbc, i)}, &existing)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("ensureServices: %w", err)
		}

		clusterIP, repin := "", false
		if err == nil {
			clusterIP = existing.Spec.ClusterIP
		} else if i < len(tbc.Status.ServiceIPs) && tbc.Status.ServiceIPs[i] != "" {
			clusterIP, repin = tbc.Status.ServiceIPs[i], true
		}

		svc := buildService(tbc, i, clusterIP)
		if err := r.Patch(ctx, &svc, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
			if repin && (apierrors.IsInvalid(err) || apierrors.IsAlreadyExists(err)) {
				return nil, stalled(ReasonServiceIPRepinFailed,
					"Service %s was deleted and cannot be recreated with its previous IP %s: %v. "+
						"Replicas and clients use that IP; restore it or recreate the cluster",
					svc.Name, clusterIP, err)
			}
			return nil, fmt.Errorf("ensureServices: %w", err)
		}
		if repin {
			r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonServiceIPRepinned,
				"Recreated Service %s with its previous IP %s", svc.Name, clusterIP)
		}
		ips[i] = svc.Spec.ClusterIP
	}
	return ips, nil
}

func (r *TigerBeetleClusterReconciler) applyConfigMap(ctx context.Context, tbc *tbv1.TigerBeetleCluster, addresses string, bootstrapped bool) error {
	cm := buildConfigMap(tbc, addresses, bootstrapped)
	if err := r.Patch(ctx, &cm, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
		return fmt.Errorf("applyConfigMap: %w", err)
	}
	return nil
}

// isBootstrapped reports whether any bootstrap marker exists. Once a cluster could have
// committed data, formatting is unsafe, so any single marker is enough.
func (r *TigerBeetleClusterReconciler) isBootstrapped(ctx context.Context, tbc *tbv1.TigerBeetleCluster, reader client.Reader) (bool, error) {
	if tbc.Status.Bootstrapped {
		return true, nil
	}

	var cm corev1.ConfigMap
	err := reader.Get(ctx, client.ObjectKey{Namespace: tbc.Namespace, Name: configMapName(tbc)}, &cm)
	if err == nil && cm.Data[configMapKeyBootstrapped] == "true" {
		return true, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}

	var sts appsv1.StatefulSet
	err = reader.Get(ctx, client.ObjectKey{Namespace: tbc.Namespace, Name: tbc.Name}, &sts)
	if err == nil {
		return true, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, err
	}

	// Replica pods outlive their StatefulSet when it is deleted with --cascade=orphan, and
	// they may be serving a cluster with committed data.
	var pods corev1.PodList
	if err := reader.List(ctx, &pods, client.InNamespace(tbc.Namespace),
		client.MatchingLabels(selectorLabels(tbc, componentReplica)), client.Limit(1)); err != nil {
		return false, err
	}
	return len(pods.Items) > 0, nil
}

// bootstrap creates the data PVCs and one format Job per replica. It returns true once every
// Job has succeeded. It refuses to touch PVCs that this TigerBeetleCluster did not create.
func (r *TigerBeetleClusterReconciler) bootstrap(ctx context.Context, tbc *tbv1.TigerBeetleCluster, obs *observation, in podSpecInput) (bool, error) {
	replicas := int(tbc.Spec.Replicas)

	var foreign, missing []string
	for i := range replicas {
		var pvc corev1.PersistentVolumeClaim
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: tbc.Namespace, Name: pvcName(tbc, i)}, &pvc)
		switch {
		case apierrors.IsNotFound(err):
			missing = append(missing, pvcName(tbc, i))
		case err != nil:
			return false, fmt.Errorf("bootstrap: %w", err)
		case pvc.Labels[labelBootstrapUID] != string(tbc.UID):
			foreign = append(foreign, pvc.Name)
		}
	}
	if len(foreign) > 0 {
		if tbc.Annotations[tbv1.AnnotationAdoptExistingData] != annotationTrue {
			return false, stalled(ReasonBootstrapConfirmationRequired,
				"PVCs %s already exist but were not formatted by this TigerBeetleCluster, so they may hold data; "+
					"refusing to format. To start this cluster on that data (e.g. a restored TigerBeetleCluster), "+
					"annotate it with %s=true. To start a new cluster and discard the data, delete the data PVCs",
				strings.Join(foreign, ", "), tbv1.AnnotationAdoptExistingData)
		}
		if len(missing) > 0 {
			return false, stalled(ReasonBootstrapConfirmationRequired,
				"Cannot adopt existing data: PVCs %s are missing, and adopting needs the data PVC of every replica",
				strings.Join(missing, ", "))
		}
		// TigerBeetle itself refuses to start on data files of another cluster ID or replica count.
		obs.adoptedExistingData = true
		r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonExistingDataAdopted,
			"Starting on the existing PVCs %s without formatting them (%s)",
			strings.Join(foreign, ", "), tbv1.AnnotationAdoptExistingData)
		return true, nil
	}

	for i := range replicas {
		pvc := buildBootstrapPVC(tbc, i)
		if err := r.Create(ctx, &pvc); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, fmt.Errorf("bootstrap: creating PVC: %w", err)
		}
	}

	prev := map[int32]tbv1.ReplicaState{}
	for _, rs := range tbc.Status.ReplicaStatuses {
		prev[rs.Index] = rs.State
	}
	statuses := make([]tbv1.ReplicaStatus, replicas)
	succeeded := 0
	created := false
	failedJob := ""

	for i := range replicas {
		statuses[i] = tbv1.ReplicaStatus{Index: int32(i), State: tbv1.ReplicaStateFormatting}

		var job batchv1.Job
		err := r.Get(ctx, client.ObjectKey{Namespace: tbc.Namespace, Name: formatJobName(tbc, i)}, &job)
		if apierrors.IsNotFound(err) {
			job = buildFormatJob(tbc, i, in)
			if err := r.Create(ctx, &job); err != nil && !apierrors.IsAlreadyExists(err) {
				return false, fmt.Errorf("bootstrap: creating Job: %w", err)
			}
			created = true
			continue
		}
		if err != nil {
			return false, fmt.Errorf("bootstrap: %w", err)
		}

		switch {
		case jobCondition(&job, batchv1.JobComplete):
			succeeded++
			statuses[i].State = tbv1.ReplicaStateFormatted
			if prev[int32(i)] != tbv1.ReplicaStateFormatted {
				r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonReplicaFormatted,
					"Replica %d formatted by Job %s", i, job.Name)
			}
		case jobCondition(&job, batchv1.JobFailed):
			if failedJob == "" {
				failedJob = job.Name
			}
		}
	}
	tbc.Status.ReplicaStatuses = statuses
	obs.bootstrapProgress = fmt.Sprintf("%d/%d replicas formatted", succeeded, replicas)

	if created && len(prev) == 0 {
		r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonBootstrapStarted,
			"Created PVCs and format Jobs for %d replicas", replicas)
	}
	if failedJob != "" {
		return false, stalled(ReasonFormatJobFailed,
			"Format Job %s failed; check its pod logs, fix the cause, then delete the Job to retry", failedJob)
	}
	return succeeded == replicas, nil
}

func jobCondition(job *batchv1.Job, condType batchv1.JobConditionType) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == condType && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *TigerBeetleClusterReconciler) applyStatefulSet(ctx context.Context, tbc *tbv1.TigerBeetleCluster, in podSpecInput, completingBootstrap bool) (appsv1.StatefulSet, error) {
	var existing appsv1.StatefulSet
	err := r.Get(ctx, client.ObjectKey{Namespace: tbc.Namespace, Name: tbc.Name}, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return appsv1.StatefulSet{}, fmt.Errorf("applyStatefulSet: %w", err)
	}
	exists := err == nil

	sts := buildStatefulSet(tbc, in)
	if err := r.Patch(ctx, &sts, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
		return appsv1.StatefulSet{}, fmt.Errorf("applyStatefulSet: %w", err)
	}

	switch {
	case !exists && completingBootstrap:
		r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonBootstrapCompleted,
			"All %d replicas formatted; created StatefulSet %s", tbc.Spec.Replicas, sts.Name)
	case exists && sts.Generation != existing.Generation:
		r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonRolloutStarted,
			"StatefulSet %s pod template changed; rolling replicas", sts.Name)
	}
	return sts, nil
}

// applyPodDisruptionBudget protects the quorum from node drains. A single replica has no
// quorum to protect, and a budget there would only block draining its node.
func (r *TigerBeetleClusterReconciler) applyPodDisruptionBudget(ctx context.Context, tbc *tbv1.TigerBeetleCluster) error {
	if tbc.Spec.Replicas < 2 {
		return nil
	}
	pdb := buildPodDisruptionBudget(tbc)
	if err := r.Patch(ctx, &pdb, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
		return fmt.Errorf("applyPodDisruptionBudget: %w", err)
	}
	return nil
}

func (r *TigerBeetleClusterReconciler) deleteFormatJobs(ctx context.Context, tbc *tbv1.TigerBeetleCluster) error {
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(tbc.Namespace),
		client.MatchingLabels(selectorLabels(tbc, componentFormat))); err != nil {
		return fmt.Errorf("deleteFormatJobs: %w", err)
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !metav1.IsControlledBy(job, tbc) || !job.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleteFormatJobs: %w", err)
		}
	}
	return nil
}

// observeReplicas records per-replica states, ready replicas and rollout progress.
func (r *TigerBeetleClusterReconciler) observeReplicas(tbc *tbv1.TigerBeetleCluster, sts *appsv1.StatefulSet, pods []corev1.Pod, obs *observation, now time.Time) {
	replicas := tbc.Spec.Replicas

	byName := map[string]*corev1.Pod{}
	for i := range pods {
		byName[pods[i].Name] = &pods[i]
	}

	statuses := make([]tbv1.ReplicaStatus, replicas)
	for i := range int(replicas) {
		status := &statuses[i]
		*status = tbv1.ReplicaStatus{Index: int32(i), State: tbv1.ReplicaStatePending}
		pod, ok := byName[podName(tbc, i)]
		if !ok {
			continue
		}
		// A running recover init container means the replica's data file is missing.
		recoverStarted, recovering := initContainerStartedAt(pod, containerRecover)
		switch {
		case podReady(pod):
			status.State = tbv1.ReplicaStateRunning
		case recovering && replicaCount(tbc) == 1:
			status.State = tbv1.ReplicaStateFailing
			status.Message = "Data file is missing; a single-replica cluster cannot recover it"
			obs.dataLost = true
		case recovering && tbc.Spec.RecoveryPolicy == tbv1.RecoveryPolicyManual &&
			!recoveryApproved(pod):
			status.State = tbv1.ReplicaStateAwaitingApproval
			status.Message = fmt.Sprintf("Data file is missing; approve recovery of pod UID %s with the %s annotation",
				pod.UID, tbv1.AnnotationApproveRecovery)
			obs.awaitingApproval = append(obs.awaitingApproval, *pod)
		case recovering:
			status.State = tbv1.ReplicaStateRecovering
			obs.recovering = append(obs.recovering, i)
			if now.Sub(recoverStarted) >= recoveryDelayedAfter {
				status.Message = "Recovery is waiting for the other replicas; they must be running and healthy"
				obs.recoveryDelayed = append(obs.recoveryDelayed, i)
			}
		default:
			if message, since, failing := podFailure(pod); failing {
				status.State = tbv1.ReplicaStateFailing
				status.Message = message
				obs.failing = append(obs.failing, replicaIssue{ordinal: i, pod: pod.Name, message: message, since: since})
			}
		}
	}
	tbc.Status.ReplicaStatuses = statuses
	tbc.Status.ReadyReplicas = sts.Status.ReadyReplicas

	obs.rolloutInProgress = sts.Status.ObservedGeneration < sts.Generation ||
		sts.Status.UpdatedReplicas != replicas ||
		sts.Status.CurrentRevision != sts.Status.UpdateRevision
	obs.allReady = !obs.rolloutInProgress && sts.Status.ReadyReplicas == replicas

	if obs.allReady && tbc.Status.CurrentImage != tbc.Spec.Image {
		if tbc.Status.CurrentImage != "" {
			r.Recorder.Eventf(tbc, corev1.EventTypeNormal, ReasonUpgradeCompleted,
				"All replicas run %s (was %s)", tbc.Spec.Image, tbc.Status.CurrentImage)
		}
		tbc.Status.CurrentImage = tbc.Spec.Image
	}
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func initContainerRunning(pod *corev1.Pod, name string) bool {
	for _, s := range pod.Status.InitContainerStatuses {
		if s.Name == name {
			return s.State.Running != nil
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *TigerBeetleClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&tbv1.TigerBeetleCluster{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&batchv1.Job{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Named("tigerbeetlecluster").
		Complete(r)
}
