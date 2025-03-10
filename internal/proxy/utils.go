package proxy

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// proxyGenerateUUID generates a random UUID for use in various identifiers
// This is a renamed version to avoid conflicts until we clean up proxy.go
func proxyGenerateUUID() string {
	id, err := uuid.NewRandom()
	if err != nil {
		// If we can't generate a UUID using the standard library,
		// fall back to our own implementation
		b := make([]byte, 16)
		_, err := rand.Read(b)
		if err != nil {
			return fmt.Sprintf("%d", time.Now().UnixNano())
		}
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
	}
	return id.String()
}
