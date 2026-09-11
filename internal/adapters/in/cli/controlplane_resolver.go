package cli

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
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
		log := cliLogger()
		log.Warn().Err(err).Msg("failed to close control-plane resources")
	}
}

var newLocalControlPlaneClient = remote.NewLocalClient

func resolveDaemonClient() (*remote.Client, bool, error) {
	client, isRemote, err := GetRemoteClient()
	if err != nil || isRemote {
		return client, isRemote, err
	}

	client, err = newLocalControlPlaneClient()
	if err != nil {
		return nil, false, fmt.Errorf("local control plane unavailable: %w", err)
	}
	return client, false, nil
}

func resolveControlPlane(_ string) (*controlPlaneHandle, error) {
	client, isRemote, err := resolveDaemonClient()
	if err != nil {
		return nil, err
	}
	return &controlPlaneHandle{plane: NewRemoteControlPlane(client), isRemote: isRemote}, nil
}

func newRemoteControlPlaneHandle(target *remote.ResolvedRemote) *controlPlaneHandle {
	client := remote.NewClient(target.URL, remoteClientOptions(target.Token, target.InsecureTLS)...)
	return &controlPlaneHandle{plane: NewRemoteControlPlane(client), isRemote: true}
}

func resolveControlPlaneForDomain(ctx context.Context, _ string) (*controlPlaneHandle, error) {
	return resolveControlPlaneWithInference(ctx, inferExplicitTarget)
}

func resolveControlPlaneForRepository(ctx context.Context, repository string) (*controlPlaneHandle, error) {
	return resolveControlPlaneWithInference(ctx, func(ctx context.Context) (*remote.ResolvedRemote, error) {
		return inferRemoteForRepository(ctx, repository)
	})
}

func resolveControlPlaneWithInference(ctx context.Context, infer func(context.Context) (*remote.ResolvedRemote, error)) (*controlPlaneHandle, error) {
	resolved, err := infer(ctx)
	if err != nil {
		return nil, err
	}
	if resolved != nil {
		return newRemoteControlPlaneHandle(resolved), nil
	}
	return resolveControlPlane(cliConfigPath)
}
