package domainsecrets

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func cleanupPassDomain(_ *testing.T, domainName string, keys []string) {
	safeDomain, err := domain.SanitizeDomainForEnvFile(domainName)
	if err != nil {
		return
	}

	for _, key := range keys {
		path := fmt.Sprintf("%s/%s/%s", passDomainSecretsPath, safeDomain, key)
		_ = passCmd("rm", "-f", path)
	}

	manifestPath := fmt.Sprintf("%s/%s/.keys", passDomainSecretsPath, safeDomain)
	_ = passCmd("rm", "-f", manifestPath)
}

func TestPassStore_SetGetDelete(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	domainName := fmt.Sprintf("test.%d.example.com", time.Now().UnixNano())
	keys := []string{"API_KEY", "DB_PASSWORD"}
	defer cleanupPassDomain(t, domainName, keys)

	secretsMap := map[string]string{
		"API_KEY":     "alpha",
		"DB_PASSWORD": "bravo",
	}

	err = store.Set(domainName, secretsMap)
	require.NoError(t, err)

	keysList, err := store.ListKeys(domainName)
	require.NoError(t, err)
	assert.Len(t, keysList, 2)
	assert.ElementsMatch(t, keys, keysList)

	values, err := store.GetAll(domainName)
	require.NoError(t, err)
	assert.Equal(t, "alpha", values["API_KEY"])
	assert.Equal(t, "bravo", values["DB_PASSWORD"])

	err = store.Delete(domainName, "DB_PASSWORD")
	require.NoError(t, err)

	keysList, err = store.ListKeys(domainName)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"API_KEY"}, keysList)
}
