package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/domain"
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

func cleanupPassDomain(t *testing.T, domainName string, keys []string) {
	safeDomain, err := domain.SanitizeDomainForEnvFile(domainName)
	if err != nil {
		return
	}

	for _, key := range keys {
		path := fmt.Sprintf("%s/%s/%s", domainsecrets.PassDomainSecretsPath, safeDomain, key)
		_ = passCmd("rm", "-f", path)
	}

	manifestPath := fmt.Sprintf("%s/%s/.keys", domainsecrets.PassDomainSecretsPath, safeDomain)
	_ = passCmd("rm", "-f", manifestPath)
}

func TestMigrateEnvFile(t *testing.T) {
	requirePass(t)

	tmpDir, err := os.MkdirTemp("", "migrate-env-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	logger := zerowrap.Default()
	store, err := domainsecrets.NewPassStore(logger)
	require.NoError(t, err)

	domainName := fmt.Sprintf("test.%d.example.com", time.Now().UnixNano())
	keys := []string{"API_KEY", "DB_PASSWORD"}
	defer cleanupPassDomain(t, domainName, keys)

	envContent := "API_KEY=secret123\nDB_PASSWORD=pass456\n"
	envFileName := domainName + ".env"
	envFilePath := filepath.Join(tmpDir, envFileName)
	err = os.WriteFile(envFilePath, []byte(envContent), 0600)
	require.NoError(t, err)

	err = migrateEnvFile(tmpDir, envFileName, store, logger)
	require.NoError(t, err)

	migratedPath := envFilePath + ".migrated"
	_, err = os.Stat(envFilePath)
	assert.True(t, os.IsNotExist(err), "original .env file should be removed")

	_, err = os.Stat(migratedPath)
	assert.NoError(t, err, ".env.migrated file should exist")

	passKeys, err := store.ListKeys(domainName)
	require.NoError(t, err)
	assert.ElementsMatch(t, keys, passKeys)

	values, err := store.GetAll(domainName)
	require.NoError(t, err)
	assert.Equal(t, "secret123", values["API_KEY"])
	assert.Equal(t, "pass456", values["DB_PASSWORD"])
}
