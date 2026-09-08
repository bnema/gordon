package backup

// Tests for attachment-free (app-identity) database detection: the same
// fixtures must resolve through the ACTIVE-record path and the legacy
// attachment path with identical database fields, differing only in
// identity (App/Service vs Domain).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestDetectAppDatabases_ParityWithAttachmentPath(t *testing.T) {
	attachments := []domain.Attachment{
		{Name: "db", Image: "postgres:16", ContainerID: "ctr-db", Ports: []int{5432}},
		{Name: "cache", Image: "redis:7", ContainerID: "ctr-cache", Ports: []int{6379}},
		{Name: "legacy", Image: "myregistry/custom:1", ContainerID: "ctr-legacy", Ports: []int{5432}},
	}
	sources := []AppDatabaseSource{
		{App: "blog", Service: "db", Name: "db", Image: "postgres:16", ContainerID: "ctr-db", Ports: []int{5432}},
		{App: "blog", Service: "cache", Name: "cache", Image: "redis:7", ContainerID: "ctr-cache", Ports: []int{6379}},
		{App: "blog", Service: "legacy", Name: "legacy", Image: "myregistry/custom:1", ContainerID: "ctr-legacy", Ports: []int{5432}},
	}

	var viaAttachments []domain.DBInfo
	for _, a := range attachments {
		if db, ok := detectDatabaseFromAttachment("blog.example.com", a); ok {
			viaAttachments = append(viaAttachments, db)
		}
	}
	viaApps := DetectAppDatabases(sources)

	require.Len(t, viaAttachments, 2)
	require.Len(t, viaApps, 2, "same rules, no attachments involved")
	for i := range viaApps {
		assert.Equal(t, viaAttachments[i].Type, viaApps[i].Type)
		assert.Equal(t, viaAttachments[i].Version, viaApps[i].Version)
		assert.Equal(t, viaAttachments[i].Name, viaApps[i].Name)
		assert.Equal(t, viaAttachments[i].Host, viaApps[i].Host)
		assert.Equal(t, viaAttachments[i].Port, viaApps[i].Port)
		assert.Equal(t, viaAttachments[i].ContainerID, viaApps[i].ContainerID)
		assert.Equal(t, viaAttachments[i].ImageName, viaApps[i].ImageName)
	}

	assert.Equal(t, "blog", viaApps[0].App)
	assert.Equal(t, "db", viaApps[0].Service)
	assert.Empty(t, viaApps[0].Domain, "domain stays empty until the cutover rewiring")
	assert.Equal(t, "16", viaApps[0].Version)
	assert.Equal(t, "legacy", viaApps[1].Service, "5432 fallback works without attachments")
}

func TestDetectAppDatabases_Empty(t *testing.T) {
	assert.Empty(t, DetectAppDatabases(nil))
	assert.Empty(t, DetectAppDatabases([]AppDatabaseSource{
		{App: "blog", Service: "web", Name: "web", Image: "blog:latest"},
	}))
}
