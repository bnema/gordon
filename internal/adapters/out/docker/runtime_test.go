package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntime_InspectImageEnv_RedactsValuesInDebugLog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/images/")
		assert.Contains(t, r.URL.Path, "/json")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Config": {
				"Env": ["API_KEY=super-secret-key", "DATABASE_URL=postgres://user:pass@example/db", "PORT=8080", "MALFORMED_SECRET_VALUE", "=empty-key-secret"]
			}
		}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	runtime := NewRuntimeWithClient(cli)

	var logs bytes.Buffer
	log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &logs})
	ctx := zerowrap.WithCtx(context.Background(), log)

	envVars, err := runtime.InspectImageEnv(ctx, "example/app:latest")
	require.NoError(t, err)
	assert.Equal(t, []string{"API_KEY=super-secret-key", "DATABASE_URL=postgres://user:pass@example/db", "PORT=8080", "MALFORMED_SECRET_VALUE", "=empty-key-secret"}, envVars)

	logOutput := logs.String()
	assert.Contains(t, logOutput, "env_keys")
	assert.Contains(t, logOutput, "API_KEY")
	assert.Contains(t, logOutput, "DATABASE_URL")
	assert.Contains(t, logOutput, "PORT")
	assert.Contains(t, logOutput, "[malformed]")
	assert.NotContains(t, logOutput, "super-secret-key")
	assert.NotContains(t, logOutput, "postgres://user:pass@example/db")
	assert.NotContains(t, logOutput, "MALFORMED_SECRET_VALUE")
	assert.NotContains(t, logOutput, "empty-key-secret")
	assert.NotContains(t, logOutput, "API_KEY=super-secret-key")
}

func TestParseVolumeOptionsSeparatesAccessFromEngineOptions(t *testing.T) {
	assert.Nil(t, parseVolumeOptions("ro"))
	assert.Nil(t, parseVolumeOptions("rw"))
	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, parseVolumeOptions("ro,U"))
	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, parseVolumeOptions("rw,U"))
}

func TestNormalizeInspectedGenerationVolumeChownAcceptsOnlyCanonicalPodmanMode(t *testing.T) {
	inspected := canonicalGenerationContainer()
	normalizeInspectedGenerationVolumeChown(inspected, []string{
		"gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind",
	}, nativePodmanSecurityProof{boundingCapsNull: true}, true)

	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, inspected.VolumeMounts[0].Options)
}

func TestNormalizeInspectedGenerationVolumeChownRejectsModeVariants(t *testing.T) {
	variants := map[string][]string{
		"missing":                 nil,
		"bare create mode":        {"gordon-runtime-migration-g1:/var/lib/gordon:U"},
		"subset":                  {"gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev"},
		"superset":                {"gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind,z"},
		"reordered":               {"gordon-runtime-migration-g1:/var/lib/gordon:U,nosuid,rprivate,nodev,rbind"},
		"duplicate token":         {"gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind,rbind"},
		"lowercase alias":         {"gordon-runtime-migration-g1:/var/lib/gordon:u,rprivate,nosuid,nodev,rbind"},
		"extra access flag":       {"gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind,rw"},
		"bind source":             {"/srv/gordon:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind"},
		"other volume":            {"other-volume:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind"},
		"other destination":       {"gordon-runtime-migration-g1:/data:U,rprivate,nosuid,nodev,rbind"},
		"destination alias":       {"gordon-runtime-migration-g1:/var/lib/./gordon:U,rprivate,nosuid,nodev,rbind"},
		"duplicate bind":          {"gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind", "gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind"},
		"conflicting destination": {"gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind", "gordon-runtime-migration-g1:/var/lib/gordon:rw"},
	}
	for name, binds := range variants {
		t.Run(name, func(t *testing.T) {
			inspected := canonicalGenerationContainer()
			normalizeInspectedGenerationVolumeChown(inspected, binds, nativePodmanSecurityProof{boundingCapsNull: true}, true)
			assert.Nil(t, inspected.VolumeMounts[0].Options)
		})
	}
}

func TestNormalizeInspectedGenerationVolumeChownRequiresNativeProofAndExactGenerationMount(t *testing.T) {
	tests := map[string]func(*domain.Container) (nativePodmanSecurityProof, bool){
		"proof absent": func(_ *domain.Container) (nativePodmanSecurityProof, bool) { return nativePodmanSecurityProof{}, false },
		"bounding caps unproved": func(_ *domain.Container) (nativePodmanSecurityProof, bool) {
			return nativePodmanSecurityProof{}, true
		},
		"noncanonical container ID": func(container *domain.Container) (nativePodmanSecurityProof, bool) {
			container.ID = "existing"
			return nativePodmanSecurityProof{boundingCapsNull: true}, true
		},
		"non-generation name": func(container *domain.Container) (nativePodmanSecurityProof, bool) {
			container.Name = "gordon-runtime-other-g1"
			return nativePodmanSecurityProof{boundingCapsNull: true}, true
		},
		"wrong volume": func(container *domain.Container) (nativePodmanSecurityProof, bool) {
			container.VolumeMounts[0].Name = "other-volume"
			return nativePodmanSecurityProof{boundingCapsNull: true}, true
		},
		"bind mount": func(container *domain.Container) (nativePodmanSecurityProof, bool) {
			container.VolumeMounts[0].Type = string(mount.TypeBind)
			return nativePodmanSecurityProof{boundingCapsNull: true}, true
		},
		"read only": func(container *domain.Container) (nativePodmanSecurityProof, bool) {
			container.VolumeMounts[0].ReadOnly = true
			return nativePodmanSecurityProof{boundingCapsNull: true}, true
		},
		"wrong destination": func(container *domain.Container) (nativePodmanSecurityProof, bool) {
			container.VolumeMounts[0].Destination = "/data"
			return nativePodmanSecurityProof{boundingCapsNull: true}, true
		},
		"existing mount option": func(container *domain.Container) (nativePodmanSecurityProof, bool) {
			container.VolumeMounts[0].Options = []string{"delegated"}
			return nativePodmanSecurityProof{boundingCapsNull: true}, true
		},
		"edge role": func(container *domain.Container) (nativePodmanSecurityProof, bool) {
			container.Labels[domain.LabelComponentRole] = string(domain.ComponentRoleEdge)
			container.Name = "gordon-edge-migration-g1"
			container.VolumeMounts[0].Name = container.Name
			return nativePodmanSecurityProof{boundingCapsNull: true}, true
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			inspected := canonicalGenerationContainer()
			proof, ok := mutate(inspected)
			normalizeInspectedGenerationVolumeChown(inspected, []string{
				inspected.VolumeMounts[0].Name + ":/var/lib/gordon:U,rprivate,nosuid,nodev,rbind",
			}, proof, ok)
			assert.NotEqual(t, []string{domain.ContainerVolumeOptionChown}, inspected.VolumeMounts[0].Options)
		})
	}
}

func TestNormalizeInspectedGenerationVolumeChownPreservesOtherMountOptions(t *testing.T) {
	inspected := canonicalGenerationContainer()
	inspected.VolumeMounts = append(inspected.VolumeMounts, domain.ContainerVolumeMount{
		Type: string(mount.TypeBind), Source: "/host/config", Destination: "/config", Mode: "ro,z", Options: []string{"z"}, ReadOnly: true,
	})
	normalizeInspectedGenerationVolumeChown(inspected, []string{
		"gordon-runtime-migration-g1:/var/lib/gordon:U,rprivate,nosuid,nodev,rbind",
		"/host/config:/config:ro",
	}, nativePodmanSecurityProof{boundingCapsNull: true}, true)

	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, inspected.VolumeMounts[0].Options)
	assert.Equal(t, []string{"z"}, inspected.VolumeMounts[1].Options)
}

func canonicalGenerationContainer() *domain.Container {
	return &domain.Container{
		ID:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Name: "gordon-runtime-migration-g1",
		Labels: map[string]string{
			domain.LabelComponent:            "true",
			domain.LabelComponentRole:        string(domain.ComponentRoleRuntime),
			domain.LabelComponentMigrationID: "migration",
			domain.LabelComponentGeneration:  "1",
		},
		VolumeMounts: []domain.ContainerVolumeMount{{
			Type: string(mount.TypeVolume), Name: "gordon-runtime-migration-g1", Destination: "/var/lib/gordon",
		}},
	}
}

func TestInspectVolumeOptionsPreservesNonChownOptionsWithoutTrustingMountModeU(t *testing.T) {
	assert.Nil(t, inspectVolumeOptions(container.MountPoint{Mode: "rw,U"}))
	assert.Equal(t, []string{"z", "delegated"}, inspectVolumeOptions(container.MountPoint{Mode: "ro,z,delegated,U"}))
}

func TestBuildVolumeBindsReturnsVolumeOptionValidationErrors(t *testing.T) {
	config := &domain.ContainerConfig{
		Volumes:       map[string]string{"/var/lib/gordon": "gordon-runtime-fixture-g1"},
		VolumeOptions: map[string][]string{"/var/lib/gordon": {"z"}},
	}

	binds, err := buildVolumeBinds(config, zerowrap.FromCtx(t.Context()))

	require.Error(t, err)
	assert.Nil(t, binds)
}

func TestRuntimeVolumeOptionsRequireCanonicalSingletonChown(t *testing.T) {
	engines := []struct {
		name            string
		version         string
		info            string
		canonicalCreate bool
	}{
		{name: "rootless Podman", version: `{"Components":[{"Name":"Podman Engine"}]}`, info: `{"Rootless":true,"SecurityOptions":["name=rootless"]}`, canonicalCreate: true},
		{name: "Docker", version: `{"Components":[{"Name":"Docker Engine"}]}`, info: `{"Rootless":true,"SecurityOptions":["name=rootless"]}`},
		{name: "rootful Podman", version: `{"Components":[{"Name":"Podman Engine"}]}`, info: `{"Rootless":false,"SecurityOptions":[]}`},
	}
	invalid := []struct {
		name    string
		options []string
	}{
		{name: "comma containing", options: []string{"U,ro"}},
		{name: "composite comma duplicate", options: []string{"U,U"}},
		{name: "empty token", options: []string{""}},
		{name: "empty entry", options: []string{}},
		{name: "duplicate", options: []string{"U", "U"}},
		{name: "mixed", options: []string{"U", "ro"}},
		{name: "read only access flag", options: []string{"ro"}},
		{name: "writable access flag", options: []string{"rw"}},
		{name: "unknown", options: []string{"z"}},
		{name: "whitespace", options: []string{" U "}},
		{name: "wrong case", options: []string{"u"}},
	}

	for _, engine := range engines {
		t.Run(engine.name+"/canonical", func(t *testing.T) {
			created, probed, binds, err := createContainerWithVolumeOptions(t, engine.version, engine.info, []string{domain.ContainerVolumeOptionChown}, false)
			assert.True(t, probed)
			assert.Equal(t, engine.canonicalCreate, created)
			if engine.canonicalCreate {
				require.NoError(t, err)
				assert.Equal(t, []string{"gordon-runtime-fixture-g1:/var/lib/gordon:U"}, binds)
				return
			}
			require.Error(t, err)
		})
		for _, malformed := range invalid {
			t.Run(engine.name+"/"+malformed.name, func(t *testing.T) {
				created, probed, _, err := createContainerWithVolumeOptions(t, engine.version, engine.info, malformed.options, false)
				require.Error(t, err)
				assert.False(t, probed, "invalid tokens must be rejected before engine preflight")
				assert.False(t, created, "invalid tokens must never reach container serialization")
			})
		}
	}
}

func TestRuntimeRejectsChownOptionOnReadOnlyMount(t *testing.T) {
	created, probed, _, err := createContainerWithVolumeOptions(
		t,
		`{"Components":[{"Name":"Podman Engine"}]}`,
		`{"Rootless":true,"SecurityOptions":["name=rootless"]}`,
		[]string{domain.ContainerVolumeOptionChown},
		true,
	)
	require.Error(t, err)
	assert.False(t, probed, "U is valid only on a writable generation volume")
	assert.False(t, created, "read-only mounts must never serialize U")
}

func createContainerWithVolumeOptions(t *testing.T, version, info string, options []string, readOnly bool) (bool, bool, []string, error) {
	t.Helper()
	var created atomic.Bool
	var probed atomic.Bool
	var binds []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/version":
			probed.Store(true)
			_, _ = w.Write([]byte(version))
		case "/v1.41/info":
			probed.Store(true)
			_, _ = w.Write([]byte(info))
		case "/v1.41/containers/create":
			created.Store(true)
			var payload struct {
				HostConfig struct {
					Binds []string
				}
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			binds = payload.HostConfig.Binds
			_, _ = w.Write([]byte(`{"Id":"created"}`))
		case "/v1.41/containers/created/json":
			_, _ = w.Write([]byte(`{"Id":"created","Name":"/gordon-runtime-fixture-g1","Image":"sha256:fixture","Created":"2026-05-05T00:00:00Z","Config":{"Image":"gordon:fixture"},"State":{"Status":"created","ExitCode":0},"NetworkSettings":{"Ports":{}}}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	apiClient, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	volumes := map[string]string{"/var/lib/gordon": "gordon-runtime-fixture-g1"}
	var readOnlyVolumes map[string]string
	if readOnly {
		readOnlyVolumes = volumes
		volumes = nil
	}
	_, err = NewRuntimeWithClient(apiClient).CreateContainer(t.Context(), &domain.ContainerConfig{
		Image: "gordon:fixture", Name: "gordon-runtime-fixture-g1",
		Volumes:         volumes,
		ReadOnlyVolumes: readOnlyVolumes,
		VolumeOptions:   map[string][]string{"/var/lib/gordon": options},
	})
	return created.Load(), probed.Load(), binds, err
}

func TestWaitForVolumeArchiveContainerIgnoresNilErrorBeforeStatus(t *testing.T) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	errCh <- nil
	statusCh <- container.WaitResponse{StatusCode: 7}

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.NoError(t, err)
	assert.Equal(t, int64(7), statusCode)
}

func TestWaitForVolumeArchiveContainerHandlesClosedErrorChannelBeforeStatus(t *testing.T) {
	statusCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error)
	close(errCh)
	statusCh <- container.WaitResponse{StatusCode: 3}

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.NoError(t, err)
	assert.Equal(t, int64(3), statusCode)
}

func TestWaitForVolumeArchiveContainerReturnsErrorChannelError(t *testing.T) {
	statusCh := make(chan container.WaitResponse)
	errCh := make(chan error, 1)
	wantErr := errors.New("wait failed")
	errCh <- wantErr

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, int64(0), statusCode)
}

func TestWaitForVolumeArchiveContainerErrorsWhenStatusChannelCloses(t *testing.T) {
	statusCh := make(chan container.WaitResponse)
	errCh := make(chan error)
	close(statusCh)
	close(errCh)

	statusCode, err := waitForVolumeArchiveContainer(statusCh, errCh)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait channel closed")
	assert.Equal(t, int64(0), statusCode)
}
