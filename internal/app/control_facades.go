package app

import (
	"context"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// controlNetworkService reads only the sanitized runtime actual-state stream.
// It deliberately does not depend on a container runtime or its socket.
type controlNetworkService struct {
	runtime out.RuntimeStateSubscriber
}

func (s controlNetworkService) ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("runtime state subscriber unavailable")
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshots, err := s.runtime.SubscribeRuntimeState(readCtx)
	if err != nil {
		return nil, fmt.Errorf("subscribe runtime state: %w", err)
	}
	select {
	case <-readCtx.Done():
		return nil, fmt.Errorf("runtime state unavailable: %w", readCtx.Err())
	case snapshot, ok := <-snapshots:
		if !ok {
			return nil, fmt.Errorf("runtime state unavailable")
		}
		networks := make([]*domain.NetworkInfo, 0, len(snapshot.Networks))
		for _, network := range snapshot.Networks {
			networks = append(networks, &domain.NetworkInfo{
				Name:       network.Name,
				Driver:     network.Driver,
				Containers: append([]string(nil), network.Aliases...),
			})
		}
		return networks, nil
	}
}
