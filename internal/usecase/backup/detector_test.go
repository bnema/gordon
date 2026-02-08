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

func TestDetectDatabaseFromAttachment_PgsqlImage(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "pgsql",
		Image:       "pgsql:16",
		ContainerID: "pgsql-1",
		Ports:       []int{5432},
	})

	require.True(t, ok)
	assert.Equal(t, domain.DBTypePostgreSQL, db.Type)
	assert.Equal(t, "16", db.Version)
	assert.Equal(t, 5432, db.Port)
}

func TestDetectDatabaseFromAttachment_PgsqlInCompositeName(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("mon-fauteuil.fr", domain.Attachment{
		Name:        "mon-fauteuil-prod-pgsql",
		Image:       "mon-fauteuil-prod-pgsql:v18",
		ContainerID: "pgsql-2",
		Ports:       []int{5432},
	})

	require.True(t, ok)
	assert.Equal(t, domain.DBTypePostgreSQL, db.Type)
	assert.Equal(t, "", db.Version) // v18 tag starts with non-digit, no version extracted
}

func TestDetectDatabaseFromAttachment_PostgresqlFullName(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "postgresql",
		Image:       "postgresql:15",
		ContainerID: "pg-full-1",
		Ports:       []int{5432},
	})

	require.True(t, ok)
	assert.Equal(t, domain.DBTypePostgreSQL, db.Type)
	assert.Equal(t, "15", db.Version)
}

func TestDetectDatabaseFromAttachment_PostgisImage(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "postgis",
		Image:       "postgis/postgis:16-3.4",
		ContainerID: "postgis-1",
		Ports:       []int{5432},
	})

	require.True(t, ok)
	assert.Equal(t, domain.DBTypePostgreSQL, db.Type)
	assert.Equal(t, "16", db.Version)
}

func TestDetectDatabaseFromAttachment_Port5432Fallback(t *testing.T) {
	db, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "custom-db",
		Image:       "custom-db:latest",
		ContainerID: "fallback-1",
		Ports:       []int{5432},
	})

	require.True(t, ok, "port 5432 should trigger PostgreSQL fallback detection")
	assert.Equal(t, domain.DBTypePostgreSQL, db.Type)
	assert.Equal(t, 5432, db.Port)
}

func TestDetectDatabaseFromAttachment_UnknownImageNoPostgresPort(t *testing.T) {
	_, ok := detectDatabaseFromAttachment("app.example.com", domain.Attachment{
		Name:        "custom-db",
		Image:       "custom-db:latest",
		ContainerID: "unknown-1",
		Ports:       []int{3306},
	})
	assert.False(t, ok, "non-postgres port with unknown image should not match")
}

func TestLooksLikePostgres(t *testing.T) {
	// Positive cases
	assert.True(t, looksLikePostgres("postgres"))
	assert.True(t, looksLikePostgres("postgres:16"))
	assert.True(t, looksLikePostgres("my-postgres-db"))
	assert.True(t, looksLikePostgres("postgresql"))
	assert.True(t, looksLikePostgres("pgsql"))
	assert.True(t, looksLikePostgres("mon-fauteuil-prod-pgsql"))
	assert.True(t, looksLikePostgres("postgis/postgis"))

	// Negative cases
	assert.False(t, looksLikePostgres("mysql"))
	assert.False(t, looksLikePostgres("redis"))
	assert.False(t, looksLikePostgres("custom-db"))
	assert.False(t, looksLikePostgres(""))
}
