package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAndConvertScopes_DefaultWildcard(t *testing.T) {
	got, err := parseAndConvertScopes("push,pull", "*")
	require.NoError(t, err)
	assert.Equal(t, []string{"repository:*:push,pull"}, got)
}

func TestParseAndConvertScopes_SpecificRepo(t *testing.T) {
	got, err := parseAndConvertScopes("push,pull", "myapp")
	require.NoError(t, err)
	assert.Equal(t, []string{"repository:myapp:push,pull"}, got)
}

func TestParseAndConvertScopes_SpecificRepoPushOnly(t *testing.T) {
	got, err := parseAndConvertScopes("push", "myapp")
	require.NoError(t, err)
	assert.Equal(t, []string{"repository:myapp:push"}, got)
}

func TestParseAndConvertScopes_MixedWithAdminScopes(t *testing.T) {
	got, err := parseAndConvertScopes("push,pull,admin:routes:read", "myapp")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"repository:myapp:push,pull", "admin:routes:read"}, got)
}

func TestParseAndConvertScopes_AlreadyV2Format_RepoIgnored(t *testing.T) {
	// When scopes are already in v2 format, --repo is ignored (scopes returned as-is)
	got, err := parseAndConvertScopes("repository:specific:push", "*")
	require.NoError(t, err)
	assert.Equal(t, []string{"repository:specific:push"}, got)
}

func TestParseAndConvertScopes_EmptyRepo(t *testing.T) {
	_, err := parseAndConvertScopes("push,pull", "")
	assert.EqualError(t, err, "repository name cannot be empty")
}

func TestParseAndConvertScopes_WhitespaceRepo(t *testing.T) {
	_, err := parseAndConvertScopes("push,pull", "  ")
	assert.EqualError(t, err, "repository name cannot be empty")
}
