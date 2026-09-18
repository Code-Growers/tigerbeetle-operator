package tigerbeetle

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePodAnnotations parses the annotations file of a downward API volume, which holds one
// `key="value"` line per annotation with the value quoted as a Go string.
func ParsePodAnnotations(data string) (map[string]string, error) {
	annotations := map[string]string{}
	for line := range strings.Lines(data) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, quoted, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("malformed annotation line %q", line)
		}
		value, err := strconv.Unquote(quoted)
		if err != nil {
			return nil, fmt.Errorf("malformed value for annotation %q: %w", key, err)
		}
		annotations[key] = value
	}
	return annotations, nil
}
