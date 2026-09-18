package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dagger/tigerbeetle-operator/internal/dagger"
)

const (
	e2eCluster = "e2e"

	// Images built from source for the test. test/e2e reads them from E2E_IMG and E2E_INIT_IMG.
	e2eOperatorImage = "tigerbeetle-operator:e2e"
	e2eInitImage     = "tigerbeetle-operator-init:e2e"
)

// preloadedImages are pulled by Dagger (and cached there) instead of by every kind node.
// They must match the images test/e2e uses; anything missing is simply pulled by the nodes.
var preloadedImages = []string{
	"ghcr.io/tigerbeetle/tigerbeetle:0.17.8",
	"registry.k8s.io/pause:3.10",
	"prom/statsd-exporter:v0.31.0",
}

// loadImageScript loads an image tarball into Docker, tags it, and copies it into the given kind
// nodes. Loading into the control plane too would waste disk: kind taints it when the cluster has
// workers, so no replica pod ever runs there.
const loadImageScript = `#!/bin/sh
set -eu
out=$(docker load -i "$1")
ref=$(echo "$out" | sed -n 's/^Loaded image\( ID\)\{0,1\}: //p' | tail -n 1)
docker tag "$ref" "$2"
kind load docker-image "$2" --name "$3" --nodes "$4"
`

// EndToEnd runs test/e2e against a fresh kind cluster.
//
// The cluster runs in a Docker-in-Docker service: a control plane plus `workers` nodes. The
// operator and init images are built from source and loaded into the nodes, then the suite
// installs the Helm chart and exercises a TigerBeetleCluster. The Docker daemon, and the
// cluster with it, stop when the check ends, whether it passed or not.
//
// +check
func (m *TigerbeetleOperator) EndToEnd(
	ctx context.Context,
	// Worker nodes. The suite expects one node per replica (three) for required anti-affinity.
	// +default=3
	workers int,
) error {
	tools, err := m.tools(ctx)
	if err != nil {
		return err
	}

	docker, err := dockerd().Start(ctx)
	if err != nil {
		return fmt.Errorf("starting Docker-in-Docker: %w", err)
	}
	defer func() { _, _ = docker.Stop(context.WithoutCancel(ctx)) }()

	type image struct {
		ref     string
		tarball *dagger.File
	}
	images := []image{
		{e2eOperatorImage, m.Source.DockerBuild(dagger.DirectoryDockerBuildOpts{Dockerfile: "docker/Dockerfile"}).AsTarball()},
		{e2eInitImage, m.Source.DockerBuild(dagger.DirectoryDockerBuildOpts{Dockerfile: "docker/Dockerfile.init"}).AsTarball()},
	}
	for _, ref := range preloadedImages {
		images = append(images, image{ref, dag.Container().From(ref).AsTarball()})
	}

	runner := m.goContainer("e2e").
		WithFile("/usr/local/bin/docker", tools.docker).
		WithFile("/usr/local/bin/kind", tools.kind, dagger.ContainerWithFileOpts{Permissions: 0o755}).
		WithFile("/usr/local/bin/kubectl", tools.kubectl, dagger.ContainerWithFileOpts{Permissions: 0o755}).
		WithFile("/usr/local/bin/helm", tools.helm).
		WithNewFile("/usr/local/bin/load-image", loadImageScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithNewFile("/e2e/kind.yaml", kindConfig(workers)).
		WithServiceBinding("docker", docker).
		WithEnvVariable("DOCKER_HOST", "tcp://docker:2375").
		WithEnvVariable("KUBECONFIG", "/root/.kube/config").
		// Everything below acts on the live cluster, so it must never come from the cache.
		WithEnvVariable("CACHE_BUSTER", time.Now().String())

	runner = runner.
		// The daemon's storage outlives the run (it keeps the kind node image cached), and so
		// could a cluster left over from an interrupted run.
		WithExec([]string{"sh", "-c", "kind delete cluster --name " + e2eCluster + " && docker image prune --force"}).
		WithExec([]string{"kind", "create", "cluster", "--name", e2eCluster, "--config", "/e2e/kind.yaml", "--wait", "5m"}).
		WithExec([]string{"sh", "-c", "mkdir -p /root/.kube && kind get kubeconfig --name " + e2eCluster +
			" | sed 's#https://0.0.0.0:6443#https://docker:6443#' > /root/.kube/config"}).
		WithExec([]string{"kubectl", "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=5m"})

	for _, img := range images {
		path := "/e2e/images/" + strings.NewReplacer("/", "_", ":", "_").Replace(img.ref) + ".tar"
		runner = runner.
			WithFile(path, img.tarball).
			WithExec([]string{"load-image", path, img.ref, e2eCluster, kindNodes(e2eCluster, workers)})
	}

	_, err = runner.
		WithEnvVariable("E2E_IMG", e2eOperatorImage).
		WithEnvVariable("E2E_INIT_IMG", e2eInitImage).
		WithExec([]string{"go", "test", "./test/e2e/", "-v", "-ginkgo.v", "-timeout", "30m"}).
		Sync(ctx)
	return err
}

// dockerd is a privileged Docker daemon reachable as tcp://docker:2375. kind publishes the API
// server on port 6443 of this service.
func dockerd() *dagger.Service {
	return dag.Container().From(dindImage).
		// Docker's storage cannot sit on the overlay filesystem of the container itself.
		WithMountedCache("/var/lib/docker", dag.CacheVolume("tigerbeetle-operator-e2e-docker"),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModePrivate}).
		WithEnvVariable("DOCKER_TLS_CERTDIR", "").
		WithExposedPort(2375).
		WithExposedPort(6443, dagger.ContainerWithExposedPortOpts{ExperimentalSkipHealthcheck: true}).
		AsService(dagger.ContainerAsServiceOpts{
			// The image entrypoint sets up cgroup v2 nesting, which the kind nodes' systemd needs.
			UseEntrypoint:            true,
			Args:                     []string{"dockerd", "--host=tcp://0.0.0.0:2375", "--tls=false"},
			InsecureRootCapabilities: true,
		}).
		WithHostname("docker")
}

// kindConfig publishes the API server on all interfaces of the Docker daemon and adds "docker"
// to its certificate, so clients in other Dagger containers can reach https://docker:6443.
func kindConfig(workers int) string {
	var b strings.Builder
	b.WriteString(`kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "0.0.0.0"
  apiServerPort: 6443
kubeadmConfigPatches:
  - |
    kind: ClusterConfiguration
    apiServer:
      certSANs: ["docker"]
nodes:
  - role: control-plane
`)
	for range workers {
		b.WriteString("  - role: worker\n")
	}
	return b.String()
}

// kindNodes lists the nodes that run workloads, as kind names them.
func kindNodes(cluster string, workers int) string {
	if workers == 0 {
		return cluster + "-control-plane"
	}
	nodes := make([]string, 0, workers)
	for i := range workers {
		if i == 0 {
			nodes = append(nodes, cluster+"-worker")
			continue
		}
		nodes = append(nodes, fmt.Sprintf("%s-worker%d", cluster, i+1))
	}
	return strings.Join(nodes, ",")
}
