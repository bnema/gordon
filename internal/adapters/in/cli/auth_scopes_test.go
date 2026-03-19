package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAndConvertScopes_DefaultWildcard(t *testing.T) {
	got := parseAndConvertScopes("push,pull", "*")
	assert.Equal(t, []string{"repository:*:push,pull"}, got)
}

func TestParseAndConvertScopes_SpecificRepo(t *testing.T) {
	got := parseAndConvertScopes("push,pull", "myapp")
	assert.Equal(t, []string{"repository:myapp:push,pull"}, got)
}

func TestParseAndConvertScopes_SpecificRepoPushOnly(t *testing.T) {
	got := parseAndConvertScopes("push", "myapp")
	assert.Equal(t, []string{"repository:myapp:push"}, got)
}

func TestParseAndConvertScopes_MixedWithAdminScopes(t *testing.T) {
	got := parseAndConvertScopes("push,pull,admin:routes:read", "myapp")
	assert.ElementsMatch(t, []string{"repository:myapp:push,pull", "admin:routes:read"}, got)
}

func TestParseAndConvertScopes_AlreadyV2Format_RepoIgnored(t *testing.T) {
	// When scopes are already in v2 format, --repo is ignored (scopes returned as-is)
	got := parseAndConvertScopes("repository:specific:push", "*")
	assert.Equal(t, []string{"repository:specific:push"}, got)
}
