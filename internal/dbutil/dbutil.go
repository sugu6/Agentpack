// Package dbutil provides shared database utility functions.
package dbutil

import (
	"strings"
	"time"
)

// Placeholders returns a comma-separated placeholder string for SQL queries
// (e.g., Placeholders(3) => "?,?,?").
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, n*2-1)
	b = append(b, '?')
	for i := 1; i < n; i++ {
		b = append(b, ',', '?')
	}
	return string(b)
}

// StrToIfaces converts a string slice to an interface slice for use in
// variadic SQL arguments.
func StrToIfaces(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ParseTimeToInt64 parses an RFC3339 timestamp string and returns its Unix
// epoch value (seconds). Returns 0 on parse failure.
func ParseTimeToInt64(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// ParseTimeToInt64Nano parses an RFC3339 timestamp string and returns its
// Unix epoch value in nanoseconds. Returns 0 on parse failure.
// Use this for timestamps that need sub-second precision (e.g. installed_at
// comparison in mergeDuplicatesLocked).
func ParseTimeToInt64Nano(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

// IsDuplicateColumnErr returns true if the error indicates an ALTER TABLE
// ADD COLUMN failed because the column already exists (SQLite).
func IsDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") ||
		strings.Contains(msg, "already exists")
}
