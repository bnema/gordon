package backup

// Tests for app-identity database detection over ACTIVE-record sources.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestDetectAppDatabases_AppSources(t *testing.T) {
	sources := []AppDatabaseSource{
		{App: "blog", Service: "db", Name: "db", Image: "postgres:16", ContainerID: "ctr-db", Ports: []int{5432}, Hosts: []string{"blog.example.com"}},
		{App: "blog", Service: "cache", Name: "cache", Image: "redis:7", ContainerID: "ctr-cache", Ports: []int{6379}, Hosts: []string{"blog.example.com"}},
		{App: "blog", Service: "legacy", Name: "legacy", Image: "myregistry/custom:1", ContainerID: "ctr-legacy", Ports: []int{5432}, Hosts: []string{"blog.example.com"}},
	}

	dbs := DetectAppDatabases(sources)

	require.Len(t, dbs, 2, "postgres image + 5432 fallback; redis excluded")
	assert.Equal(t, domain.DBTypePostgreSQL, dbs[0].Type)
	assert.Equal(t, "16", dbs[0].Version)
	assert.Equal(t, "db", dbs[0].Name)
	assert.Equal(t, "ctr-db", dbs[0].ContainerID)
	assert.Equal(t, "blog", dbs[0].App)
	assert.Equal(t, "db", dbs[0].Service)
	assert.Empty(t, dbs[0].Domain, "domain storage key is filled by DetectDatabases, not detection")
	assert.Equal(t, "legacy", dbs[1].Service, "5432 fallback works without attachments")
}

func TestDetectAppDatabases_Empty(t *testing.T) {
	assert.Empty(t, DetectAppDatabases(nil))
	assert.Empty(t, DetectAppDatabases([]AppDatabaseSource{
		{App: "blog", Service: "web", Name: "web", Image: "blog:latest"},
	}))
}
