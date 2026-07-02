package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	if handle, ok, err := resolveRemoteControlPlane(configPath); err != nil || ok {
		return handle, err
	}
	return resolveLocalControlPlane(configPath)
}

func resolveRemoteControlPlane(configPath string) (*controlPlaneHandle, bool, error) {
	if handle, ok, err := resolveExplicitRemoteControlPlane(); err != nil || ok {
		return handle, ok, err
	}
	if handle, ok, err := resolveConfiguredControlPlane(configPath); err != nil || ok {
		return handle, ok, err
	}

	client, isRemote, err := GetRemoteClient()
	if err != nil {
		return nil, false, err
	}
	if isRemote {
		return &controlPlaneHandle{plane: NewRemoteControlPlane(client), isRemote: true}, true, nil
	}
	return nil, false, nil
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

func resolveControlPlaneForRouteCleanupDomain(ctx context.Context, routeDomain string) (*controlPlaneHandle, error) {
	return resolveControlPlaneWithInference(ctx, func(ctx context.Context) (*remote.ResolvedRemote, error) {
		return inferRemoteForRouteCleanupDomain(ctx, routeDomain)
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

var newLocalKernelQuiet = app.NewKernelQuiet

func resolveExplicitRemoteControlPlane() (*controlPlaneHandle, bool, error) {
	resolved, ok, err := resolveExplicitRemoteTarget()
	if err != nil || !ok {
		return nil, ok, err
	}
	client := remote.NewClient(resolved.URL, remoteClientOptions(resolved.Token, resolved.InsecureTLS)...)
	return &controlPlaneHandle{plane: NewRemoteControlPlane(client), isRemote: true}, true, nil
}

func resolveExplicitRemoteTarget() (*remote.ResolvedRemote, bool, error) {
	target := strings.TrimSpace(remoteFlag)
	if target == "" {
		target = strings.TrimSpace(os.Getenv("GORDON_REMOTE"))
	}
	if target == "" {
		return nil, false, nil
	}
	return remote.ResolveStrict(target, tokenFlag, insecureTLSFlag)
}

func resolveConfiguredControlPlane(configPath string) (*controlPlaneHandle, bool, error) {
	resolved, ok, err := resolveConfiguredControlPlaneTarget(configPath)
	if err != nil || !ok {
		return nil, ok, err
	}
	client := remote.NewClient(resolved.URL, remoteClientOptions(resolved.Token, resolved.InsecureTLS)...)
	return &controlPlaneHandle{plane: NewRemoteControlPlane(client), isRemote: true}, true, nil
}

func resolveConfiguredControlPlaneTarget(configPath string) (*remote.ResolvedRemote, bool, error) {
	controlCfg, err := app.LoadControlConfig(configPath)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load control plane config: %w", err)
	}
	endpoint := strings.TrimSpace(controlCfg.Endpoint)
	if endpoint == "" {
		return nil, false, nil
	}
	token, insecureTLS := resolveConfiguredControlPlaneAuth(controlCfg)
	return &remote.ResolvedRemote{
		Name:        "control",
		URL:         endpoint,
		Token:       token,
		InsecureTLS: insecureTLS,
	}, true, nil
}

func resolveConfiguredControlPlaneAuth(controlCfg app.ControlConfig) (string, bool) {
	return resolveConfiguredControlPlaneToken(controlCfg), resolveConfiguredControlPlaneInsecureTLS(controlCfg)
}

func resolveConfiguredControlPlaneToken(controlCfg app.ControlConfig) string {
	if tokenFlag != "" {
		return tokenFlag
	}
	if envToken := os.Getenv("GORDON_TOKEN"); envToken != "" {
		return envToken
	}
	if controlCfg.Token != "" {
		return controlCfg.Token
	}
	if controlCfg.TokenEnv != "" {
		return os.Getenv(controlCfg.TokenEnv)
	}
	return ""
}

func resolveConfiguredControlPlaneInsecureTLS(controlCfg app.ControlConfig) bool {
	if insecureTLSFlag {
		return true
	}
	if env := os.Getenv("GORDON_INSECURE"); env != "" {
		if insecure, err := strconv.ParseBool(env); err == nil && insecure {
			return true
		}
	}
	return controlCfg.InsecureTLS
}

func resolveLocalControlPlane(configPath string) (*controlPlaneHandle, error) {
	kernel, err := newLocalKernelQuiet(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize local control plane: %w", err)
	}

	return &controlPlaneHandle{
		plane:   NewLocalControlPlane(kernel),
		closeFn: kernel.Close,
	}, nil
}
