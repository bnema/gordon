package apptraffic_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
)

// internalActive is one app whose only HTTP interface is internal.
func internalActive() domain.AppActive {
	return domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"api": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Container:         "c-api",
				Spec: domain.AppService{
					HTTP: []domain.AppHTTPInterface{{Port: 8080, Visibility: domain.AppVisibilityInternal}},
				},
			},
		},
	}
}

// TestProject_InternalHTTPExcluded proves an internal HTTP interface
// projects no route: it can never appear in the host index, so it also
// yields no certificate host and no TLS mode.
func TestProject_InternalHTTPExcluded(t *testing.T) {
	entries, err := apptraffic.Project("blog", internalActive(), nil)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestProject_MixedVisibilityProjectsOnlyPublic proves a service with both
// planes projects only the public interface.
func TestProject_MixedVisibilityProjectsOnlyPublic(t *testing.T) {
	active := internalActive()
	svc := active.Services["api"]
	svc.BackendBinds = map[int]int{8080: 18080}
	svc.Spec.HTTP = []domain.AppHTTPInterface{
		{Host: "blog.example.com", Port: 8080, TLS: "auto", Visibility: domain.AppVisibilityPublic},
		{Port: 9090, Visibility: domain.AppVisibilityInternal},
	}
	active.Services["api"] = svc

	entries, err := apptraffic.Project("blog", active, nil)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "blog.example.com", entries[0].Host)
	assert.Equal(t, 8080, entries[0].Backend.ContainerPort)
}
