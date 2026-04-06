package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

type deployTestDeployer struct {
	result *remote.DeployResult
	err    error
}

func (d *deployTestDeployer) Deploy(context.Context, string) (*remote.DeployResult, error) {
	return d.result, d.err
}

func TestRunDeploy_LocalTypedErrorUsesDeployFormatter(t *testing.T) {
	err := runDeploy(context.Background(), &deployTestDeployer{err: &domain.DeployFailureError{
		Summary: "failed to deploy",
		Cause:   "health check failed",
		Hint:    "check DATABASE_URL",
		Logs:    []string{"booting app", "connection refused"},
	}}, false, "app.example.com")

	require.Error(t, err)
	assert.Equal(t, "failed to deploy\nCause: health check failed\nHint: check DATABASE_URL\n\nRecent container logs:\n  booting app\n  connection refused", err.Error())
}
