package cli

import (
	"context"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// AppControlPlane abstracts local vs remote app operations. The remote
// implementation delegates to remote.Client HTTP methods; the local
// implementation calls service interfaces directly at cutover.
// CLI commands in apps.go consume this interface; root.go registration
// lands at the cutover slice (this file is unreachable until then).
type AppControlPlane interface {
	ApplyApp(ctx context.Context, req dto.AppApplyRequest) (*dto.AppApplyResponse, error)
	ListApps(ctx context.Context) ([]dto.AppSummaryDTO, error)
	ShowApp(ctx context.Context, app string) (*dto.AppShowResponse, error)
	DiffApp(ctx context.Context, app string) (*dto.AppDiffResponse, error)
	DeployApp(ctx context.Context, app string, req dto.AppDeployRequest) (*dto.AppDeployResponse, string, error)
	StopApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error)
	StartApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error)
	RestartApp(ctx context.Context, app, service string) (*dto.AppDeployResponse, string, error)
	RemoveApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error)
	OperationByKey(ctx context.Context, app, key string) (*dto.AppDeployResponse, error)
	SetAppSecrets(ctx context.Context, app string, req dto.AppSecretSetRequest) error
	DeleteAppSecret(ctx context.Context, app string, req dto.AppSecretDeleteRequest) error
}

// remoteAppControlPlane delegates to remote.Client HTTP methods.
type remoteAppControlPlane struct {
	client *remote.Client
}

// NewRemoteAppControlPlane creates the remote app control plane.
func NewRemoteAppControlPlane(client *remote.Client) AppControlPlane {
	return &remoteAppControlPlane{client: client}
}

func (p *remoteAppControlPlane) ApplyApp(ctx context.Context, req dto.AppApplyRequest) (*dto.AppApplyResponse, error) {
	return p.client.ApplyApp(ctx, req)
}

func (p *remoteAppControlPlane) ListApps(ctx context.Context) ([]dto.AppSummaryDTO, error) {
	return p.client.ListApps(ctx)
}

func (p *remoteAppControlPlane) ShowApp(ctx context.Context, app string) (*dto.AppShowResponse, error) {
	return p.client.ShowApp(ctx, app)
}

func (p *remoteAppControlPlane) DiffApp(ctx context.Context, app string) (*dto.AppDiffResponse, error) {
	return p.client.DiffApp(ctx, app)
}

func (p *remoteAppControlPlane) DeployApp(ctx context.Context, app string, req dto.AppDeployRequest) (*dto.AppDeployResponse, string, error) {
	return p.client.DeployApp(ctx, app, req)
}

func (p *remoteAppControlPlane) StopApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return p.client.StopApp(ctx, app)
}

func (p *remoteAppControlPlane) StartApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return p.client.StartApp(ctx, app)
}

func (p *remoteAppControlPlane) RestartApp(ctx context.Context, app, service string) (*dto.AppDeployResponse, string, error) {
	return p.client.RestartApp(ctx, app, service)
}

func (p *remoteAppControlPlane) RemoveApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return p.client.RemoveApp(ctx, app)
}

func (p *remoteAppControlPlane) OperationByKey(ctx context.Context, app, key string) (*dto.AppDeployResponse, error) {
	return p.client.OperationByKey(ctx, app, key)
}

func (p *remoteAppControlPlane) SetAppSecrets(ctx context.Context, app string, req dto.AppSecretSetRequest) error {
	return p.client.SetAppSecrets(ctx, app, req)
}

func (p *remoteAppControlPlane) DeleteAppSecret(ctx context.Context, app string, req dto.AppSecretDeleteRequest) error {
	return p.client.DeleteAppSecret(ctx, app, req)
}
