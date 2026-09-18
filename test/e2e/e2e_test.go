package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Code-Growers/tigerbeetle-operator/test/utils"
)

const (
	clusterNamespace = "tigerbeetle-e2e"
	clusterName      = "tb"
	replicas         = 3
)

// clusterManifest is a three-replica cluster with metrics and one sidecar, as a real deployment
// would run a VPN or mesh sidecar next to TigerBeetle.
var clusterManifest = fmt.Sprintf(`
apiVersion: tigerbeetle.codegrowers.com/v1alpha1
kind: TigerBeetleCluster
metadata:
  name: %s
  namespace: %s
spec:
  clusterID: "0"
  replicas: %d
  development: true
  image: %s
  minReadySeconds: 5
  metrics:
    enabled: true
  storage:
    size: 1Gi
  resources:
    requests:
      cpu: 100m
    limits:
      # A development replica is OOMKilled at 1Gi: the journal alone maps 1GiB.
      memory: 2Gi
  sidecars:
    - name: sleeper
      image: %s
`, clusterName, clusterNamespace, replicas, tigerbeetleImage, sidecarImage)

var _ = Describe("TigerBeetleCluster", Ordered, func() {
	// failed keeps the namespace around after a failing spec.
	var failed bool

	BeforeAll(func() {
		_, err := kubectl("create", "namespace", clusterNamespace)
		Expect(err).NotTo(HaveOccurred())

		By("creating the TigerBeetleCluster")
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(clusterManifest)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		// On failure the namespace is left behind for inspection; `make test-e2e` deletes
		// the whole cluster once the suite passes.
		if !failed {
			_, _ = kubectl("delete", "namespace", clusterNamespace, "--wait=false")
		}
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		failed = true
		for _, args := range [][]string{
			{"get", "tigerbeetlecluster", clusterName, "-n", clusterNamespace, "-o", "yaml"},
			{"get", "pods", "-n", clusterNamespace, "-o", "wide"},
			{"describe", "pods", "-n", clusterNamespace, "-l", "app.kubernetes.io/name=tigerbeetle"},
			{"logs", "-n", clusterNamespace, "-l", "app.kubernetes.io/component=replica",
				"-c", "tigerbeetle", "--tail=50", "--prefix"},
			{"get", "events", "-n", clusterNamespace, "--sort-by=.lastTimestamp"},
			{"logs", "-n", operatorNamespace, "-l", "control-plane=controller-manager", "--tail=100"},
		} {
			out, err := kubectl(args...)
			_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %s:\n%s\n%v\n", strings.Join(args, " "), out, err)
		}
	})

	It("bootstraps every replica and becomes Ready", func() {
		Eventually(func(g Gomega) {
			g.Expect(clusterField(g, ".status.phase")).To(Equal("Ready"))
			g.Expect(clusterField(g, ".status.readyReplicas")).To(Equal(fmt.Sprint(replicas)))
			g.Expect(clusterField(g, ".status.bootstrapped")).To(Equal("true"))
		}).WithTimeout(10 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("deleting the format Jobs once bootstrapped")
		out, err := kubectl("get", "jobs", "-n", clusterNamespace, "-o", "name")
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(out)).To(BeEmpty())
	})

	It("spreads the replicas across nodes and injects the sidecar", func() {
		out, err := kubectl("get", "pods", "-n", clusterNamespace,
			"-l", "app.kubernetes.io/instance="+clusterName+",app.kubernetes.io/component=replica",
			"-o", "jsonpath={range .items[*]}{.spec.nodeName}{\"\\n\"}{end}")
		Expect(err).NotTo(HaveOccurred())
		nodes := utils.GetNonEmptyLines(out)
		Expect(nodes).To(HaveLen(replicas))
		Expect(nodes).To(HaveEach(Not(BeEmpty())))
		Expect(unique(nodes)).To(HaveLen(replicas), "replicas share a node despite Required anti-affinity")

		out, err = kubectl("get", "pod", clusterName+"-0", "-n", clusterNamespace,
			"-o", "jsonpath={.spec.containers[*].name}")
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Fields(out)).To(ConsistOf("tigerbeetle", "statsd-exporter", "sleeper"))
	})

	It("serves TigerBeetle metrics in Prometheus format", func() {
		Eventually(func(g Gomega) string {
			out, err := kubectl("get", "--raw",
				"/api/v1/namespaces/"+clusterNamespace+"/pods/"+clusterName+"-0:9102/proxy/metrics")
			g.Expect(err).NotTo(HaveOccurred())
			return out
		}).WithTimeout(2 * time.Minute).Should(MatchRegexp(`(?m)^tb_replica_status\{.*replica="0"\} 0$`))

		out, err := kubectl("get", "service", clusterName+"-metrics", "-n", clusterNamespace,
			"-o", "jsonpath={.spec.clusterIP}")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("None"))
	})

	It("protects the quorum with a PodDisruptionBudget", func() {
		out, err := kubectl("get", "poddisruptionbudget", clusterName, "-n", clusterNamespace,
			"-o", "jsonpath={.spec.maxUnavailable}")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("1"))

		Eventually(func(g Gomega) string {
			out, err := kubectl("get", "poddisruptionbudget", clusterName, "-n", clusterNamespace,
				"-o", "jsonpath={.status.currentHealthy}")
			g.Expect(err).NotTo(HaveOccurred())
			return out
		}).Should(Equal(fmt.Sprint(replicas)))
	})

	It("serves client requests on the advertised addresses", func() {
		addresses := clusterField(Default, ".status.addresses")
		Expect(strings.Split(addresses, ",")).To(HaveLen(replicas))

		By("creating an account with tigerbeetle repl")
		out, err := repl("tb-repl-create", addresses, "create_accounts id=1 code=10 ledger=700")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("created"))

		By("reading the account back in a second client session")
		out, err = repl("tb-repl-lookup", addresses, "lookup_accounts id=1")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(`"ledger": "700"`))
	})
})

func kubectl(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", args...))
}

// repl runs one `tigerbeetle repl` session in a throwaway pod and returns its output.
//
// The repl exits early without a stdin, even with --command, so the pod keeps stdin open. It
// is not attached (`kubectl run -i`): a short session can finish before the attach and lose
// its output. The output is read from the pod logs once the pod has finished instead.
func repl(podName, addresses, command string) (string, error) {
	overrides, err := json.Marshal(map[string]any{"spec": map[string]any{"containers": []map[string]any{{
		"name":    podName,
		"image":   tigerbeetleImage,
		"stdin":   true,
		"command": []string{"/tigerbeetle", "repl", "--cluster=0", "--addresses=" + addresses, "--command=" + command},
	}}}})
	if err != nil {
		return "", err
	}
	if _, err := kubectl("run", podName, "--namespace", clusterNamespace, "--restart=Never",
		"--image", tigerbeetleImage, "--overrides", string(overrides)); err != nil {
		return "", err
	}
	defer func() { _, _ = kubectl("delete", "pod", podName, "--namespace", clusterNamespace, "--wait=false") }()

	var phase string
	Eventually(func(g Gomega) string {
		phase, err = kubectl("get", "pod", podName, "--namespace", clusterNamespace, "-o", "jsonpath={.status.phase}")
		g.Expect(err).NotTo(HaveOccurred())
		return phase
	}).WithTimeout(2 * time.Minute).Should(BeElementOf("Succeeded", "Failed"))

	out, err := kubectl("logs", podName, "--namespace", clusterNamespace)
	if err == nil && phase != "Succeeded" {
		err = fmt.Errorf("repl pod %s %s", podName, strings.ToLower(phase))
	}
	return out, err
}

// clusterField reads one JSONPath field of the TigerBeetleCluster under test.
func clusterField(g Gomega, jsonPath string) string {
	out, err := kubectl("get", "tigerbeetlecluster", clusterName, "-n", clusterNamespace,
		"-o", "jsonpath={"+jsonPath+"}")
	g.Expect(err).NotTo(HaveOccurred())
	return out
}

func unique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
