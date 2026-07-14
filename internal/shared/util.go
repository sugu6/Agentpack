package shared

import (
	"time"
)

// CopySlice returns a deep copy of a string slice.
func CopySlice(src []string) []string {
	if src == nil {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// CopyMap returns a deep copy of a string map.
func CopyMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// NowRFC3339 returns the current UTC time formatted as RFC3339Nano.
func NowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
