package deployment

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestCreateContainer_GuardRefusesHostNetwork(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())

	_, err := svc.createContainer(context.Background(), "web", &domain.ContainerConfig{Name: "c", NetworkMode: "host"})
	require.ErrorIs(t, err, domain.ErrRuntimeGuard)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestCreateContainer_GuardRefusesProtectedBind(t *testing.T) {
	protected := t.TempDir()
	child := filepath.Join(protected, "registry")
	require.NoError(t, os.MkdirAll(child, 0o700))
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime, Guard: domain.RuntimeGuard{ProtectedPaths: []string{protected}}}, zerowrap.Default())

	_, err := svc.createContainer(context.Background(), "web", &domain.ContainerConfig{
		Name:  "c",
		Binds: []domain.ContainerBind{{Name: "conf", Source: child, Destination: "/etc/app", ReadOnly: true}},
	})
	require.ErrorIs(t, err, domain.ErrRuntimeGuard)
	assert.NotContains(t, err.Error(), protected)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestCreateWithSecretFiles_GuardAllowsOwnSecretBind(t *testing.T) {
	dataDir := t.TempDir()
	base := filepath.Join(dataDir, "secrets-run")
	store := outmocks.NewMockSecretFileStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	guard := domain.RuntimeGuard{ProtectedPaths: []string{dataDir}, SecretFilesDir: base}
	svc := NewService(Deps{Runtime: runtime, SecretFiles: store, Guard: guard}, zerowrap.Default())
	config := &domain.ContainerConfig{Name: "gordon-blog--web--abc", Labels: appLabels("blog", "web", "rev-1")}

	store.EXPECT().Write(mock.Anything, "blog", "gordon-blog--web--abc", mock.Anything).
		Return(map[string]string{"B": filepath.Join(base, "blog", "gordon-blog--web--abc", "B")}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-1"}, nil).Once()

	_, err := svc.createWithSecretFiles(context.Background(), "blog", "web", config, map[string]string{"B": "v"})
	require.NoError(t, err)
}

func TestResolveGuardedBinds_RefusesProtectedSource(t *testing.T) {
	protected := t.TempDir()
	svc := NewService(Deps{Guard: domain.RuntimeGuard{ProtectedPaths: []string{protected}}}, zerowrap.Default()).
		WithBindPolicies(map[string]domain.AppBindPolicy{
			"config": bindPolicyFor(protected, protected, true, []string{"blog"}, []string{"web"}),
		})

	_, err := svc.resolveGuardedBinds("blog", domain.AppService{
		Name:  "web",
		Binds: []domain.AppBind{{Name: "config", Path: "/etc/app", ReadOnly: true}},
	})
	require.ErrorIs(t, err, domain.ErrRuntimeGuard)
	assert.NotContains(t, err.Error(), protected)
}
