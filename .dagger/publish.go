package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/tigerbeetle-operator/internal/dagger"
)

// releasePlatforms are the architectures every published image is built for. The Dockerfiles
// build on the native platform and cross-compile, so no emulation is involved.
var releasePlatforms = []dagger.Platform{"linux/amd64", "linux/arm64"}

// Publish builds the operator and init helper images for every release platform, packages the
// Helm chart, and pushes all three to a registry.
//
// `version` is the release version, with or without a leading "v"; it becomes the image tag, the
// chart version and the chart appVersion (so a released chart points at its own images by default).
// Credentials are optional, for registries that allow anonymous pushes.
func (m *TigerbeetleOperator) Publish(
	ctx context.Context,
	// Registry and namespace to push to, e.g. "ghcr.io/code-growers".
	registry string,
	// Release version, e.g. "v0.2.0".
	version string,
	// Registry username.
	// +optional
	username string,
	// Registry password or token.
	// +optional
	password *dagger.Secret,
	// Also tag the images "latest".
	// +default=true
	latest bool,
	// Push the Helm chart as an OCI artifact under <registry>/charts.
	// +default=true
	chart bool,
) (string, error) {
	version = strings.TrimPrefix(version, "v")
	registryHost, _, _ := strings.Cut(registry, "/")

	var published []string
	for _, image := range []struct{ name, dockerfile string }{
		{"tigerbeetle-operator", "docker/Dockerfile"},
		{"tigerbeetle-operator-init", "docker/Dockerfile.init"},
	} {
		variants := make([]*dagger.Container, 0, len(releasePlatforms))
		for _, platform := range releasePlatforms {
			variants = append(variants, m.Source.DockerBuild(dagger.DirectoryDockerBuildOpts{
				Dockerfile: image.dockerfile,
				Platform:   platform,
			}))
		}

		tags := []string{version}
		if latest {
			tags = append(tags, "latest")
		}
		for _, tag := range tags {
			ref := fmt.Sprintf("%s/%s:%s", registry, image.name, tag)
			publisher := dag.Container()
			if username != "" && password != nil {
				publisher = publisher.WithRegistryAuth(registryHost, username, password)
			}
			digest, err := publisher.Publish(ctx, ref, dagger.ContainerPublishOpts{PlatformVariants: variants})
			if err != nil {
				return "", fmt.Errorf("publishing %s: %w", ref, err)
			}
			published = append(published, digest)
		}
	}

	if chart {
		ref, err := m.publishChart(ctx, registry, version, username, password)
		if err != nil {
			return "", err
		}
		published = append(published, ref)
	}
	return strings.Join(published, "\n"), nil
}

// publishChart packages the chart at the release version and pushes it as an OCI artifact.
func (m *TigerbeetleOperator) publishChart(
	ctx context.Context, registry, version, username string, password *dagger.Secret,
) (string, error) {
	tools, err := m.tools(ctx)
	if err != nil {
		return "", err
	}
	registryHost, _, _ := strings.Cut(registry, "/")

	helm := dag.Container().From(goImage).
		WithFile("/usr/local/bin/helm", tools.helm).
		WithDirectory("/src", m.Source).
		WithWorkdir("/src").
		WithExec([]string{"helm", "package", chartPath, "--version", version, "--app-version", version,
			"--destination", "/tmp/chart"})
	if username != "" && password != nil {
		helm = helm.WithSecretVariable("REGISTRY_PASSWORD", password).
			WithExec([]string{"sh", "-c",
				"printf %s \"$REGISTRY_PASSWORD\" | helm registry login " + registryHost +
					" --username " + username + " --password-stdin"})
	}

	chartRef := fmt.Sprintf("oci://%s/charts", registry)
	out, err := helm.
		WithExec([]string{"sh", "-c", "helm push /tmp/chart/tigerbeetle-operator-" + version + ".tgz " + chartRef}).
		Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("pushing the chart: %w", err)
	}
	return strings.TrimSpace(out), nil
}
