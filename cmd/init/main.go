// Command init is the TigerBeetle pod helper. It is copied into a shared emptyDir by the
// "copy" subcommand, then run inside the TigerBeetle image:
//
//	copy <dest>  copy this binary to dest
//	format       format the replica data file (bootstrap Jobs only); skipped if it exists
//	recover      recover a missing data file (StatefulSet init container); never formats
//	start        run `tigerbeetle start` with the replica's own address slot set to 0.0.0.0
//
// Configuration comes from TB_* environment variables set by the operator.
package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Code-Growers/tigerbeetle-operator/internal/tigerbeetle"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "tigerbeetle-operator-init")
	slog.SetDefault(logger)

	if err := run(os.Args[1:]); err != nil {
		slog.Error("Command failed", "error", err)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: init copy <dest> | format | recover | start")
	}
	if args[0] == "copy" {
		if len(args) != 2 {
			return errors.New("usage: init copy <dest>")
		}
		return copySelf(args[1])
	}

	c, err := loadConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "format":
		return format(c)
	case "recover":
		return recoverReplica(c)
	case "start":
		return start(c)
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

type config struct {
	binary         string
	clusterID      string
	replica        int
	replicaCount   int
	addresses      string
	port           int32
	dataDir        string
	development    bool
	cacheGrid      string
	recoveryPolicy string
	statsd         string
}

func (c config) dataFile() string {
	return filepath.Join(c.dataDir, tigerbeetle.DataFileName(c.clusterID, c.replica))
}

func loadConfig() (config, error) {
	c := config{
		binary:         envOr(tigerbeetle.EnvBinary, tigerbeetle.DefaultBinary),
		clusterID:      os.Getenv(tigerbeetle.EnvClusterID),
		addresses:      os.Getenv(tigerbeetle.EnvAddresses),
		dataDir:        envOr(tigerbeetle.EnvDataDir, tigerbeetle.DefaultDataDir),
		development:    os.Getenv(tigerbeetle.EnvDevelopment) == "true",
		cacheGrid:      os.Getenv(tigerbeetle.EnvCacheGrid),
		recoveryPolicy: envOr(tigerbeetle.EnvRecoveryPolicy, "Automatic"),
		statsd:         os.Getenv(tigerbeetle.EnvStatsD),
	}
	if err := tigerbeetle.ValidateClusterID(c.clusterID, c.development); err != nil {
		return config{}, err
	}

	var err error
	if v := os.Getenv(tigerbeetle.EnvReplica); v != "" {
		c.replica, err = strconv.Atoi(v)
	} else {
		var hostname string
		if hostname, err = os.Hostname(); err == nil {
			c.replica, err = tigerbeetle.OrdinalFromHostname(hostname)
		}
	}
	if err != nil {
		return config{}, fmt.Errorf("determining replica index: %w", err)
	}

	if c.replicaCount, err = strconv.Atoi(os.Getenv(tigerbeetle.EnvReplicaCount)); err != nil || c.replicaCount < 1 {
		return config{}, fmt.Errorf("%s must be a positive integer", tigerbeetle.EnvReplicaCount)
	}
	if c.replica >= c.replicaCount {
		return config{}, fmt.Errorf("replica %d is out of range for replica count %d", c.replica, c.replicaCount)
	}
	if v := os.Getenv(tigerbeetle.EnvPort); v != "" {
		port, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			return config{}, fmt.Errorf("%s: %w", tigerbeetle.EnvPort, err)
		}
		c.port = int32(port)
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func copySelf(dest string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	slog.Info("Copied helper", "dest", dest)
	return nil
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// runToFile runs a TigerBeetle command that creates a data file. It writes to a temporary
// path and renames it into place only on success, so an interrupted run never leaves
// something that looks like a real data file.
func runToFile(c config, path string, args ...string) error {
	tmp := path + ".partial"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cmd := exec.Command(c.binary, append(args, tmp)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	slog.Info("Running TigerBeetle", "args", cmd.Args)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func format(c config) error {
	path := c.dataFile()
	ok, err := exists(path)
	if err != nil {
		return err
	}
	if ok {
		slog.Info("Data file already exists; skipping format", "path", path)
		return nil
	}
	args := []string{"format",
		"--cluster=" + c.clusterID,
		"--replica=" + strconv.Itoa(c.replica),
		"--replica-count=" + strconv.Itoa(c.replicaCount),
	}
	if c.development {
		args = append(args, "--development")
	}
	if err := runToFile(c, path, args...); err != nil {
		return fmt.Errorf("format failed: %w", err)
	}
	slog.Info("Formatted data file", "path", path)
	return nil
}

func recoverReplica(c config) error {
	path := c.dataFile()
	ok, err := exists(path)
	if err != nil {
		return err
	}
	if ok {
		slog.Info("Data file exists", "path", path)
		return nil
	}

	// Never format here: after bootstrap, a missing data file means the replica was lost.
	if c.replicaCount == 1 {
		waitForever("Data file is missing and a single-replica cluster cannot recover; "+
			"the data is lost unless the volume can be restored", "path", path)
	}
	if c.recoveryPolicy != "Automatic" {
		waitForApproval(path, c.recoveryPolicy)
	}

	addresses, err := tigerbeetle.LocalAddresses(c.addresses, c.replica, c.port)
	if err != nil {
		return err
	}
	args := []string{"recover",
		"--cluster=" + c.clusterID,
		"--addresses=" + addresses,
		"--replica=" + strconv.Itoa(c.replica),
		"--replica-count=" + strconv.Itoa(c.replicaCount),
	}
	if c.development {
		args = append(args, "--development")
	}

	backoff := 5 * time.Second
	for {
		slog.Info("Data file is missing; recovering from the cluster", "path", path)
		err := runToFile(c, path, args...)
		if err == nil {
			slog.Info("Recovered data file", "path", path)
			return nil
		}
		slog.Warn("Recover failed; the rest of the cluster must be healthy", "error", err, "retryIn", backoff)
		time.Sleep(backoff)
		backoff = min(backoff*2, time.Minute)
	}
}

// waitForApproval blocks until the operator marks this pod's recovery as approved, which it does
// when a user lists the pod's UID in the TigerBeetleCluster's approve-recovery annotation. The
// annotations reach the pod through a downward API volume, which the kubelet refreshes.
func waitForApproval(path, policy string) {
	file := filepath.Join(tigerbeetle.PodInfoDir, tigerbeetle.PodInfoAnnotationsFile)
	var lastLog time.Time
	for {
		approved, err := recoveryApproved(file)
		if approved {
			slog.Info("Recovery approved", "path", path)
			return
		}
		if time.Since(lastLog) >= time.Minute {
			attrs := []any{"path", path, "recoveryPolicy", policy}
			if err != nil {
				attrs = append(attrs, "error", err)
			}
			slog.Warn("Data file is missing; waiting for recovery approval", attrs...)
			lastLog = time.Now()
		}
		time.Sleep(5 * time.Second)
	}
}

func recoveryApproved(file string) (bool, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return false, err
	}
	annotations, err := tigerbeetle.ParsePodAnnotations(string(data))
	if err != nil {
		return false, err
	}
	return annotations[tigerbeetle.AnnotationRecoveryApproved] == "true", nil
}

func waitForever(msg string, attrs ...any) {
	for {
		slog.Error(msg, attrs...)
		time.Sleep(time.Minute)
	}
}

func start(c config) error {
	addresses, err := tigerbeetle.LocalAddresses(c.addresses, c.replica, c.port)
	if err != nil {
		return err
	}
	args := []string{"start", "--addresses=" + addresses}
	if c.cacheGrid != "" {
		args = append(args, "--cache-grid="+c.cacheGrid)
	}
	if c.development {
		args = append(args, "--development")
	}
	if c.statsd != "" {
		args = append(args, "--experimental", "--statsd="+c.statsd)
	}
	args = append(args, c.dataFile())

	// Run TigerBeetle as a child rather than exec'ing it: this process is PID 1 and must
	// forward termination signals, which TigerBeetle as PID 1 would otherwise ignore.
	cmd := exec.Command(c.binary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	slog.Info("Running TigerBeetle", "args", cmd.Args)
	if err := cmd.Start(); err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for sig := range signals {
			_ = cmd.Process.Signal(sig)
		}
	}()
	return cmd.Wait()
}
