package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

func TestFormatDeployFailure_RemoteStructuredError(t *testing.T) {
	err := formatDeployFailure(&remote.HTTPError{
		StatusCode: 500,
		Status:     "500 Internal Server Error",
		Body:       "failed to deploy",
		Cause:      "health check failed",
		Hint:       "check DATABASE_URL",
		Logs: []string{
			"booting app",
			"connection refused",
		},
	})

	require.Error(t, err)
	assert.Equal(t, "failed to deploy\nCause: health check failed\nHint: check DATABASE_URL\n\nRecent container logs:\n  booting app\n  connection refused", err.Error())
}

func TestFormatDeployFailure_LocalTypedError(t *testing.T) {
	err := formatDeployFailure(&domain.DeployFailureError{
		Summary: "failed to deploy",
		Cause:   "health check failed",
		Hint:    "check DATABASE_URL",
		Logs: []string{
			"booting app",
			"connection refused",
		},
	})

	require.Error(t, err)
	assert.Equal(t, "failed to deploy\nCause: health check failed\nHint: check DATABASE_URL\n\nRecent container logs:\n  booting app\n  connection refused", err.Error())
}

func TestFormatDeployFailure_GenericFallback(t *testing.T) {
	root := errors.New("boom")
	err := formatDeployFailure(root)

	require.Error(t, err)
	assert.Equal(t, "failed to deploy: boom", err.Error())
	assert.ErrorIs(t, err, root)
}
