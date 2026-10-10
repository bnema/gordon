package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

const (
	testFileSecretValue = "file-s3cr3t-value"
	testSecretFileBase  = "/data/secrets-run"
)

// fileSecretService returns a service with one env and one file secret,
// wired to mocks returning "env-only" and testFileSecretValue.
func fileSecretService(t *testing.T, deps Deps) (*Service, pinnedService) {
	t.Helper()
	state := outmocks.NewMockAppState(t)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Maybe()
	secrets := outmocks.NewMockSecretProvider(t)
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-1/web/a").Return("env-only", nil).Maybe()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-1/web/b").Return(testFileSecretValue, nil).Maybe()
	deps.State = state
	deps.Secrets = secrets
	p := pinnedService{name: "web", appEnv: map[string]string{"APP_ENV": "production"}, spec: domain.AppService{
		Name:        "web",
		Secrets:     map[string]string{"A": "a", "B": "b"},
		SecretFiles: []string{"B"},
	}}
	return NewService(deps, zerowrap.Default()), p
}

func TestServiceEnv_SplitsFileSecrets(t *testing.T) {
	svc, p := fileSecretService(t, Deps{})

	env, files, err := svc.serviceEnv(context.Background(), "blog", p)
	require.NoError(t, err)
	assert.Equal(t, []string{"A=env-only", "APP_ENV=production", "B_FILE=/run/secrets/B"}, env)
	assert.Equal(t, map[string]string{"B": testFileSecretValue}, files)
	for _, entry := range env {
		assert.NotContains(t, entry, testFileSecretValue)
	}
}

func TestServiceEnv_FileKeyOverlapFails(t *testing.T) {
	svc, p := fileSecretService(t, Deps{})
	p.appEnv = map[string]string{"B_FILE": "x"}

	_, _, err := svc.serviceEnv(context.Background(), "blog", p)
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
}

func TestCreateWithSecretFiles_MountsReadOnlyFiles(t *testing.T) {
	store := outmocks.NewMockSecretFileStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())
	admin := domain.ContainerBind{Name: "config", Source: "/srv/config", Destination: "/etc/app", ReadOnly: true}
	config := &domain.ContainerConfig{Name: "gordon-blog--web--abc", Binds: []domain.ContainerBind{admin}}

	store.EXPECT().Write(mock.Anything, "blog", "gordon-blog--web--abc", map[string]string{"B": testFileSecretValue}).
		Return(map[string]string{"B": testSecretFileBase + "/blog/gordon-blog--web--abc/B"}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(c *domain.ContainerConfig) bool {
		return assert.ObjectsAreEqual([]domain.ContainerBind{admin, {
			Name:        "secret-file-B",
			Source:      testSecretFileBase + "/blog/gordon-blog--web--abc/B",
			Destination: "/run/secrets/B",
			ReadOnly:    true,
		}}, c.Binds)
	})).Return(&domain.Container{ID: "c-1"}, nil).Once()

	created, err := svc.createWithSecretFiles(context.Background(), "blog", "web", config, map[string]string{"B": testFileSecretValue})
	require.NoError(t, err)
	assert.Equal(t, "c-1", created.ID)
	assert.Equal(t, []domain.ContainerBind{admin}, config.Binds, "caller config is not mutated")
}

func TestCreateWithSecretFiles_CreateFailureRemovesFiles(t *testing.T) {
	store := outmocks.NewMockSecretFileStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())
	config := &domain.ContainerConfig{Name: "gordon-blog--web--abc"}
	path := testSecretFileBase + "/blog/gordon-blog--web--abc/B"

	store.EXPECT().Write(mock.Anything, "blog", "gordon-blog--web--abc", mock.Anything).
		Return(map[string]string{"B": path}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).
		Return(nil, errors.New("create failed: mount "+path+" "+testFileSecretValue)).Once()
	store.EXPECT().Remove(mock.Anything, "blog", "gordon-blog--web--abc").Return(nil).Once()

	_, err := svc.createWithSecretFiles(context.Background(), "blog", "web", config, map[string]string{"B": testFileSecretValue})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testFileSecretValue)
	assert.NotContains(t, err.Error(), path)
}

func TestCreateWithSecretFiles_WriteFailureSkipsCreate(t *testing.T) {
	store := outmocks.NewMockSecretFileStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())

	store.EXPECT().Write(mock.Anything, "blog", "c", mock.Anything).Return(nil, errors.New("disk full")).Once()

	_, err := svc.createWithSecretFiles(context.Background(), "blog", "web", &domain.ContainerConfig{Name: "c"}, map[string]string{"B": testFileSecretValue})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testFileSecretValue)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestCreateWithSecretFiles_CleanupFailureKeepsCreateError(t *testing.T) {
	store := outmocks.NewMockSecretFileStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())

	store.EXPECT().Write(mock.Anything, "blog", "c", mock.Anything).
		Return(map[string]string{"B": testSecretFileBase + "/blog/c/B"}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(nil, errors.New("create failed")).Once()
	store.EXPECT().Remove(mock.Anything, "blog", "c").Return(errors.New("busy")).Once()

	_, err := svc.createWithSecretFiles(context.Background(), "blog", "web", &domain.ContainerConfig{Name: "c"}, map[string]string{"B": "v"})
	require.Error(t, err)
	// The create error is returned; the cleanup error is only logged.
	assert.Contains(t, err.Error(), `"web"`)
	assert.Contains(t, err.Error(), "runtime error redacted")
	assert.NotContains(t, err.Error(), "busy")
}

func TestCreateWithSecretFiles_NilStoreRefuses(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewService(Deps{Runtime: runtime}, zerowrap.Default())

	_, err := svc.createWithSecretFiles(context.Background(), "blog", "web", &domain.ContainerConfig{Name: "c"}, map[string]string{"B": "v"})
	require.Error(t, err)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestCheckSecrets_FileSecretsNeedStore(t *testing.T) {
	secrets := outmocks.NewMockSecretProvider(t)
	svc := NewService(Deps{Secrets: secrets}, zerowrap.Default())

	err := svc.checkSecrets(context.Background(), "blog", "app-1", domain.AppService{
		Name: "web", Secrets: map[string]string{"B": "b"}, SecretFiles: []string{"B"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no secret file store")
}

func TestEnvCurrent_ComparesSecretFiles(t *testing.T) {
	running := &domain.Container{
		Name: "gordon-blog--web--abc",
		Env:  []string{"APP_ENV=production", "A=env-only", "B_FILE=/run/secrets/B", "PATH=/bin"},
	}
	cases := []struct {
		name string
		read func(*outmocks.MockSecretFileStore)
		want bool
	}{
		{"same content kept", func(m *outmocks.MockSecretFileStore) {
			m.EXPECT().Read(mock.Anything, "blog", "gordon-blog--web--abc", "B").Return(testFileSecretValue, nil).Once()
		}, true},
		{"changed content replaced", func(m *outmocks.MockSecretFileStore) {
			m.EXPECT().Read(mock.Anything, "blog", "gordon-blog--web--abc", "B").Return("old", nil).Once()
		}, false},
		{"read error replaced", func(m *outmocks.MockSecretFileStore) {
			m.EXPECT().Read(mock.Anything, "blog", "gordon-blog--web--abc", "B").Return("", errors.New("gone")).Once()
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := outmocks.NewMockSecretFileStore(t)
			tc.read(store)
			svc, p := fileSecretService(t, Deps{SecretFiles: store})
			assert.Equal(t, tc.want, svc.envCurrent(context.Background(), "blog", p, running))
		})
	}

	t.Run("nil store replaced", func(t *testing.T) {
		svc, p := fileSecretService(t, Deps{})
		assert.False(t, svc.envCurrent(context.Background(), "blog", p, running))
	})
}

func TestRedactDiagnostics_RedactsFileSecrets(t *testing.T) {
	svc, p := fileSecretService(t, Deps{})

	got := svc.redactDiagnostics(context.Background(), "blog", p, []string{"loaded " + testFileSecretValue, "ok"})
	assert.Equal(t, []string{"loaded [redacted]", "ok"}, got)
}

func TestSweepSecretFiles(t *testing.T) {
	sets := []out.SecretFileSet{
		{App: "blog", Container: "gordon-blog--web--a"},
		{App: "blog", Container: "gordon-blog--web--b"},
		{App: "other", Container: "gordon-other--x--c"},
	}
	live := []*domain.Container{{Name: "gordon-blog--web--b"}, {Name: "unrelated"}}

	t.Run("app scoped", func(t *testing.T) {
		store := outmocks.NewMockSecretFileStore(t)
		runtime := outmocks.NewMockContainerRuntime(t)
		store.EXPECT().List(mock.Anything).Return(sets, nil).Once()
		runtime.EXPECT().ListContainers(mock.Anything, true).Return(live, nil).Once()
		store.EXPECT().Remove(mock.Anything, "blog", "gordon-blog--web--a").Return(nil).Once()

		svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())
		require.NoError(t, svc.sweepSecretFiles(context.Background(), "blog"))
	})

	t.Run("all apps", func(t *testing.T) {
		store := outmocks.NewMockSecretFileStore(t)
		runtime := outmocks.NewMockContainerRuntime(t)
		store.EXPECT().List(mock.Anything).Return(sets, nil).Once()
		runtime.EXPECT().ListContainers(mock.Anything, true).Return(live, nil).Once()
		store.EXPECT().Remove(mock.Anything, "blog", "gordon-blog--web--a").Return(nil).Once()
		store.EXPECT().Remove(mock.Anything, "other", "gordon-other--x--c").Return(nil).Once()

		svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())
		require.NoError(t, svc.SweepSecretFiles(context.Background()))
	})

	t.Run("no sets skips runtime", func(t *testing.T) {
		store := outmocks.NewMockSecretFileStore(t)
		runtime := outmocks.NewMockContainerRuntime(t)
		store.EXPECT().List(mock.Anything).Return(sets, nil).Once()

		svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())
		require.NoError(t, svc.sweepSecretFiles(context.Background(), "ghost"))
		runtime.AssertNotCalled(t, "ListContainers", mock.Anything, mock.Anything)
	})

	t.Run("nil store", func(t *testing.T) {
		svc := NewService(Deps{}, zerowrap.Default())
		require.NoError(t, svc.SweepSecretFiles(context.Background()))
	})

	t.Run("runtime list error removes nothing", func(t *testing.T) {
		store := outmocks.NewMockSecretFileStore(t)
		runtime := outmocks.NewMockContainerRuntime(t)
		store.EXPECT().List(mock.Anything).Return(sets, nil).Once()
		runtime.EXPECT().ListContainers(mock.Anything, true).Return(nil, errors.New("daemon down")).Once()

		svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())
		require.Error(t, svc.SweepSecretFiles(context.Background()))
		store.AssertNotCalled(t, "Remove", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("partial remove failure continues", func(t *testing.T) {
		store := outmocks.NewMockSecretFileStore(t)
		runtime := outmocks.NewMockContainerRuntime(t)
		store.EXPECT().List(mock.Anything).Return(sets, nil).Once()
		runtime.EXPECT().ListContainers(mock.Anything, true).Return(live, nil).Once()
		store.EXPECT().Remove(mock.Anything, "blog", "gordon-blog--web--a").Return(errors.New("busy")).Once()
		store.EXPECT().Remove(mock.Anything, "other", "gordon-other--x--c").Return(nil).Once()

		svc := NewService(Deps{Runtime: runtime, SecretFiles: store}, zerowrap.Default())
		require.Error(t, svc.SweepSecretFiles(context.Background()))
	})
}

func TestRetireContainer_SweepsSecretFiles(t *testing.T) {
	t.Run("gone sweeps", func(t *testing.T) {
		state := outmocks.NewMockAppState(t)
		runtime := outmocks.NewMockContainerRuntime(t)
		store := outmocks.NewMockSecretFileStore(t)
		runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", true).Return(nil).Once()
		state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()
		store.EXPECT().List(mock.Anything).Return([]out.SecretFileSet{{App: "blog", Container: "gordon-blog--web--a"}}, nil).Once()
		runtime.EXPECT().ListContainers(mock.Anything, true).Return(nil, nil).Once()
		store.EXPECT().Remove(mock.Anything, "blog", "gordon-blog--web--a").Return(nil).Once()

		svc := NewService(Deps{State: state, Runtime: runtime, SecretFiles: store}, zerowrap.Default())
		retired := svc.retireContainer(context.Background(), "blog", retireOptions{Service: "web", Force: true}, "c-1")
		assert.True(t, retired.Gone)
		assert.Empty(t, retired.Warnings)
	})

	t.Run("sweep failure is a fixed warning", func(t *testing.T) {
		state := outmocks.NewMockAppState(t)
		runtime := outmocks.NewMockContainerRuntime(t)
		store := outmocks.NewMockSecretFileStore(t)
		runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", true).Return(nil).Once()
		state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()
		store.EXPECT().List(mock.Anything).Return(nil, errors.New("open "+testSecretFileBase+": denied")).Once()

		svc := NewService(Deps{State: state, Runtime: runtime, SecretFiles: store}, zerowrap.Default())
		retired := svc.retireContainer(context.Background(), "blog", retireOptions{Service: "web", Force: true}, "c-1")
		assert.True(t, retired.Gone)
		require.Len(t, retired.Warnings, 1)
		assert.Equal(t, "secret files cleanup failed", retired.Warnings[0].Detail)
	})

	t.Run("not gone does not sweep", func(t *testing.T) {
		state := outmocks.NewMockAppState(t)
		runtime := outmocks.NewMockContainerRuntime(t)
		store := outmocks.NewMockSecretFileStore(t)
		runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", true).Return(errors.New("busy")).Once()

		svc := NewService(Deps{State: state, Runtime: runtime, SecretFiles: store}, zerowrap.Default())
		retired := svc.retireContainer(context.Background(), "blog", retireOptions{Service: "web", Force: true}, "c-1")
		assert.False(t, retired.Gone)
		store.AssertNotCalled(t, "List", mock.Anything)
	})
}
