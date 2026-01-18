package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
		errType error
	}{
		// Valid paths
		{
			name:    "simple path",
			path:    "github.com/bnema/gordon/registry/secret",
			wantErr: false,
		},
		{
			name:    "path with dots in name",
			path:    "my.secret.key",
			wantErr: false,
		},
		{
			name:    "path with hyphens",
			path:    "my-app/api-key",
			wantErr: false,
		},
		{
			name:    "path with underscores",
			path:    "my_app/api_key",
			wantErr: false,
		},
		{
			name:    "nested path",
			path:    "org/team/app/secret",
			wantErr: false,
		},

		// Invalid paths - path traversal
		{
			name:    "path traversal with double dots",
			path:    "../../../etc/passwd",
			wantErr: true,
			errType: domain.ErrPathTraversal,
		},
		{
			name:    "path traversal in middle",
			path:    "foo/../../../etc/passwd",
			wantErr: true,
			errType: domain.ErrPathTraversal,
		},
		{
			name:    "path traversal at end",
			path:    "foo/bar/..",
			wantErr: true,
			errType: domain.ErrPathTraversal,
		},
		{
			name:    "encoded path traversal",
			path:    "..%2f..%2fetc/passwd",
			wantErr: true, // % is not allowed by regex
		},

		// Invalid paths - absolute paths
		{
			name:    "absolute path",
			path:    "/etc/passwd",
			wantErr: true,
		},
		{
			name:    "absolute path with valid chars",
			path:    "/gordon/secret",
			wantErr: true,
		},

		// Invalid paths - special characters (command injection)
		{
			name:    "semicolon injection",
			path:    "secret;rm -rf /",
			wantErr: true,
		},
		{
			name:    "pipe injection",
			path:    "secret|cat /etc/passwd",
			wantErr: true,
		},
		{
			name:    "backtick injection",
			path:    "secret`whoami`",
			wantErr: true,
		},
		{
			name:    "dollar injection",
			path:    "secret$(whoami)",
			wantErr: true,
		},
		{
			name:    "ampersand injection",
			path:    "secret&rm -rf /",
			wantErr: true,
		},
		{
			name:    "newline injection",
			path:    "secret\nrm -rf /",
			wantErr: true,
		},
		{
			name:    "space in path",
			path:    "secret with space",
			wantErr: true,
		},
		{
			name:    "null byte injection",
			path:    "secret\x00evil",
			wantErr: true,
		},

		// Edge cases
		{
			name:    "empty path",
			path:    "",
			wantErr: true,
		},
		{
			name:    "single dot",
			path:    ".",
			wantErr: false, // single dot is valid, not traversal
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePath(tt.path)

			if tt.wantErr {
				require.Error(t, err, "expected error for path: %q", tt.path)
				if tt.errType != nil {
					assert.True(t, errors.Is(err, tt.errType), "expected error type %v, got %v", tt.errType, err)
				}
			} else {
				assert.NoError(t, err, "unexpected error for path: %q", tt.path)
			}
		})
	}
}

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestPassProvider_GetSecret_PathValidation(t *testing.T) {
	provider := NewPassProvider(testLogger())

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "valid path rejected due to pass not installed",
			path:    "github.com/bnema/gordon/test/secret",
			wantErr: true, // Will fail because pass command doesn't exist in test, but path is valid
		},
		{
			name:    "path traversal rejected before command execution",
			path:    "../../../etc/passwd",
			wantErr: true,
		},
		{
			name:    "command injection rejected before command execution",
			path:    "secret;whoami",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.GetSecret(context.Background(), tt.path)

			if tt.wantErr {
				require.Error(t, err)
			}

			// For security-related rejections, verify the error is from validation, not command execution
			if tt.path == "../../../etc/passwd" {
				assert.True(t, errors.Is(err, domain.ErrPathTraversal))
			}
			if tt.path == "secret;whoami" {
				assert.Contains(t, err.Error(), "invalid path")
			}
		})
	}
}
