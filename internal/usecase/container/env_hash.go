package container

import (
	"crypto/sha256"
	"fmt"
	"slices"
)

// hashEnvironment computes a stable SHA-256 hash of a set of KEY=VALUE pairs.
// The input is sorted by key before hashing so map iteration order does not
// affect the result. The returned string is the hex-encoded digest.
func hashEnvironment(env []string) string {
	sorted := make([]string, len(env))
	copy(sorted, env)
	slices.Sort(sorted)

	h := sha256.New()
	for _, kv := range sorted {
		// Length-prefix each entry so embedded newlines cannot collide.
		_, _ = fmt.Fprintf(h, "%d:%s\n", len(kv), kv)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
