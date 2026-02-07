package backup

import (
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectDatabaseFromAttachment_ExtractsPostgresVersion(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "postgres",
		Image:       "postgres:17.4-alpine",
		ContainerID: "abc123",
		Ports:       []int{55432},
	})

	require.True(t, ok)
	assert.Equal(t, domain.DBTypePostgreSQL, db.Type)
	assert.Equal(t, "17", db.Version)
	assert.Equal(t, 55432, db.Port)
}

func TestDetectDatabaseFromAttachment_DefaultPort(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "postgres",
		Image:       "postgres:16",
		ContainerID: "xyz789",
		Ports:       []int{},
	})

	require.True(t, ok)
	assert.Equal(t, 5432, db.Port)
	assert.Equal(t, "16", db.Version)
}

func TestDetectDatabaseFromAttachment_PrefersPostgresPort(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "postgres",
		Image:       "postgres:16",
		ContainerID: "xyz789",
		Ports:       []int{15432, 5432, 25432},
	})

	require.True(t, ok)
	assert.Equal(t, 5432, db.Port)
}

func TestDetectDatabaseFromAttachment_UsesFirstPositivePortWhen5432Missing(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "postgres",
		Image:       "postgres:16",
		ContainerID: "xyz789",
		Ports:       []int{0, -1, 15432, 25432},
	})

	require.True(t, ok)
	assert.Equal(t, 15432, db.Port)
}

func TestPostgresVersionFromImage(t *testing.T) {
	assert.Equal(t, "18", postgresVersionFromImage("postgres:18"))
	assert.Equal(t, "17", postgresVersionFromImage("postgres:17.4-alpine"))
	assert.Equal(t, "", postgresVersionFromImage("postgres:latest"))
	assert.Equal(t, "", postgresVersionFromImage("postgres:alpine"))
	assert.Equal(t, "", postgresVersionFromImage("postgres"))
}

func TestDetectDatabaseFromAttachment_NonPostgres(t *testing.T) {
	_, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "mysql",
		Image:       "mysql:8.0",
		ContainerID: "def456",
		Ports:       []int{3306},
	})
	assert.False(t, ok)
}
