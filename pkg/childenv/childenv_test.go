package childenv

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterDropsGordonVariables(t *testing.T) {
	got := filter([]string{
		"PATH=/usr/bin",
		"GORDON_AUTH_TOKEN_SECRET=s3cr3t",
		"PASSWORD_STORE_DIR=/keys/pass",
		"GORDON_CLOUDFLARE_API_TOKEN=t0ken",
		"GNUPGHOME=/keys/gnupg",
	})
	assert.Equal(t, []string{"PATH=/usr/bin", "PASSWORD_STORE_DIR=/keys/pass", "GNUPGHOME=/keys/gnupg"}, got)
}

func TestCommandContextStripsGordonVariables(t *testing.T) {
	t.Setenv("GORDON_TEST_SECRET", "s3cr3t")
	t.Setenv("CHILDENV_KEEP", "1")
	cmd := CommandContext(context.Background(), "true")
	assert.Contains(t, cmd.Env, "CHILDENV_KEEP=1")
	assert.NotContains(t, cmd.Env, "GORDON_TEST_SECRET=s3cr3t")
}
