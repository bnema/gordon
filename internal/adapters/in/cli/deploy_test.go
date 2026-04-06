package cli

import (
	"bytes"
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
	}}, false, "app.example.com", &bytes.Buffer{}, false)

	require.Error(t, err)
	assert.Equal(t, "failed to deploy\nCause: health check failed\nHint: check DATABASE_URL\n\nRecent container logs:\n  booting app\n  connection refused", err.Error())
}

func TestRunDeploy_UsesResultDomainWhenPresent(t *testing.T) {
	var out bytes.Buffer
	err := runDeploy(context.Background(), &deployTestDeployer{result: &remote.DeployResult{
		Status:      "deployed",
		Domain:      "actual.example.com",
		ContainerID: "1234567890abcdef",
	}}, false, "requested.example.com", &out, false)

	require.NoError(t, err)
	assert.Contains(t, out.String(), "Deployed actual.example.com (container: 1234567890ab)")
	assert.NotContains(t, out.String(), "requested.example.com")
}
