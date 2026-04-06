package cli

import (
	"bytes"
	"context"
	"errors"
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
	root := &domain.DeployFailureError{
		Summary: "failed to deploy",
		Cause:   "health check failed",
		Hint:    "check DATABASE_URL",
		Logs:    []string{"booting app", "connection refused"},
	}
	err := runDeploy(context.Background(), &deployTestDeployer{err: root}, false, "app.example.com", &bytes.Buffer{}, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to deploy\nCause: health check failed\nHint: check DATABASE_URL\n\nRecent container logs:\n  booting app\n  connection refused")
	assert.ErrorIs(t, err, root)
}

func TestRunDeploy_StructuredRemoteNoFallbackReturnsFormattedError(t *testing.T) {
	originalSendDeploySignal := sendDeploySignal
	sendDeploySignal = func(string) (string, error) {
		t.Fatalf("sendDeploySignal should not be called for structured deploy failures")
		return "", nil
	}
	defer func() { sendDeploySignal = originalSendDeploySignal }()

	root := &remote.HTTPError{
		StatusCode: 503,
		Status:     "503 Service Unavailable",
		Body:       "failed to deploy",
		Cause:      "health check failed",
		Hint:       "check DATABASE_URL",
		Logs:       []string{"booting app", "connection refused"},
		Structured: true,
	}
	err := runDeploy(context.Background(), &deployTestDeployer{err: root}, true, "app.example.com", &bytes.Buffer{}, false)

	require.Error(t, err)
	assert.Equal(t, "failed to deploy\nCause: health check failed\nHint: check DATABASE_URL\n\nRecent container logs:\n  booting app\n  connection refused", err.Error())
	assert.ErrorIs(t, err, root)
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

func TestRunDeploy_RemoteFallbackUsesJSONWhenRequested(t *testing.T) {
	originalSendDeploySignal := sendDeploySignal
	sendDeploySignal = func(string) (string, error) {
		return "app.example.com", nil
	}
	defer func() { sendDeploySignal = originalSendDeploySignal }()

	var out bytes.Buffer
	err := runDeploy(context.Background(), &deployTestDeployer{err: errors.New("503 Service Unavailable: Registry Unavailable")}, true, "app.example.com", &out, true)

	require.NoError(t, err)
	assert.JSONEq(t, `{"domain":"app.example.com","status":"success","warning":"Remote deploy failed (503 Service Unavailable: Registry Unavailable), used local signal fallback"}`, out.String())
}
