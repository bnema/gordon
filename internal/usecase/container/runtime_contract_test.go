package container

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeContractDeployConfig(t *testing.T) {
	t.Parallel()

	baseInput := containerConfigInput{
		Domain:       "app.example.com",
		Image:        "app:latest",
		ImageRef:     "app@sha256:1",
		ExposedPorts: []int{8080},
		EnvVars:      []string{"GORDON_ROUTE=app.example.com", "FROM_FILE=1"},
		EnvHash:      "envhash",
		Volumes:      map[string]string{"/data": "gordon-app-example-com-data"},
		NetworkName:  "gordon-app-example-com",
		ImageLabels:  map[string]string{},
	}

	tests := []struct {
		name   string
		cfg    Config
		mutate func(*containerConfigInput)
		assert func(*testing.T, *domain.ContainerConfig)
	}{
		{
			name: "gordon proxy port label is propagated and exposed",
			mutate: func(in *containerConfigInput) {
				in.ImageLabels[domain.LabelProxyPort] = "9090"
			},
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.Equal(t, "9090", got.Labels[domain.LabelProxyPort])
				assert.ElementsMatch(t, []int{8080, 9090}, got.Ports)
			},
		},
		{
			name: "exposed port fallback is preserved",
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.Equal(t, []int{8080}, got.Ports)
			},
		},
		{
			name: "env file merge and pre-resolved route env are passed through",
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.ElementsMatch(t, []string{"GORDON_ROUTE=app.example.com", "FROM_FILE=1"}, got.Env)
				assert.Equal(t, "envhash", got.Labels[domain.LabelEnvHash])
			},
		},
		{
			name: "volume auto-create preserve result is passed through",
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.Equal(t, map[string]string{"/data": "gordon-app-example-com-data"}, got.Volumes)
			},
		},
		{
			name: "network isolation on uses selected network and route target alias",
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.Equal(t, "gordon-app-example-com", got.NetworkMode)
				assert.Equal(t, "app.example.com", got.Hostname)
				assert.Equal(t, []string{"gordon-target-app-example-com"}, got.Aliases)
			},
		},
		{
			name:   "network isolation off leaves default network empty",
			mutate: func(in *containerConfigInput) { in.NetworkName = "" },
			assert: func(t *testing.T, got *domain.ContainerConfig) { assert.Empty(t, got.NetworkMode) },
		},
		{
			name:   "attachment deployment uses alternate replacement name",
			mutate: func(in *containerConfigInput) { in.Existing = &domain.Container{Name: "gordon-app.example.com"} },
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.Equal(t, "gordon-app.example.com-new", got.Name)
			},
		},
		{
			name: "memory cpu pid defaults and restart policy always",
			cfg:  Config{DefaultMemoryLimit: 128 << 20, DefaultNanoCPUs: 500_000_000, DefaultPidsLimit: 64},
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.Equal(t, int64(128<<20), got.MemoryLimit)
				assert.Equal(t, int64(500_000_000), got.NanoCPUs)
				assert.Equal(t, int64(64), got.PidsLimit)
				assert.Equal(t, domain.RestartPolicyAlways, got.RestartPolicy)
			},
		},
		{
			name: "strict security profile hardens config",
			cfg:  Config{SecurityProfile: "strict"},
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.True(t, got.ReadOnlyRootFS)
				assert.Equal(t, []string{"ALL"}, got.CapDrop)
			},
		},
		{
			name: "compat security profile keeps runtime defaults",
			cfg:  Config{SecurityProfile: "compat"},
			assert: func(t *testing.T, got *domain.ContainerConfig) {
				assert.False(t, got.ReadOnlyRootFS)
				assert.Nil(t, got.CapDrop)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := baseInput
			in.ExposedPorts = slices.Clone(baseInput.ExposedPorts)
			in.EnvVars = slices.Clone(baseInput.EnvVars)
			in.Volumes = map[string]string{"/data": "gordon-app-example-com-data"}
			in.ImageLabels = map[string]string{}
			if tt.mutate != nil {
				tt.mutate(&in)
			}
			svc := NewService(nil, nil, nil, nil, tt.cfg, nil)
			tt.assert(t, svc.buildContainerConfig(in))
		})
	}
}

func TestRuntimeContractOperationSequence(t *testing.T) {
	ctx := domain.WithSkipReadiness(testContext())

	t.Run("first deploy", func(t *testing.T) {
		rt := newRecordingRuntime()
		svc := NewService(rt, noopEnvLoader{}, noopEventPublisher{}, nil, testMinDelayConfig(), nil)
		_, err := svc.Deploy(ctx, domain.Route{Domain: "app.example.com", Image: "app:latest", Env: []string{"APP_ENV=test"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"ListContainers", "ListContainers", "ListImages", "PullImage", "GetImageExposedPorts", "GetImageLabels", "InspectImageEnv", "CreateContainer", "StartContainer", "InspectContainer"}, rt.ops)
	})

	t.Run("deploy failure cleanup", func(t *testing.T) {
		rt := newRecordingRuntime()
		rt.startErr = errors.New("boom")
		svc := NewService(rt, noopEnvLoader{}, noopEventPublisher{}, nil, testMinDelayConfig(), nil)
		_, err := svc.Deploy(ctx, domain.Route{Domain: "app.example.com", Image: "app:latest", Env: []string{"APP_ENV=test"}})
		require.Error(t, err)
		assert.Subset(t, rt.ops, []string{"CreateContainer", "StartContainer", "RemoveContainer"})
	})

	t.Run("zero-downtime replacement starts new container before stopping old", func(t *testing.T) {
		rt := newRecordingRuntime()
		rt.containers = []*domain.Container{{ID: "old", Name: "gordon-app.example.com", Image: "app:old", ImageID: "sha256:old", Status: string(domain.ContainerStatusRunning), Labels: map[string]string{domain.LabelManaged: "true", domain.LabelDomain: "app.example.com"}}}
		svc := NewService(rt, noopEnvLoader{}, noopEventPublisher{}, nil, testMinDelayConfig(), nil)
		_, err := svc.Deploy(ctx, domain.Route{Domain: "app.example.com", Image: "app:new", Env: []string{"APP_ENV=test"}})
		require.NoError(t, err)
		svc.WaitForCleanup()
		assert.Less(t, opIndex(t, rt.ops, "StartContainer"), opIndex(t, rt.ops, "StopContainer"))
		assert.Contains(t, rt.ops, "RenameContainer")
	})

	t.Run("route removal cleanup removes main route container and preserves attachment", func(t *testing.T) {
		rt := newRecordingRuntime()
		rt.containers = []*domain.Container{
			{ID: "main", Name: "gordon-app.example.com", Image: "app:old", Status: string(domain.ContainerStatusRunning), Labels: map[string]string{domain.LabelManaged: "true", domain.LabelDomain: "app.example.com"}},
			{ID: "db", Name: "gordon-app.example.com-postgres", Image: "postgres:15", Status: string(domain.ContainerStatusRunning), Labels: map[string]string{domain.LabelManaged: "true", domain.LabelAttachment: "true", domain.LabelAttachedTo: "app.example.com"}},
		}
		svc := NewService(rt, noopEnvLoader{}, noopEventPublisher{}, nil, testMinDelayConfig(), nil)
		report, err := svc.ReconcileRemovedRoute(ctx, "app.example.com")
		require.NoError(t, err)
		assert.Contains(t, rt.ops, "StopContainer")
		assert.Contains(t, rt.ops, "RemoveContainer")
		assert.Len(t, report.PreservedAttachments, 1)
	})

	t.Run("startup sync and autostart inspect existing before deploying missing route", func(t *testing.T) {
		rt := newRecordingRuntime()
		rt.containers = []*domain.Container{{ID: "existing", Name: "gordon-existing.example.com", Image: "existing:latest", Status: string(domain.ContainerStatusRunning), Labels: map[string]string{domain.LabelManaged: "true", domain.LabelDomain: "existing.example.com"}}}
		svc := NewService(rt, noopEnvLoader{}, noopEventPublisher{}, nil, testMinDelayConfig(), nil)
		require.NoError(t, svc.SyncContainers(ctx))
		require.NoError(t, svc.AutoStart(ctx, []domain.Route{{Domain: "existing.example.com", Image: "existing:latest", Env: []string{"APP_ENV=test"}}, {Domain: "missing.example.com", Image: "missing:latest", Env: []string{"APP_ENV=test"}}}))
		assert.Contains(t, rt.ops, "ListContainers")
		assert.Contains(t, rt.ops, "CreateContainer")
		assert.Contains(t, rt.ops, "StartContainer")
	})

	t.Run("attachment redeploy creates configured attachment with route deploy", func(t *testing.T) {
		rt := newRecordingRuntime()
		cfg := testMinDelayConfig()
		cfg.Attachments = map[string][]string{"app.example.com": {"postgres:15"}}
		svc := NewService(rt, noopEnvLoader{}, noopEventPublisher{}, nil, cfg, nil)
		_, err := svc.Deploy(ctx, domain.Route{Domain: "app.example.com", Image: "app:latest", Env: []string{"APP_ENV=test"}})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, countOps(rt.ops, "CreateContainer"), 2)
		assert.GreaterOrEqual(t, countOps(rt.ops, "StartContainer"), 2)
	})
}

func opIndex(t *testing.T, ops []string, op string) int {
	t.Helper()
	for i, got := range ops {
		if got == op {
			return i
		}
	}
	require.Failf(t, "operation not recorded", "%s not in %v", op, ops)
	return -1
}

func countOps(ops []string, op string) int {
	var n int
	for _, got := range ops {
		if got == op {
			n++
		}
	}
	return n
}

type noopEventPublisher struct{}

func (noopEventPublisher) Publish(domain.EventType, any) error { return nil }

type noopEnvLoader struct{}

func (noopEnvLoader) LoadEnv(context.Context, string) ([]string, error) { return nil, nil }
func (noopEnvLoader) CreateEnvFile(context.Context, string) error       { return nil }
func (noopEnvLoader) EnvFileExists(string) (bool, error)                { return true, nil }

type recordingRuntime struct {
	ops        []string
	startErr   error
	containers []*domain.Container
}

func newRecordingRuntime() *recordingRuntime { return &recordingRuntime{} }
func (r *recordingRuntime) op(s string)      { r.ops = append(r.ops, s) }
func (r *recordingRuntime) CreateContainer(context.Context, *domain.ContainerConfig) (*domain.Container, error) {
	r.op("CreateContainer")
	return &domain.Container{ID: "new", Name: "gordon-app.example.com"}, nil
}
func (r *recordingRuntime) StartContainer(context.Context, string) error {
	r.op("StartContainer")
	return r.startErr
}
func (r *recordingRuntime) StopContainer(context.Context, string) error {
	r.op("StopContainer")
	return nil
}
func (r *recordingRuntime) RestartContainer(context.Context, string) error {
	r.op("RestartContainer")
	return nil
}
func (r *recordingRuntime) RemoveContainer(context.Context, string, bool) error {
	r.op("RemoveContainer")
	return nil
}
func (r *recordingRuntime) RenameContainer(context.Context, string, string) error {
	r.op("RenameContainer")
	return nil
}
func (r *recordingRuntime) ListContainers(context.Context, bool) ([]*domain.Container, error) {
	r.op("ListContainers")
	return r.containers, nil
}
func (r *recordingRuntime) InspectContainer(context.Context, string) (*domain.Container, error) {
	r.op("InspectContainer")
	return &domain.Container{ID: "new", Status: string(domain.ContainerStatusRunning), ImageID: "sha256:1"}, nil
}
func (r *recordingRuntime) GetContainerLogs(context.Context, string, bool) (io.ReadCloser, error) {
	r.op("GetContainerLogs")
	return io.NopCloser(nil), nil
}
func (r *recordingRuntime) PullImage(context.Context, string) error { r.op("PullImage"); return nil }
func (r *recordingRuntime) PullImageWithAuth(context.Context, string, string, string) error {
	r.op("PullImageWithAuth")
	return nil
}
func (r *recordingRuntime) TagImage(context.Context, string, string) error {
	r.op("TagImage")
	return nil
}
func (r *recordingRuntime) UntagImage(context.Context, string) error { r.op("UntagImage"); return nil }
func (r *recordingRuntime) RemoveImage(context.Context, string, bool) error {
	r.op("RemoveImage")
	return nil
}
func (r *recordingRuntime) ListImages(context.Context) ([]string, error) {
	r.op("ListImages")
	return nil, nil
}
func (r *recordingRuntime) Ping(context.Context) error              { r.op("Ping"); return nil }
func (r *recordingRuntime) Version(context.Context) (string, error) { r.op("Version"); return "", nil }
func (r *recordingRuntime) IsContainerRunning(context.Context, string) (bool, error) {
	r.op("IsContainerRunning")
	return true, nil
}
func (r *recordingRuntime) GetContainerHealthStatus(context.Context, string) (string, bool, error) {
	r.op("GetContainerHealthStatus")
	return "", false, nil
}
func (r *recordingRuntime) GetContainerPort(context.Context, string, int) (int, error) {
	r.op("GetContainerPort")
	return 0, nil
}
func (r *recordingRuntime) GetImageExposedPorts(context.Context, string) ([]int, error) {
	r.op("GetImageExposedPorts")
	return []int{8080}, nil
}
func (r *recordingRuntime) GetContainerExposedPorts(context.Context, string) ([]int, error) {
	r.op("GetContainerExposedPorts")
	return nil, nil
}
func (r *recordingRuntime) GetContainerNetworkInfo(context.Context, string) (string, int, error) {
	r.op("GetContainerNetworkInfo")
	return "", 0, nil
}
func (r *recordingRuntime) GetContainerNetwork(context.Context, string) (string, error) {
	r.op("GetContainerNetwork")
	return "", nil
}
func (r *recordingRuntime) InspectImageVolumes(context.Context, string) ([]string, error) {
	r.op("InspectImageVolumes")
	return nil, nil
}
func (r *recordingRuntime) VolumeExists(context.Context, string) (bool, error) {
	r.op("VolumeExists")
	return true, nil
}
func (r *recordingRuntime) CreateVolume(context.Context, string) error {
	r.op("CreateVolume")
	return nil
}
func (r *recordingRuntime) RemoveVolume(context.Context, string, bool) error {
	r.op("RemoveVolume")
	return nil
}
func (r *recordingRuntime) ListVolumes(context.Context) ([]*domain.VolumeInfo, error) {
	r.op("ListVolumes")
	return nil, nil
}
func (r *recordingRuntime) InspectImageEnv(context.Context, string) ([]string, error) {
	r.op("InspectImageEnv")
	return nil, nil
}
func (r *recordingRuntime) GetImageLabels(context.Context, string) (map[string]string, error) {
	r.op("GetImageLabels")
	return nil, nil
}
func (r *recordingRuntime) GetImageID(context.Context, string) (string, error) {
	r.op("GetImageID")
	return "sha256:1", nil
}
func (r *recordingRuntime) ExecInContainer(context.Context, string, []string) (*out.ExecResult, error) {
	r.op("ExecInContainer")
	return nil, nil
}
func (r *recordingRuntime) CopyFromContainer(context.Context, string, string) (io.ReadCloser, error) {
	r.op("CopyFromContainer")
	return io.NopCloser(nil), nil
}
func (r *recordingRuntime) CreateNetwork(context.Context, string, domain.NetworkConfig) error {
	r.op("CreateNetwork")
	return nil
}
func (r *recordingRuntime) RemoveNetwork(context.Context, string) error {
	r.op("RemoveNetwork")
	return nil
}
func (r *recordingRuntime) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	r.op("ListNetworks")
	return nil, nil
}
func (r *recordingRuntime) NetworkExists(context.Context, string) (bool, error) {
	r.op("NetworkExists")
	return true, nil
}
func (r *recordingRuntime) ConnectContainerToNetwork(context.Context, string, string) error {
	r.op("ConnectContainerToNetwork")
	return nil
}
func (r *recordingRuntime) DisconnectContainerFromNetwork(context.Context, string, string) error {
	r.op("DisconnectContainerFromNetwork")
	return nil
}
