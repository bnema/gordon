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
	root := &remote.HTTPError{
		StatusCode: 500,
		Status:     "500 Internal Server Error",
		Body:       "failed to deploy",
		Cause:      "health check failed",
		Hint:       "check DATABASE_URL",
		Logs: []string{
			"booting app",
			"connection refused",
		},
	}
	err := formatDeployFailure(root)

	require.Error(t, err)
	assert.Equal(t, "failed to deploy\nCause: health check failed\nHint: check DATABASE_URL\n\nRecent container logs:\n  booting app\n  connection refused", err.Error())
	assert.ErrorIs(t, err, root)
}

func TestFormatDeployFailure_LocalTypedError(t *testing.T) {
	root := &domain.DeployFailureError{
		Summary: "failed to deploy",
		Cause:   "health check failed",
		Hint:    "check DATABASE_URL",
		Logs: []string{
			"booting app",
			"connection refused",
		},
	}
	err := formatDeployFailure(root)

	require.Error(t, err)
	assert.Equal(t, "failed to deploy\nCause: health check failed\nHint: check DATABASE_URL\n\nRecent container logs:\n  booting app\n  connection refused", err.Error())
	assert.ErrorIs(t, err, root)
}

func TestFormatDeployFailure_GenericFallback(t *testing.T) {
	root := errors.New("boom")
	err := formatDeployFailure(root)

	require.Error(t, err)
	assert.Equal(t, "failed to deploy: boom", err.Error())
	assert.ErrorIs(t, err, root)
}

func TestFormatDeployFailure_SkipsEmptySanitizedLogsSection(t *testing.T) {
	root := &domain.DeployFailureError{
		Summary: "failed to deploy",
		Cause:   "health check failed",
		Logs:    []string{"\x1b[31m\x1b[0m", "\n\r\t"},
	}
	err := formatDeployFailure(root)

	require.Error(t, err)
	assert.Equal(t, "failed to deploy\nCause: health check failed", err.Error())
	assert.ErrorIs(t, err, root)
}
