// Pipelines for the TigerBeetle operator.
//
// Every check runs the same way on a laptop and in CI: `dagger check` runs them all,
// `dagger call <name>` runs one. The e2e check creates a throwaway kind cluster inside
// Docker-in-Docker, so it needs nothing on the host but Dagger and leaves nothing behind.
package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/tigerbeetle-operator/internal/dagger"
)

const chartPath = "helm/tigerbeetle-operator"

// Versions of the tools the pipelines use. Keep them in line with flake.nix and the Makefile.
const (
	goImage        = "golang:1.26"
	dockerCLIImage = "docker:29-cli"
	dindImage      = "docker:29-dind"
	kindVersion    = "v0.31.0"
	kubectlVersion = "v1.35.0"
	helmVersion    = "v4.2.2"
)

type TigerbeetleOperator struct {
	// +private
	Source *dagger.Directory
}

func New(
	// Repository root.
	// +defaultPath="/"
	// +ignore=[".git", ".direnv", ".dagger", "bin", "dist", "cover.out", "tilt_config.json"]
	source *dagger.Directory,
) *TigerbeetleOperator {
	return &TigerbeetleOperator{Source: source}
}

// Lint runs golangci-lint and lints the Helm chart.
//
// +check
func (m *TigerbeetleOperator) Lint(ctx context.Context) error {
	tools, err := m.tools(ctx)
	if err != nil {
		return err
	}
	_, err = m.goContainer("lint").
		WithMountedCache("/root/.cache/golangci-lint", dag.CacheVolume("golangci-lint")).
		WithFile("/usr/local/bin/helm", tools.helm).
		WithExec([]string{"make", "lint"}).
		WithExec([]string{"helm", "lint", chartPath}).
		// Rendering catches template errors that `helm lint` alone does not.
		WithExec([]string{"sh", "-c", "helm template tb " + chartPath + " > /dev/null"}).
		WithExec([]string{"sh", "-c", "helm template tb " + chartPath +
			" --set metrics.serviceMonitor.enabled=true --set crds.install=false" +
			" --set leaderElection=false --set metrics.enabled=false > /dev/null"}).
		Sync(ctx)
	return err
}

// Test runs the unit and envtest suites.
//
// +check
func (m *TigerbeetleOperator) Test(ctx context.Context) error {
	_, err := m.goContainer("test").
		WithExec([]string{"make", "test"}).
		Sync(ctx)
	return err
}

// goContainer is a Go toolchain with the source and shared module and build caches. Tools the
// Makefile installs into bin/ are cached per pipeline, so parallel checks never race on them.
func (m *TigerbeetleOperator) goContainer(pipeline string) *dagger.Container {
	return dag.Container().From(goImage).
		WithMountedCache("/go/pkg/mod", dag.CacheVolume("go-mod")).
		WithMountedCache("/root/.cache/go-build", dag.CacheVolume("go-build")).
		WithDirectory("/src", m.Source).
		WithWorkdir("/src").
		WithMountedCache("/src/bin", dag.CacheVolume("tigerbeetle-operator-bin-"+pipeline))
}

// toolset holds the binaries downloaded for the engine's platform.
type toolset struct {
	docker  *dagger.File
	kind    *dagger.File
	kubectl *dagger.File
	helm    *dagger.File
}

func (m *TigerbeetleOperator) tools(ctx context.Context) (toolset, error) {
	platform, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return toolset{}, err
	}
	_, arch, ok := strings.Cut(string(platform), "/")
	if !ok {
		return toolset{}, fmt.Errorf("unexpected engine platform %q", platform)
	}
	arch, _, _ = strings.Cut(arch, "/") // drop a variant such as arm64/v8

	helmArchive := dag.HTTP(fmt.Sprintf("https://get.helm.sh/helm-%s-linux-%s.tar.gz", helmVersion, arch))
	return toolset{
		docker: dag.Container().From(dockerCLIImage).File("/usr/local/bin/docker"),
		kind: dag.HTTP(fmt.Sprintf("https://github.com/kubernetes-sigs/kind/releases/download/%s/kind-linux-%s",
			kindVersion, arch)),
		kubectl: dag.HTTP(fmt.Sprintf("https://dl.k8s.io/release/%s/bin/linux/%s/kubectl", kubectlVersion, arch)),
		helm: dag.Container().From(goImage).
			WithFile("/tmp/helm.tar.gz", helmArchive).
			WithExec([]string{"tar", "-xzf", "/tmp/helm.tar.gz", "-C", "/tmp"}).
			File(fmt.Sprintf("/tmp/linux-%s/helm", arch)),
	}, nil
}
