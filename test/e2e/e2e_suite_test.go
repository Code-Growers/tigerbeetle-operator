package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Code-Growers/tigerbeetle-operator/test/utils"
)

const (
	operatorNamespace = "tigerbeetle-operator-system"
	operatorRelease   = "tigerbeetle-operator"
	chartPath         = "./helm/tigerbeetle-operator"

	// tigerbeetleImage is the server image the test cluster runs and the `repl` client uses.
	tigerbeetleImage = "ghcr.io/tigerbeetle/tigerbeetle:0.17.8"
	// sidecarImage is a container that runs forever without doing anything, to check sidecar injection.
	sidecarImage = "registry.k8s.io/pause:3.10"
)

var (
	// The operator and init helper images, built from this tree and already loaded into the cluster.
	operatorImage = envOrDefault("E2E_IMG", "tigerbeetle-operator:e2e")
	initImage     = envOrDefault("E2E_INIT_IMG", "tigerbeetle-operator-init:e2e")
)

// TestE2E runs the end-to-end tests against the cluster in KUBECONFIG.
// Run it with `make test-e2e` (`dagger call end-to-end`), which creates a throwaway kind cluster,
// loads the images and tears everything down afterwards.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting tigerbeetle-operator e2e suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("installing the operator Helm chart")
	operatorRepo, operatorTag := splitImage(operatorImage)
	initRepo, initTag := splitImage(initImage)
	_, err := utils.Run(exec.Command("helm", "upgrade", "--install", operatorRelease, chartPath,
		"--namespace", operatorNamespace, "--create-namespace",
		"--set", "image.repository="+operatorRepo,
		"--set", "image.tag="+operatorTag,
		"--set", "initImage.repository="+initRepo,
		"--set", "initImage.tag="+initTag,
		"--wait", "--timeout", "5m",
	))
	Expect(err).NotTo(HaveOccurred(), "failed to install the operator chart")
})

// splitImage splits "repository:tag" into its parts. The chart takes them separately.
func splitImage(image string) (repository, tag string) {
	repository, tag, ok := strings.Cut(image, ":")
	if !ok {
		return image, "latest"
	}
	return repository, tag
}

func envOrDefault(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		return v
	}
	return fallback
}
