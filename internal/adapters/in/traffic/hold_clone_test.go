package traffic

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestCloneTrafficGraphPreservesHold(t *testing.T) {
	until := time.Now().Add(time.Minute)
	graph := domain.TrafficGraph{Services: []domain.TrafficService{{Name: "service:app:tcp", HoldUntil: until}}}
	clone := cloneTrafficGraph(graph)
	require.Equal(t, until, clone.Services[0].HoldUntil)
	clone.Services[0].HoldUntil = time.Time{}
	require.Equal(t, until, graph.Services[0].HoldUntil)
}
