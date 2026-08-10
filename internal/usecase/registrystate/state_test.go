package registrystate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStateTracksPublishesAndExpiresPendingBlobs(t *testing.T) {
	now := time.Now().UTC()
	state := New()
	state.AddPending("sha256:published", now)
	state.AddPending("sha256:pending", now)
	state.AddPending("sha256:expired", now.Add(-PendingBlobTTL-time.Second))

	state.MarkPublished([]string{"sha256:published"})
	pending := state.PendingDigests(now)

	assert.Equal(t, map[string]struct{}{"sha256:pending": {}}, pending)
}
