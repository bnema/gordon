package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/bnema/gordon/internal/adapters/dto"
	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type attachmentPushTestControlPlane struct {
	findAttachmentTargets func(context.Context, string) ([]string, error)
	getStatus             func(context.Context) (*remote.Status, error)
}

var _ ControlPlane = (*attachmentPushTestControlPlane)(nil)

func (c *attachmentPushTestControlPlane) ListRoutesWithDetails(context.Context) ([]remote.RouteInfo, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetHealth(context.Context) (map[string]*remote.RouteHealth, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetRoute(context.Context, string) (*domain.Route, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) FindRoutesByImage(context.Context, string) ([]domain.Route, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) AddRoute(context.Context, domain.Route) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) UpdateRoute(context.Context, domain.Route) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) RemoveRoute(context.Context, string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) Bootstrap(context.Context, dto.BootstrapRequest) (*dto.BootstrapResponse, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) ListSecretsWithAttachments(context.Context, string) (*remote.SecretsListResult, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) SetSecrets(context.Context, string, map[string]string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) DeleteSecret(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) SetAttachmentSecrets(context.Context, string, string, map[string]string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) DeleteAttachmentSecret(context.Context, string, string, string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetAllAttachmentsConfig(context.Context) (map[string][]string, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetAttachmentsConfig(context.Context, string) ([]string, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) FindAttachmentTargetsByImage(ctx context.Context, imageName string) ([]string, error) {
	if c.findAttachmentTargets != nil {
		return c.findAttachmentTargets(ctx, imageName)
	}
	return nil, nil
}

func (c *attachmentPushTestControlPlane) AddAttachment(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) RemoveAttachment(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetAutoRouteAllowedDomains(context.Context) ([]string, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) AddAutoRouteAllowedDomain(context.Context, string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) RemoveAutoRouteAllowedDomain(context.Context, string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetStatus(ctx context.Context) (*remote.Status, error) {
	if c.getStatus != nil {
		return c.getStatus(ctx)
	}
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) Reload(context.Context) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetConfig(context.Context) (*remote.Config, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) DeployIntent(context.Context, string) error {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) Deploy(context.Context, string) (*remote.DeployResult, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) Restart(context.Context, string, bool) (*remote.RestartResult, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) ListTags(context.Context, string) ([]string, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) ListBackups(context.Context, string) ([]dto.BackupJob, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) BackupStatus(context.Context) ([]dto.BackupJob, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) RunBackup(context.Context, string, string) (*dto.BackupRunResponse, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) DetectDatabases(context.Context, string) ([]dto.DatabaseInfo, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetProcessLogs(context.Context, int) ([]string, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) GetContainerLogs(context.Context, string, int) ([]string, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) StreamProcessLogs(context.Context, int) (<-chan string, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) StreamContainerLogs(context.Context, string, int) (<-chan string, error) {
	panic("unexpected call")
}

func (c *attachmentPushTestControlPlane) ListVolumes(_ context.Context) ([]dto.Volume, error) {
	return nil, nil
}

func (c *attachmentPushTestControlPlane) PruneVolumes(_ context.Context, _ dto.VolumePruneRequest) (*dto.VolumePruneResponse, error) {
	return nil, nil
}

func TestResolveAttachmentImage_OneTarget(t *testing.T) {
	cp := &attachmentPushTestControlPlane{
		findAttachmentTargets: func(context.Context, string) ([]string, error) {
			return []string{"app.example.com"}, nil
		},
		getStatus: func(context.Context) (*remote.Status, error) {
			return &remote.Status{RegistryDomain: "registry.example.com"}, nil
		},
	}

	registry, imageName, targets, err := resolveAttachmentImage(context.Background(), cp, "postgres")

	require.NoError(t, err)
	assert.Equal(t, "registry.example.com", registry)
	assert.Equal(t, "postgres", imageName)
	assert.Equal(t, []string{"app.example.com"}, targets)
}

func TestResolveAttachmentImage_MultipleTargets(t *testing.T) {
	cp := &attachmentPushTestControlPlane{
		findAttachmentTargets: func(context.Context, string) ([]string, error) {
			return []string{"app.example.com", "backend"}, nil
		},
		getStatus: func(context.Context) (*remote.Status, error) {
			return &remote.Status{RegistryDomain: "registry.example.com"}, nil
		},
	}

	registry, imageName, targets, err := resolveAttachmentImage(context.Background(), cp, "redis")

	require.NoError(t, err)
	assert.Equal(t, "registry.example.com", registry)
	assert.Equal(t, "redis", imageName)
	assert.Equal(t, []string{"app.example.com", "backend"}, targets)
}

func TestResolveAttachmentImage_NotConfigured(t *testing.T) {
	cp := &attachmentPushTestControlPlane{
		findAttachmentTargets: func(context.Context, string) ([]string, error) {
			return nil, nil
		},
	}

	_, _, _, err := resolveAttachmentImage(context.Background(), cp, "postgres")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured as an attachment")
}

func TestAttachmentPushCmd_NoDeploy(t *testing.T) {
	origResolveControlPlane := resolveControlPlaneFn
	origDetermineVersion := determineVersionFn
	origNewImageOps := newImageOpsFn
	t.Cleanup(func() {
		resolveControlPlaneFn = origResolveControlPlane
		determineVersionFn = origDetermineVersion
		newImageOpsFn = origNewImageOps
	})

	cp := &attachmentPushTestControlPlane{
		findAttachmentTargets: func(context.Context, string) ([]string, error) {
			return []string{"app.example.com"}, nil
		},
		getStatus: func(context.Context) (*remote.Status, error) {
			return &remote.Status{RegistryDomain: "registry.example.com"}, nil
		},
	}

	resolveControlPlaneFn = func(string) (*controlPlaneHandle, error) {
		return &controlPlaneHandle{plane: cp}, nil
	}
	determineVersionFn = func(context.Context, string) string { return "v1.2.3" }
	pushCalled := false
	newImageOpsFn = func() (pushImageOps, error) {
		m := climocks.NewMockpushImageOps(t)
		m.On("Tag", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		m.On("Exists", mock.Anything, mock.Anything).Return(true, nil)
		m.On("Push", mock.Anything, mock.Anything).Run(func(mock.Arguments) {
			pushCalled = true
		}).Return(nil)
		return m, nil
	}

	cmd := newAttachmentsPushCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"postgres"})

	err := cmd.Execute()

	require.NoError(t, err)
	assert.True(t, pushCalled)
	assert.Contains(t, out.String(), "Push complete")
}

func TestAttachmentPushCmd_TaggedImageInputBuildsValidRefs(t *testing.T) {
	origResolveControlPlane := resolveControlPlaneFn
	origDetermineVersion := determineVersionFn
	origNewImageOps := newImageOpsFn
	t.Cleanup(func() {
		resolveControlPlaneFn = origResolveControlPlane
		determineVersionFn = origDetermineVersion
		newImageOpsFn = origNewImageOps
	})

	cp := &attachmentPushTestControlPlane{
		findAttachmentTargets: func(context.Context, string) ([]string, error) {
			return []string{"app.example.com"}, nil
		},
		getStatus: func(context.Context) (*remote.Status, error) {
			return &remote.Status{RegistryDomain: "registry.example.com"}, nil
		},
	}

	resolveControlPlaneFn = func(string) (*controlPlaneHandle, error) {
		return &controlPlaneHandle{plane: cp}, nil
	}
	determineVersionFn = func(context.Context, string) string { return "v1.2.3" }

	var gotVersionRef string
	var gotLatestRef string
	newImageOpsFn = func() (pushImageOps, error) {
		m := climocks.NewMockpushImageOps(t)
		m.On("Tag", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		m.On("Exists", mock.Anything, mock.Anything).Return(true, nil)
		m.On("Push", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			ref := args.Get(1).(string)
			if gotVersionRef == "" {
				gotVersionRef = ref
			} else if gotLatestRef == "" {
				gotLatestRef = ref
			}
		}).Return(nil)
		return m, nil
	}

	cmd := newAttachmentsPushCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"postgres:18"})

	err := cmd.Execute()

	require.NoError(t, err)
	assert.Equal(t, "registry.example.com/postgres:v1.2.3", gotVersionRef)
	assert.Equal(t, "registry.example.com/postgres:latest", gotLatestRef)
}

func TestNormalizeAttachmentImageName_PreservesNamespaceWithoutRegistryHost(t *testing.T) {
	got := normalizeAttachmentImageName("myorg/postgres:18")

	assert.Equal(t, "myorg/postgres", got)
}
