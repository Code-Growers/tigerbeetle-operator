package controller

import (
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	tbv1 "github.com/Code-Growers/tigerbeetle-operator/api/v1alpha1"
	"github.com/Code-Growers/tigerbeetle-operator/internal/tigerbeetle"
)

const (
	labelName      = "app.kubernetes.io/name"
	labelInstance  = "app.kubernetes.io/instance"
	labelComponent = "app.kubernetes.io/component"
	labelManagedBy = "app.kubernetes.io/managed-by"

	// labelBootstrapUID marks PVCs created by the bootstrap of a specific TigerBeetleCluster
	// (by UID). PVCs without it, or with another UID, are never formatted.
	labelBootstrapUID = "tigerbeetle.codegrowers.com/bootstrap-uid"

	componentReplica = "replica"
	componentFormat  = "format"

	configMapKeyBootstrapped = "bootstrapped"

	dataVolume    = "data"
	helperVolume  = "tb-helper"
	helperDir     = "/tb-helper"
	helperPath    = helperDir + "/tigerbeetle-operator-init"
	podInfoVolume = "podinfo"
	portName      = "tigerbeetle"

	containerTigerBeetle = "tigerbeetle"
	containerExporter    = "statsd-exporter"
	containerRecover     = "recover"
	metricsPortName      = "metrics"

	// Used when spec.metrics was set without API server defaulting (e.g. by a Go client).
	defaultExporterImage = "prom/statsd-exporter:v0.31.0"
	defaultMetricsPort   = 9102
)

func labels(tbc *tbv1.TigerBeetleCluster, component string) map[string]string {
	return map[string]string{
		labelName:      "tigerbeetle",
		labelInstance:  tbc.Name,
		labelComponent: component,
		labelManagedBy: "tigerbeetle-operator",
	}
}

func selectorLabels(tbc *tbv1.TigerBeetleCluster, component string) map[string]string {
	return map[string]string{
		labelName:      "tigerbeetle",
		labelInstance:  tbc.Name,
		labelComponent: component,
	}
}

func ownerRef(tbc *tbv1.TigerBeetleCluster) metav1.OwnerReference {
	return *metav1.NewControllerRef(tbc, tbv1.GroupVersion.WithKind("TigerBeetleCluster"))
}

func objectMeta(tbc *tbv1.TigerBeetleCluster, name, component string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:            name,
		Namespace:       tbc.Namespace,
		Labels:          labels(tbc, component),
		OwnerReferences: []metav1.OwnerReference{ownerRef(tbc)},
	}
}

func serviceName(tbc *tbv1.TigerBeetleCluster, ordinal int) string {
	return fmt.Sprintf("%s-%d", tbc.Name, ordinal)
}

func podName(tbc *tbv1.TigerBeetleCluster, ordinal int) string {
	return fmt.Sprintf("%s-%d", tbc.Name, ordinal)
}

func metricsServiceName(tbc *tbv1.TigerBeetleCluster) string {
	return tbc.Name + "-metrics"
}

func configMapName(tbc *tbv1.TigerBeetleCluster) string {
	return tbc.Name + "-config"
}

func formatJobName(tbc *tbv1.TigerBeetleCluster, ordinal int) string {
	return fmt.Sprintf("%s-format-%d", tbc.Name, ordinal)
}

// pvcName is the name the StatefulSet volumeClaimTemplate "data" uses for an ordinal.
func pvcName(tbc *tbv1.TigerBeetleCluster, ordinal int) string {
	return fmt.Sprintf("%s-%s-%d", dataVolume, tbc.Name, ordinal)
}

// replicaCount is the total TigerBeetle replica count, including external replicas.
func replicaCount(tbc *tbv1.TigerBeetleCluster) int {
	return int(tbc.Spec.Replicas) + len(tbc.Spec.ExternalAddresses)
}

// buildService builds the per-replica ClusterIP Service. clusterIP pins the address when known.
func buildService(tbc *tbv1.TigerBeetleCluster, ordinal int, clusterIP string) corev1.Service {
	return corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(tbc, serviceName(tbc, ordinal), componentReplica),
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: clusterIP,
			Selector:  map[string]string{appsv1.StatefulSetPodNameLabel: podName(tbc, ordinal)},
			// Replicas must reach each other before any of them can become ready.
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{{
				Name:       portName,
				Protocol:   corev1.ProtocolTCP,
				Port:       tbc.Spec.Port,
				TargetPort: intstr.FromInt32(tbc.Spec.Port),
			}},
		},
	}
}

func buildConfigMap(tbc *tbv1.TigerBeetleCluster, addresses string, bootstrapped bool) corev1.ConfigMap {
	return corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: objectMeta(tbc, configMapName(tbc), componentReplica),
		Data: map[string]string{
			"addresses":              addresses,
			"clusterID":              tbc.Spec.ClusterID,
			"replicaCount":           strconv.Itoa(replicaCount(tbc)),
			"port":                   strconv.Itoa(int(tbc.Spec.Port)),
			configMapKeyBootstrapped: strconv.FormatBool(bootstrapped),
		},
	}
}

// buildPodDisruptionBudget keeps voluntary disruptions (node drains, evictions) from taking
// out more than one replica at a time. TigerBeetle tolerates a minority being down, but two
// replicas of a three-replica cluster losing quorum stops the cluster.
func buildPodDisruptionBudget(tbc *tbv1.TigerBeetleCluster) policyv1.PodDisruptionBudget {
	maxUnavailable := intstr.FromInt32(1)
	return policyv1.PodDisruptionBudget{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
		ObjectMeta: objectMeta(tbc, tbc.Name, componentReplica),
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &maxUnavailable,
			Selector:       &metav1.LabelSelector{MatchLabels: selectorLabels(tbc, componentReplica)},
		},
	}
}

func pvcSpec(tbc *tbv1.TigerBeetleCluster) corev1.PersistentVolumeClaimSpec {
	accessMode := tbc.Spec.Storage.AccessMode
	if accessMode == "" {
		accessMode = corev1.ReadWriteOnce
	}
	return corev1.PersistentVolumeClaimSpec{
		AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
		StorageClassName: tbc.Spec.Storage.StorageClassName,
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: tbc.Spec.Storage.Size},
		},
	}
}

// buildBootstrapPVC builds the PVC the StatefulSet will later adopt. It has no owner
// reference, so deleting the TigerBeetleCluster never deletes data.
func buildBootstrapPVC(tbc *tbv1.TigerBeetleCluster, ordinal int) corev1.PersistentVolumeClaim {
	l := labels(tbc, componentReplica)
	l[labelBootstrapUID] = string(tbc.UID)
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pvcName(tbc, ordinal), Namespace: tbc.Namespace, Labels: l},
		Spec:       pvcSpec(tbc),
	}
}

// containerResources forces the memory request to equal the limit: TigerBeetle allocates
// its memory statically at startup.
func containerResources(tbc *tbv1.TigerBeetleCluster) corev1.ResourceRequirements {
	res := *tbc.Spec.Resources.DeepCopy()
	limit, hasLimit := res.Limits[corev1.ResourceMemory]
	request, hasRequest := res.Requests[corev1.ResourceMemory]
	switch {
	case hasLimit:
		if res.Requests == nil {
			res.Requests = corev1.ResourceList{}
		}
		res.Requests[corev1.ResourceMemory] = limit
	case hasRequest:
		if res.Limits == nil {
			res.Limits = corev1.ResourceList{}
		}
		res.Limits[corev1.ResourceMemory] = request
	}
	return res
}

func memoryLimit(tbc *tbv1.TigerBeetleCluster) *resource.Quantity {
	res := containerResources(tbc)
	if q, ok := res.Limits[corev1.ResourceMemory]; ok {
		return &q
	}
	return nil
}

// podSpecInput is everything the pod builders need that is not in the spec.
type podSpecInput struct {
	initImage string
	addresses string
	cacheGrid string
}

func tigerbeetleEnv(tbc *tbv1.TigerBeetleCluster, in podSpecInput) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: tigerbeetle.EnvClusterID, Value: tbc.Spec.ClusterID},
		{Name: tigerbeetle.EnvReplicaCount, Value: strconv.Itoa(replicaCount(tbc))},
		{Name: tigerbeetle.EnvAddresses, Value: in.addresses},
		{Name: tigerbeetle.EnvPort, Value: strconv.Itoa(int(tbc.Spec.Port))},
		{Name: tigerbeetle.EnvDataDir, Value: tigerbeetle.DefaultDataDir},
		{Name: tigerbeetle.EnvDevelopment, Value: strconv.FormatBool(tbc.Spec.Development)},
		{Name: tigerbeetle.EnvCacheGrid, Value: in.cacheGrid},
		{Name: tigerbeetle.EnvRecoveryPolicy, Value: string(tbc.Spec.RecoveryPolicy)},
	}
	if tbc.Spec.Metrics.Enabled {
		env = append(env, corev1.EnvVar{Name: tigerbeetle.EnvStatsD, Value: tigerbeetle.StatsDAddress})
	}
	return env
}

func tigerbeetleSecurityContext() *corev1.SecurityContext {
	sc := corev1.SecurityContext{
		Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"IPC_LOCK"}},
	}
	return &sc
}

func volumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: dataVolume, MountPath: tigerbeetle.DefaultDataDir},
		{Name: helperVolume, MountPath: helperDir},
	}
}

func copyHelperContainer(initImage string) corev1.Container {
	return corev1.Container{
		Name:            "copy-helper",
		Image:           initImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/init", "copy", helperPath},
		VolumeMounts:    []corev1.VolumeMount{{Name: helperVolume, MountPath: helperDir}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsNonRoot:             ptr.To(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

func podAffinity(tbc *tbv1.TigerBeetleCluster, component string) *corev1.Affinity {
	term := corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{MatchLabels: selectorLabels(tbc, component)},
		TopologyKey:   corev1.LabelHostname,
	}
	var anti corev1.PodAntiAffinity
	if tbc.Spec.PodAntiAffinity == tbv1.PodAntiAffinityPreferred {
		anti.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.WeightedPodAffinityTerm{{
			Weight:          100,
			PodAffinityTerm: term,
		}}
	} else {
		anti.RequiredDuringSchedulingIgnoredDuringExecution = []corev1.PodAffinityTerm{term}
	}
	affinity := corev1.Affinity{PodAntiAffinity: &anti}
	return &affinity
}

func podSecurityContext() *corev1.PodSecurityContext {
	// Container runtimes block io_uring under their default seccomp profiles.
	psc := corev1.PodSecurityContext{
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
	}
	return &psc
}

// buildFormatJob builds the bootstrap Job that formats one replica's data file.
func buildFormatJob(tbc *tbv1.TigerBeetleCluster, ordinal int, in podSpecInput) batchv1.Job {
	env := append(tigerbeetleEnv(tbc, in), corev1.EnvVar{Name: tigerbeetle.EnvReplica, Value: strconv.Itoa(ordinal)})
	meta := objectMeta(tbc, formatJobName(tbc, ordinal), componentFormat)
	return batchv1.Job{
		ObjectMeta: meta,
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To[int32](3),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(tbc, componentFormat)},
				Spec: corev1.PodSpec{
					RestartPolicy:   corev1.RestartPolicyNever,
					SecurityContext: podSecurityContext(),
					// Same spreading as the replicas: with WaitForFirstConsumer volumes, the
					// format pod decides which node each replica's volume lives on.
					Affinity:       podAffinity(tbc, componentFormat),
					InitContainers: []corev1.Container{copyHelperContainer(in.initImage)},
					Containers: []corev1.Container{{
						Name:            "format",
						Image:           tbc.Spec.Image,
						ImagePullPolicy: tbc.Spec.ImagePullPolicy,
						Command:         []string{helperPath, "format"},
						Env:             env,
						Resources:       containerResources(tbc),
						SecurityContext: tigerbeetleSecurityContext(),
						VolumeMounts:    volumeMounts(),
					}},
					Volumes: []corev1.Volume{
						{Name: dataVolume, VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName(tbc, ordinal)},
						}},
						{Name: helperVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
}

// buildStatefulSet builds the replica StatefulSet. The address list is embedded in the pod
// template, so any address change rolls every replica.
func buildStatefulSet(tbc *tbv1.TigerBeetleCluster, in podSpecInput) appsv1.StatefulSet {
	env := tigerbeetleEnv(tbc, in)
	port := tbc.Spec.Port

	containers := []corev1.Container{{
		Name:            containerTigerBeetle,
		Image:           tbc.Spec.Image,
		ImagePullPolicy: tbc.Spec.ImagePullPolicy,
		Command:         []string{helperPath, "start"},
		Env:             env,
		Ports:           []corev1.ContainerPort{{Name: portName, ContainerPort: port, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}},
			PeriodSeconds: 10,
		},
		Resources:       containerResources(tbc),
		SecurityContext: tigerbeetleSecurityContext(),
		VolumeMounts:    volumeMounts(),
	}}
	if tbc.Spec.Metrics.Enabled {
		containers = append(containers, exporterContainer(tbc))
	}
	containers = append(containers, tbc.Spec.Sidecars...)

	return appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: objectMeta(tbc, tbc.Name, componentReplica),
		Spec: appsv1.StatefulSetSpec{
			Replicas:    ptr.To(tbc.Spec.Replicas),
			ServiceName: tbc.Name,
			// A replica serves TCP before it has caught up, so the rolling update waits
			// minReadySeconds after each replica becomes ready.
			MinReadySeconds:     tbc.Spec.MinReadySeconds,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			UpdateStrategy:      appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
				WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
			},
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels(tbc, componentReplica)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(tbc, componentReplica)},
				Spec: corev1.PodSpec{
					SecurityContext: podSecurityContext(),
					Affinity:        podAffinity(tbc, componentReplica),
					InitContainers: []corev1.Container{
						copyHelperContainer(in.initImage),
						{
							Name:            containerRecover,
							Image:           tbc.Spec.Image,
							ImagePullPolicy: tbc.Spec.ImagePullPolicy,
							Command:         []string{helperPath, "recover"},
							Env:             env,
							Resources:       containerResources(tbc),
							SecurityContext: tigerbeetleSecurityContext(),
							// The pod annotations carry the recovery approval (recoveryPolicy: Manual).
							VolumeMounts: append(volumeMounts(),
								corev1.VolumeMount{Name: podInfoVolume, MountPath: tigerbeetle.PodInfoDir, ReadOnly: true}),
						},
					},
					Containers: containers,
					Volumes: []corev1.Volume{
						{Name: helperVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: podInfoVolume, VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
							Items: []corev1.DownwardAPIVolumeFile{{
								Path:     tigerbeetle.PodInfoAnnotationsFile,
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations"},
							}},
						}}},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: dataVolume, Labels: labels(tbc, componentReplica)},
				Spec:       pvcSpec(tbc),
			}},
		},
	}
}

func metricsPort(tbc *tbv1.TigerBeetleCluster) int32 {
	if tbc.Spec.Metrics.Port == 0 {
		return defaultMetricsPort
	}
	return tbc.Spec.Metrics.Port
}

// exporterContainer receives TigerBeetle's StatsD metrics on the pod's loopback interface and
// serves them in Prometheus format.
func exporterContainer(tbc *tbv1.TigerBeetleCluster) corev1.Container {
	image := tbc.Spec.Metrics.ExporterImage
	if image == "" {
		image = defaultExporterImage
	}
	resources := *tbc.Spec.Metrics.Resources.DeepCopy()
	if resources.Requests == nil && resources.Limits == nil {
		resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		}
	}
	port := metricsPort(tbc)
	return corev1.Container{
		Name:  containerExporter,
		Image: image,
		Args: []string{
			"--statsd.listen-udp=" + tigerbeetle.StatsDAddress,
			"--statsd.listen-tcp=",
			"--web.listen-address=:" + strconv.Itoa(int(port)),
			"--log.format=json",
		},
		Ports: []corev1.ContainerPort{{Name: metricsPortName, ContainerPort: port, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/metrics", Port: intstr.FromString(metricsPortName)}},
			PeriodSeconds: 30,
		},
		Resources: resources,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsNonRoot:             ptr.To(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

// buildMetricsService is a headless Service selecting every replica pod, for Prometheus
// service discovery (e.g. a ServiceMonitor).
func buildMetricsService(tbc *tbv1.TigerBeetleCluster) corev1.Service {
	return corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: objectMeta(tbc, metricsServiceName(tbc), componentReplica),
		Spec: corev1.ServiceSpec{
			ClusterIP:                corev1.ClusterIPNone,
			Selector:                 selectorLabels(tbc, componentReplica),
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{{
				Name:       metricsPortName,
				Protocol:   corev1.ProtocolTCP,
				Port:       metricsPort(tbc),
				TargetPort: intstr.FromString(metricsPortName),
			}},
		},
	}
}
