package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntime_InventoryRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.41/containers/json":
			assert.Equal(t, "1", r.URL.Query().Get("all"))
			_, _ = w.Write([]byte(`[
				{"Id":"c-running","Names":["/shop-web-1"],"Image":"registry.example/shop:1","ImageID":"sha256:aaa","State":"running",
				 "Mounts":[{"Type":"volume","Name":"gordon-shop--web--vol--data"}]},
				{"Id":"c-stopped","Names":["/shop-web-2"],"Image":"registry.example/shop:2","ImageID":"sha256:bbb","State":"exited","Mounts":[]},
				{"Id":"c-opaque","Names":["/foreign"],"Image":"","ImageID":"","State":"exited","Mounts":[]}
			]`))
		case "/v1.41/images/json":
			assert.Equal(t, "1", r.URL.Query().Get("all"))
			_, _ = w.Write([]byte(`[
				{"Id":"sha256:aaa","RepoTags":["registry.example/shop:1","registry.example/shop:latest"],
				 "RepoDigests":["registry.example/shop@sha256:1111"],"Labels":{"gordon.app":"shop"},"Size":10,"Created":1700000000},
				{"Id":"sha256:bbb","RepoTags":["<none>:<none>"],"RepoDigests":[],"Labels":{},"Size":20,"Created":1700000000}
			]`))
		case "/v1.41/volumes":
			_, _ = w.Write([]byte(`{"Volumes":[
				{"Name":"gordon-shop--web--vol--data","Driver":"local","Mountpoint":"/var/lib/docker/volumes/x/_data","CreatedAt":"2026-01-01T00:00:00Z","Labels":{"gordon.managed":"true","gordon.app":"shop"}},
				{"Name":"pgdata","Driver":"local","Mountpoint":"/var/lib/docker/volumes/pgdata/_data","CreatedAt":"not-a-time","Labels":{}}
			]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	rt := newRuntimeForHTTPServer(t, server)
	inventory, err := rt.InventoryRuntime(context.Background())
	require.NoError(t, err)
	require.True(t, inventory.Complete(), "inventory gaps: %+v", inventory.Gaps)

	require.Len(t, inventory.Containers, 3)
	byContainer := map[string]domain.RuntimeContainerUse{}
	for _, use := range inventory.Containers {
		byContainer[use.ContainerID] = use
	}
	assert.True(t, byContainer["c-running"].Running)
	assert.False(t, byContainer["c-stopped"].Running)
	assert.Equal(t, "sha256:bbb", byContainer["c-stopped"].ImageID)
	assert.True(t, byContainer["c-opaque"].ImageUnknown, "unresolvable container image must be flagged")

	require.Len(t, inventory.Images, 2)
	byImage := map[string]domain.RuntimeImage{}
	for _, image := range inventory.Images {
		byImage[image.ID] = image
	}
	// One image ID may carry several repo tags and repo digests.
	assert.Equal(t, []string{"registry.example/shop:1", "registry.example/shop:latest"}, byImage["sha256:aaa"].RepoTags)
	assert.Equal(t, []string{"registry.example/shop@sha256:1111"}, byImage["sha256:aaa"].RepoDigests)
	assert.Equal(t, "<none>:<none>", byImage["sha256:bbb"].RepoTags[0])

	require.Len(t, inventory.Volumes, 2)
	byVolume := map[string]*domain.VolumeInfo{}
	for _, volume := range inventory.Volumes {
		byVolume[volume.Name] = volume
	}
	assert.True(t, byVolume["gordon-shop--web--vol--data"].InUse)
	assert.Equal(t, []string{"shop-web-1"}, byVolume["gordon-shop--web--vol--data"].Containers)
	assert.Equal(t, "shop", byVolume["gordon-shop--web--vol--data"].Labels[domain.LabelApp])
	assert.False(t, byVolume["pgdata"].InUse)
	assert.Empty(t, byVolume["pgdata"].Labels)
}

func TestRuntime_InventoryRuntimeReportsGapsNotAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1.41/images/json" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		switch r.URL.Path {
		case "/v1.41/containers/json":
			_, _ = w.Write([]byte(`[]`))
		case "/v1.41/volumes":
			_, _ = w.Write([]byte(`{"Volumes":[]}`))
		}
	}))
	defer server.Close()

	rt := newRuntimeForHTTPServer(t, server)
	inventory, err := rt.InventoryRuntime(context.Background())
	require.NoError(t, err)
	require.False(t, inventory.Complete())
	require.Len(t, inventory.Gaps, 1)
	assert.Equal(t, domain.InventorySourceRuntimeImages, inventory.Gaps[0].Source)
	assert.Equal(t, domain.PruneReasonUnknownInventory, inventory.Gaps[0].Reason)
}

func TestRuntime_InventoryRuntimeContainerListFailureProtectsImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.41/containers/json":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		case "/v1.41/images/json":
			_, _ = w.Write([]byte(`[]`))
		case "/v1.41/volumes":
			_, _ = w.Write([]byte(`{"Volumes":[]}`))
		}
	}))
	defer server.Close()

	rt := newRuntimeForHTTPServer(t, server)
	inventory, err := rt.InventoryRuntime(context.Background())
	require.NoError(t, err)
	require.False(t, inventory.Complete())
	require.Len(t, inventory.Gaps, 1)
	assert.Equal(t, domain.InventorySourceRuntimeContainers, inventory.Gaps[0].Source)
	assert.Equal(t, domain.PruneReasonUnknownContainerUse, inventory.Gaps[0].Reason)
}

func TestRuntime_RemoveImageExactUsesExactIdentityWithoutForce(t *testing.T) {
	var method, path string
	var force string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		force = r.URL.Query().Get("force")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	rt := newRuntimeForHTTPServer(t, server)
	ref, err := domain.NewRuntimeImageRef("sha256:deadbeef")
	require.NoError(t, err)
	require.NoError(t, rt.RemoveImageExact(context.Background(), ref))

	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "/v1.41/images/sha256:deadbeef", path)
	assert.NotEqual(t, "true", force)
	assert.NotEqual(t, "1", force)
}

func TestRuntime_RemoveVolumeExactUsesExactIdentityWithoutForce(t *testing.T) {
	var method, path string
	var force string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		force = r.URL.Query().Get("force")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	rt := newRuntimeForHTTPServer(t, server)
	ref, err := domain.NewRuntimeVolumeRef("gordon-shop--web--vol--data")
	require.NoError(t, err)
	require.NoError(t, rt.RemoveVolumeExact(context.Background(), ref))

	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "/v1.41/volumes/gordon-shop--web--vol--data", path)
	assert.NotEqual(t, "true", force)
	assert.NotEqual(t, "1", force)
}

func TestRuntime_ExactRemovalRejectsInvalidIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid identity must not reach the runtime: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	rt := newRuntimeForHTTPServer(t, server)
	err := rt.RemoveImageExact(context.Background(), domain.RuntimeImageRef{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid identity")

	err = rt.RemoveVolumeExact(context.Background(), domain.RuntimeVolumeRef{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid identity")

	err = rt.RemoveImageExact(context.Background(), domain.RuntimeImageRef{ID: "<none>"})
	require.Error(t, err)
}

func TestRuntime_ExactRemovalPropagatesRuntimeErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"volume is in use"}`))
	}))
	defer server.Close()

	rt := newRuntimeForHTTPServer(t, server)
	ref, err := domain.NewRuntimeVolumeRef("in-use")
	require.NoError(t, err)
	err = rt.RemoveVolumeExact(context.Background(), ref)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "remove volume") || strings.Contains(err.Error(), "in use"),
		"unexpected error: %v", err)
}
