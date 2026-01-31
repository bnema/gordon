package envloader

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/zerowrap"
)

func passCmd(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "pass", args...).Run()
}

func requirePass(t *testing.T) {
	if err := passCmd("version"); err != nil {
		t.Skip("pass not available")
	}
	if err := passCmd("ls"); err != nil {
		t.Skip("pass store not initialized")
	}
}

func cleanupPassEnv(t *testing.T, path string) {
	_ = passCmd("rm", "-rf", path)
}

func TestPassLoader_LoadEnv_Domain(t *testing.T) {
	requirePass(t)

	log := zerowrap.New(zerowrap.Config{Level: "fatal"})
	store, err := domainsecrets.NewPassStore(log)
	require.NoError(t, err)

	loader, err := NewPassLoader(store, log)
	require.NoError(t, err)

	domainName := fmt.Sprintf("test.%d.example.com", time.Now().UnixNano())
	defer cleanupPassEnv(t, fmt.Sprintf("%s/%s", domainsecrets.PassDomainSecretsPath, domainName))

	secretsMap := map[string]string{
		"API_KEY": "test123",
		"DB_HOST": "localhost",
	}
	err = store.Set(domainName, secretsMap)
	require.NoError(t, err)

	ctx := context.Background()
	envVars, err := loader.LoadEnv(ctx, domainName)
	require.NoError(t, err)
	assert.Len(t, envVars, 2)
	assert.Contains(t, envVars, "API_KEY=test123")
	assert.Contains(t, envVars, "DB_HOST=localhost")
}

func TestPassLoader_LoadEnv_Attachment(t *testing.T) {
	requirePass(t)

	log := zerowrap.New(zerowrap.Config{Level: "fatal"})
	store, err := domainsecrets.NewPassStore(log)
	require.NoError(t, err)

	loader, err := NewPassLoader(store, log)
	require.NoError(t, err)

	containerName := fmt.Sprintf("gordon-gitea-postgres-%d", time.Now().UnixNano())
	defer cleanupPassEnv(t, fmt.Sprintf("%s/%s", domainsecrets.PassAttachmentPath, containerName))

	secretsMap := map[string]string{
		"POSTGRES_USER":     "gitea",
		"POSTGRES_PASSWORD": "secret123",
	}
	err = store.SetAttachment(containerName, secretsMap)
	require.NoError(t, err)

	ctx := context.Background()
	envVars, err := loader.LoadEnv(ctx, containerName)
	require.NoError(t, err)
	assert.Len(t, envVars, 2)
	assert.Contains(t, envVars, "POSTGRES_USER=gitea")
	assert.Contains(t, envVars, "POSTGRES_PASSWORD=secret123")
}

func TestPassLoader_LoadEnv_EmptyDomain(t *testing.T) {
	requirePass(t)

	log := zerowrap.New(zerowrap.Config{Level: "fatal"})
	store, err := domainsecrets.NewPassStore(log)
	require.NoError(t, err)

	loader, err := NewPassLoader(store, log)
	require.NoError(t, err)

	domainName := fmt.Sprintf("empty.%d.example.com", time.Now().UnixNano())

	ctx := context.Background()
	envVars, err := loader.LoadEnv(ctx, domainName)
	require.NoError(t, err)
	assert.Len(t, envVars, 0)
}
