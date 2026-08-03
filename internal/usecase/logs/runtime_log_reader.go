package logs

import (
	"context"
	"fmt"
	"io"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
)

type localRuntimeLogReader struct {
	containerSvc in.ContainerService
	runtime      out.ContainerRuntime
}

// NewLocalRuntimeLogReader creates a monolith/local implementation of the narrow runtime log reader port.
func NewLocalRuntimeLogReader(containerSvc in.ContainerService, runtime out.ContainerRuntime) out.RuntimeLogReader {
	return localRuntimeLogReader{containerSvc: containerSvc, runtime: runtime}
}

func (r localRuntimeLogReader) ReadRouteLogs(ctx context.Context, routeDomain string, follow bool) (io.ReadCloser, error) {
	if r.containerSvc == nil || r.runtime == nil {
		return nil, fmt.Errorf("runtime log reader not configured")
	}
	container, ok := r.containerSvc.Get(ctx, routeDomain)
	if !ok || container == nil {
		return nil, fmt.Errorf("container not found for domain: %s", routeDomain)
	}
	return r.runtime.GetContainerLogs(ctx, container.ID, follow)
}
