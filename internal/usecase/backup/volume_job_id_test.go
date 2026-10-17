package backup

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNewVolumeBackupJobIDIsShortAndUnique proves the volume job ID adds
// only a short random suffix: the storage key already holds the timestamp.
func TestNewVolumeBackupJobIDIsShortAndUnique(t *testing.T) {
	a, b := newVolumeBackupJobID(), newVolumeBackupJobID()

	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}$`), a)
	assert.NotEqual(t, a, b)
}
