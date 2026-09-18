package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tigerbeetlev1alpha1 "github.com/Code-Growers/tigerbeetle-operator/api/v1alpha1"
	"github.com/Code-Growers/tigerbeetle-operator/internal/controller"
	// +kubebuilder:scaffold:imports
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(tigerbeetlev1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

type options struct {
	metricsAddr          string
	metricsCertPath      string
	metricsCertName      string
	metricsCertKey       string
	secureMetrics        bool
	probeAddr            string
	enableLeaderElection bool
	initImage            string
	logLevel             slog.Level
	logFormat            string
}

func main() {
	var opts options
	flag.StringVar(&opts.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&opts.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&opts.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&opts.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&opts.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&opts.metricsCertName, "metrics-cert-name", "tls.crt",
		"The name of the metrics server certificate file.")
	flag.StringVar(&opts.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.StringVar(&opts.initImage, "init-image", "ghcr.io/code-growers/tigerbeetle-operator-init:latest",
		"The init helper image used by TigerBeetle pods and format Jobs.")
	flag.TextVar(&opts.logLevel, "log-level", slog.LevelInfo, "Minimum log level: debug, info, warn or error.")
	flag.StringVar(&opts.logFormat, "log-format", "json", "Log format: json or text.")
	flag.Parse()

	logger, err := newLogger(opts.logFormat, opts.logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	slog.SetDefault(logger)
	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler()))
	klog.SetSlogLogger(logger)

	if err := run(opts); err != nil {
		slog.Error("Operator failed", "error", err)
		os.Exit(1)
	}
}

func newLogger(format string, level slog.Level) (*slog.Logger, error) {
	opts := slog.HandlerOptions{Level: level}
	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, &opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, &opts)), nil
	default:
		return nil, fmt.Errorf("unknown --log-format %q: use json or text", format)
	}
}

func run(opts options) error {
	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   opts.metricsAddr,
		SecureServing: opts.secureMetrics,
	}

	if opts.secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	var metricsCertWatcher *certwatcher.CertWatcher
	if len(opts.metricsCertPath) > 0 {
		slog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", opts.metricsCertPath,
			"metrics-cert-name", opts.metricsCertName,
			"metrics-cert-key", opts.metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(opts.metricsCertPath, opts.metricsCertName),
			filepath.Join(opts.metricsCertPath, opts.metricsCertKey),
		)
		if err != nil {
			return fmt.Errorf("initializing metrics certificate watcher: %w", err)
		}
		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(c *tls.Config) {
			c.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: opts.probeAddr,
		LeaderElection:         opts.enableLeaderElection,
		LeaderElectionID:       "2433540f.codegrowers.com",
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	reconciler := controller.TigerBeetleClusterReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		APIReader: mgr.GetAPIReader(),
		Recorder:  mgr.GetEventRecorderFor("tigerbeetle-operator"),
		InitImage: opts.initImage,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up TigerBeetleCluster controller: %w", err)
	}
	// +kubebuilder:scaffold:builder

	if metricsCertWatcher != nil {
		if err := mgr.Add(metricsCertWatcher); err != nil {
			return fmt.Errorf("adding metrics certificate watcher: %w", err)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up ready check: %w", err)
	}

	slog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("running manager: %w", err)
	}
	return nil
}
