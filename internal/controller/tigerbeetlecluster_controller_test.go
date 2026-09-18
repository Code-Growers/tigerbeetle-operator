package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tbv1 "github.com/Code-Growers/tigerbeetle-operator/api/v1alpha1"
)

// These tests cover the bootstrap safety gate: formatting a replica after the cluster could
// have committed data loses data, so it must only ever happen during a fresh bootstrap.
var _ = Describe("Bootstrap safety gate", func() {
	var (
		reconciler TigerBeetleClusterReconciler
		tbc        tbv1.TigerBeetleCluster
	)

	newCluster := func(name string, replicas int32) tbv1.TigerBeetleCluster {
		return tbv1.TigerBeetleCluster{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: tbv1.TigerBeetleClusterSpec{
				ClusterID:   "0",
				Development: true,
				Replicas:    replicas,
				Image:       "ghcr.io/tigerbeetle/tigerbeetle:0.17.8",
				Port:        3000,
				Storage:     tbv1.StorageSpec{Size: resource.MustParse("1Gi")},
			},
		}
	}

	key := func() client.ObjectKey { return client.ObjectKeyFromObject(&tbc) }

	reconcileOnce := func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key()})
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
	}

	statefulSetExists := func() bool {
		var sts appsv1.StatefulSet
		err := k8sClient.Get(ctx, key(), &sts)
		if err != nil {
			ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue())
		}
		return err == nil
	}

	formatJobs := func() []batchv1.Job {
		var jobs batchv1.JobList
		ExpectWithOffset(1, k8sClient.List(ctx, &jobs, client.InNamespace(tbc.Namespace),
			client.MatchingLabels(selectorLabels(&tbc, componentFormat)))).To(Succeed())
		return jobs.Items
	}

	markJobSucceeded := func(job *batchv1.Job) {
		now := metav1.NewTime(time.Now())
		job.Status.StartTime = &now
		job.Status.CompletionTime = &now
		job.Status.Succeeded = 1
		job.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
		}
		ExpectWithOffset(1, k8sClient.Status().Update(ctx, job)).To(Succeed())
	}

	BeforeEach(func() {
		reconciler = TigerBeetleClusterReconciler{
			Client:    k8sClient,
			Scheme:    k8sClient.Scheme(),
			APIReader: k8sClient,
			Recorder:  record.NewFakeRecorder(100),
			InitImage: "tigerbeetle-operator-init:test",
		}
	})

	It("creates the StatefulSet only after every format Job has succeeded", func() {
		tbc = newCluster("gate-fresh", 3)
		Expect(k8sClient.Create(ctx, &tbc)).To(Succeed())

		// Services get ClusterIPs from the API server; reconcile until bootstrap starts.
		Eventually(func() []batchv1.Job {
			reconcileOnce()
			return formatJobs()
		}).Should(HaveLen(3))

		for i := range 3 {
			var pvc corev1.PersistentVolumeClaim
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: pvcName(&tbc, i)}, &pvc)).To(Succeed())
			Expect(pvc.Labels).To(HaveKeyWithValue(labelBootstrapUID, string(tbc.UID)))
		}
		Expect(statefulSetExists()).To(BeFalse())

		jobs := formatJobs()
		markJobSucceeded(&jobs[0])
		markJobSucceeded(&jobs[1])
		reconcileOnce()
		Expect(statefulSetExists()).To(BeFalse(), "StatefulSet created before all replicas were formatted")

		markJobSucceeded(&jobs[2])
		reconcileOnce()
		Expect(statefulSetExists()).To(BeTrue())

		Expect(k8sClient.Get(ctx, key(), &tbc)).To(Succeed())
		Expect(tbc.Status.Bootstrapped).To(BeTrue())
		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: configMapName(&tbc)}, &cm)).To(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue(configMapKeyBootstrapped, "true"))

		By("never formatting again once bootstrapped")
		reconcileOnce()
		for _, job := range formatJobs() {
			Expect(job.DeletionTimestamp).NotTo(BeNil(), "format Job %s still exists after bootstrap", job.Name)
		}
	})

	It("does not format when data PVCs exist without a bootstrap marker", func() {
		tbc = newCluster("gate-restored", 1)
		// A PVC left behind by an earlier cluster with the same name.
		leftover := corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data-gate-restored-0", Namespace: "default"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &leftover)).To(Succeed())
		Expect(k8sClient.Create(ctx, &tbc)).To(Succeed())

		Eventually(func() *metav1.Condition {
			reconcileOnce()
			Expect(k8sClient.Get(ctx, key(), &tbc)).To(Succeed())
			return meta.FindStatusCondition(tbc.Status.Conditions, ConditionStalled)
		}).Should(And(
			HaveField("Status", metav1.ConditionTrue),
			HaveField("Reason", ReasonBootstrapConfirmationRequired),
		))

		Expect(formatJobs()).To(BeEmpty())
		Expect(statefulSetExists()).To(BeFalse())
		Expect(tbc.Status.Bootstrapped).To(BeFalse())
	})

	It("starts on existing data PVCs without formatting when told to adopt them", func() {
		tbc = newCluster("gate-adopt", 2)
		tbc.Annotations = map[string]string{tbv1.AnnotationAdoptExistingData: "true"}
		leftover := func(name string) corev1.PersistentVolumeClaim {
			return corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			}
		}
		// Only one replica's PVC exists: adopting needs all of them.
		first := leftover("data-gate-adopt-0")
		Expect(k8sClient.Create(ctx, &first)).To(Succeed())
		Expect(k8sClient.Create(ctx, &tbc)).To(Succeed())

		Eventually(func() *metav1.Condition {
			reconcileOnce()
			Expect(k8sClient.Get(ctx, key(), &tbc)).To(Succeed())
			return meta.FindStatusCondition(tbc.Status.Conditions, ConditionStalled)
		}).Should(And(
			HaveField("Status", metav1.ConditionTrue),
			HaveField("Reason", ReasonBootstrapConfirmationRequired),
			HaveField("Message", ContainSubstring("data-gate-adopt-1 are missing")),
		))
		Expect(statefulSetExists()).To(BeFalse())

		second := leftover("data-gate-adopt-1")
		Expect(k8sClient.Create(ctx, &second)).To(Succeed())
		reconcileOnce()

		Expect(statefulSetExists()).To(BeTrue())
		Expect(formatJobs()).To(BeEmpty())
		Expect(k8sClient.Get(ctx, key(), &tbc)).To(Succeed())
		Expect(tbc.Status.Bootstrapped).To(BeTrue())
		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: configMapName(&tbc)}, &cm)).To(Succeed())
		Expect(cm.Data).To(HaveKeyWithValue(configMapKeyBootstrapped, "true"))
	})

	It("does not format when replica pods are still running without their StatefulSet", func() {
		tbc = newCluster("gate-orphaned", 1)
		// A replica pod orphaned by `kubectl delete statefulset --cascade=orphan`.
		orphan := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName(&tbc, 0),
				Namespace: "default",
				Labels:    labels(&tbc, componentReplica),
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "tigerbeetle", Image: tbc.Spec.Image}}},
		}
		Expect(k8sClient.Create(ctx, &orphan)).To(Succeed())
		Expect(k8sClient.Create(ctx, &tbc)).To(Succeed())

		Eventually(func() bool {
			reconcileOnce()
			return statefulSetExists()
		}).Should(BeTrue())
		Expect(formatJobs()).To(BeEmpty())
	})
})
