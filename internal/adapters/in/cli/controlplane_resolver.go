package cli

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
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
		log := cliLogger()
		log.Warn().Err(err).Msg("failed to close control-plane resources")
	}
}

func resolveControlPlane(configPath string) (*controlPlaneHandle, error) {
	client, isRemote := GetRemoteClient()
	if isRemote {
		return &controlPlaneHandle{plane: NewRemoteControlPlane(client), isRemote: true}, nil
	}

	return resolveLocalControlPlane(configPath)
}

func newRemoteControlPlaneHandle(target *remote.ResolvedRemote) *controlPlaneHandle {
	client := remote.NewClient(target.URL, remoteClientOptions(target.Token, target.InsecureTLS)...)
	return &controlPlaneHandle{plane: NewRemoteControlPlane(client), isRemote: true}
}

func resolveControlPlaneForRouteDomain(ctx context.Context, routeDomain string) (*controlPlaneHandle, error) {
	return resolveControlPlaneWithInference(ctx, func(ctx context.Context) (*remote.ResolvedRemote, error) {
		return inferRemoteForRouteDomain(ctx, routeDomain)
	})
}

func resolveControlPlaneForAttachmentTarget(ctx context.Context, target string) (*controlPlaneHandle, error) {
	return resolveControlPlaneWithInference(ctx, func(ctx context.Context) (*remote.ResolvedRemote, error) {
		return inferRemoteForAttachmentTarget(ctx, target)
	})
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
	return resolveControlPlane(configPath)
}

func resolveLocalControlPlane(configPath string) (*controlPlaneHandle, error) {
	kernel, err := app.NewKernelQuiet(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize local control plane: %w", err)
	}

	return &controlPlaneHandle{
		plane:   NewLocalControlPlane(kernel),
		closeFn: kernel.Close,
	}, nil
}
