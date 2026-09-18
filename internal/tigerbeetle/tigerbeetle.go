// Package tigerbeetle holds TigerBeetle-specific logic shared by the operator and the init helper:
// cluster IDs, address lists, data file names and cache sizing.
package tigerbeetle

import (
	"fmt"
	"math"
	"math/big"
	"net"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	// MaxReplicaCount is the largest replica count TigerBeetle supports.
	MaxReplicaCount = 6

	// ProcessOverheadBytes is the memory TigerBeetle needs besides the grid cache
	// (from `tigerbeetle -h`: "(Total RAM) - 3GiB (TigerBeetle) - 1GiB (System)").
	ProcessOverheadBytes int64 = 3 << 30
)

var maxU128 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

// ValidateClusterID checks that id is a canonical decimal u128. Cluster ID 0 is reserved
// for testing and only allowed in development mode.
func ValidateClusterID(id string, development bool) error {
	if id == "0" && !development {
		return fmt.Errorf("clusterID 0 is reserved for testing; use it only with development: true")
	}
	// The ID is part of data file names, so only the canonical form is accepted: printing the
	// parsed number must give back the input (no sign, no leading zeros).
	var n big.Int
	if _, ok := n.SetString(id, 10); !ok || n.Sign() < 0 || n.String() != id {
		return fmt.Errorf("clusterID %q must be a decimal integer without sign or leading zeros", id)
	}
	if n.Cmp(maxU128) > 0 {
		return fmt.Errorf("clusterID %q does not fit in an unsigned 128-bit integer", id)
	}
	return nil
}

// DataFileName is the name of a replica's data file inside the data volume.
func DataFileName(clusterID string, replica int) string {
	return fmt.Sprintf("%s_%d.tigerbeetle", clusterID, replica)
}

// Addresses builds the ordered --addresses value: one ip:port per in-cluster replica
// (by ordinal), followed by the external addresses.
func Addresses(serviceIPs []string, port int32, external []string) string {
	addrs := make([]string, 0, len(serviceIPs)+len(external))
	for _, ip := range serviceIPs {
		addrs = append(addrs, net.JoinHostPort(ip, strconv.Itoa(int(port))))
	}
	addrs = append(addrs, external...)
	return strings.Join(addrs, ",")
}

// LocalAddresses replaces the replica's own slot in addresses with 0.0.0.0:port,
// because a pod cannot bind to its Service IP.
func LocalAddresses(addresses string, replica int, port int32) (string, error) {
	addrs := strings.Split(addresses, ",")
	if replica < 0 || replica >= len(addrs) {
		return "", fmt.Errorf("replica %d is out of range for %d addresses", replica, len(addrs))
	}
	addrs[replica] = net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port)))
	return strings.Join(addrs, ","), nil
}

// OrdinalFromHostname extracts the StatefulSet ordinal from a pod hostname such as "my-tb-2".
func OrdinalFromHostname(hostname string) (int, error) {
	i := strings.LastIndex(hostname, "-")
	if i < 0 || i == len(hostname)-1 {
		return 0, fmt.Errorf("hostname %q has no ordinal suffix", hostname)
	}
	ordinal, err := strconv.Atoi(hostname[i+1:])
	if err != nil || ordinal < 0 {
		return 0, fmt.Errorf("hostname %q has no ordinal suffix", hostname)
	}
	return ordinal, nil
}

// ParseSize parses a TigerBeetle size such as "512MiB" into bytes.
func ParseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		shift  uint
	}{{"KiB", 10}, {"MiB", 20}, {"GiB", 30}}
	for _, u := range units {
		digits, ok := strings.CutSuffix(s, u.suffix)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != digits {
			return 0, fmt.Errorf("size %q must be a positive integer followed by KiB, MiB or GiB", s)
		}
		if n > math.MaxInt64>>u.shift {
			return 0, fmt.Errorf("size %q is too large", s)
		}
		return n << u.shift, nil
	}
	return 0, fmt.Errorf("size %q must be a positive integer followed by KiB, MiB or GiB", s)
}

// CacheGrid returns the --cache-grid value to pass, or "" to use TigerBeetle's default.
//
// An explicit value is used as is, but outside development mode the memory limit must leave
// ProcessOverheadBytes on top of it. Without an explicit value, outside development mode, the
// cache grid is derived from the memory limit as limit - ProcessOverheadBytes, in whole MiB.
func CacheGrid(explicit string, memoryLimit *resource.Quantity, development bool) (string, error) {
	var limit int64
	if memoryLimit != nil {
		limit = memoryLimit.Value()
	}

	if explicit != "" {
		grid, err := ParseSize(explicit)
		if err != nil {
			return "", err
		}
		if !development && limit > 0 && limit < grid+ProcessOverheadBytes {
			return "", fmt.Errorf("memory limit %s is too small for cacheGrid %s: TigerBeetle needs about 3GiB on top of the grid cache",
				memoryLimit.String(), explicit)
		}
		return explicit, nil
	}

	if development || limit == 0 {
		return "", nil
	}
	gridMiB := (limit - ProcessOverheadBytes) >> 20
	if gridMiB <= 0 {
		return "", fmt.Errorf("memory limit %s is too small: TigerBeetle needs more than 3GiB outside development mode",
			memoryLimit.String())
	}
	return fmt.Sprintf("%dMiB", gridMiB), nil
}
