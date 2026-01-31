package domainsecrets

import (
	"fmt"
	"testing"

	"github.com/bnema/zerowrap"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

// CleanupPassAttachment removes attachment secrets from pass for testing.
// It's a test helper that should be called with defer to ensure cleanup.
// This function is exported to be shared across test packages.
func CleanupPassAttachment(_ *testing.T, containerName string, keys []string) {
	for _, key := range keys {
		path := fmt.Sprintf("%s/%s/%s", PassAttachmentPath, containerName, key)
		_ = passCmd("rm", "-f", path)
	}

	manifestPath := fmt.Sprintf("%s/%s/.keys", PassAttachmentPath, containerName)
	_ = passCmd("rm", "-f", manifestPath)
}
