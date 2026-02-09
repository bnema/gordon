package cli

import (
	"fmt"

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
	_ = h.closeFn()
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

	return &controlPlaneHandle{
		plane:   NewLocalControlPlane(kernel),
		closeFn: kernel.Close,
	}, nil
}
