package cli

import (
	"fmt"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/app"
)

type controlPlaneHandle struct {
	plane    ControlPlane
	isRemote bool
	closeFn  func() error
}

func (h *controlPlaneHandle) close() {
	if h == nil || h.closeFn == nil {
		return
	}
	if err := h.closeFn(); err != nil {
		log := zerowrap.Default()
		log.Warn().Err(err).Msg("failed to close control-plane resources")
	}
}

func resolveControlPlane(configPath string) (*controlPlaneHandle, error) {
	client, isRemote := GetRemoteClient()
	if isRemote {
		return &controlPlaneHandle{plane: NewRemoteControlPlane(client), isRemote: true}, nil
	}

	kernel, err := app.NewKernel(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize local control plane: %w", err)
	}
	if kernel.AuthEnabled() {
		kernel.Close()
		return nil, fmt.Errorf("local control plane is disabled when auth.enabled=true; use --remote with token")
	}

	return &controlPlaneHandle{
		plane:   NewLocalControlPlane(kernel),
		closeFn: kernel.Close,
	}, nil
}
