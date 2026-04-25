package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseScope(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantScope   *Scope
		wantErr     bool
		errContains string
	}{
		// Valid cases
		{
			name:  "simple repository scope",
			input: "repository:myrepo:pull",
			wantScope: &Scope{
				Type:    "repository",
				Name:    "myrepo",
				Actions: []string{"pull"},
			},
		},
		{
			name:  "multiple actions",
			input: "repository:myrepo:push,pull",
			wantScope: &Scope{
				Type:    "repository",
				Name:    "myrepo",
				Actions: []string{"push", "pull"},
			},
		},
		{
			name:  "wildcard repository",
			input: "repository:*:pull",
			wantScope: &Scope{
				Type:    "repository",
				Name:    "*",
				Actions: []string{"pull"},
			},
		},
		{
			name:  "org wildcard",
			input: "repository:myorg/*:push,pull",
			wantScope: &Scope{
				Type:    "repository",
				Name:    "myorg/*",
				Actions: []string{"push", "pull"},
			},
		},
		{
			name:  "wildcard action",
			input: "repository:myrepo:*",
			wantScope: &Scope{
				Type:    "repository",
				Name:    "myrepo",
				Actions: []string{"*"},
			},
		},
		{
			name:  "nested repository name",
			input: "repository:myorg/team/app:pull",
			wantScope: &Scope{
				Type:    "repository",
				Name:    "myorg/team/app",
				Actions: []string{"pull"},
			},
		},
		{
			name:  "actions with whitespace trimmed",
			input: "repository:myrepo:push, pull",
			wantScope: &Scope{
				Type:    "repository",
				Name:    "myrepo",
				Actions: []string{"push", "pull"},
			},
		},
		{
			name:  "registry scope type",
			input: "registry:catalog:*",
			wantScope: &Scope{
				Type:    "registry",
				Name:    "catalog",
				Actions: []string{"*"},
			},
		},

		// Invalid cases
		{
			name:        "empty string",
			input:       "",
			wantErr:     true,
			errContains: "invalid scope format",
		},
		{
			name:        "missing actions",
			input:       "repository:myrepo",
			wantErr:     true,
			errContains: "invalid scope format",
		},
		{
			name:        "missing name and actions",
			input:       "repository",
			wantErr:     true,
			errContains: "invalid scope format",
		},
		{
			name:  "only type and name",
			input: "repository:myrepo:",
			wantScope: &Scope{
				Type:    "repository",
				Name:    "myrepo",
				Actions: []string{""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope, err := ParseScope(tt.input)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantScope.Type, scope.Type)
			assert.Equal(t, tt.wantScope.Name, scope.Name)
			assert.Equal(t, tt.wantScope.Actions, scope.Actions)
		})
	}
}

func TestScope_CanAccess(t *testing.T) {
	tests := []struct {
		name     string
		scope    *Scope
		repoName string
		action   string
		want     bool
	}{
		// Exact match cases
		{
			name:     "exact repo match with pull",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{"pull"}},
			repoName: "myrepo",
			action:   "pull",
			want:     true,
		},
		{
			name:     "exact repo match with push",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{"push"}},
			repoName: "myrepo",
			action:   "push",
			want:     true,
		},
		{
			name:     "exact repo match wrong action",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{"pull"}},
			repoName: "myrepo",
			action:   "push",
			want:     false,
		},
		{
			name:     "wrong repo name",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{"pull"}},
			repoName: "otherrepo",
			action:   "pull",
			want:     false,
		},

		// Wildcard repository cases
		{
			name:     "wildcard repo matches any",
			scope:    &Scope{Type: "repository", Name: "*", Actions: []string{"pull"}},
			repoName: "anyrepo",
			action:   "pull",
			want:     true,
		},
		{
			name:     "wildcard repo matches nested",
			scope:    &Scope{Type: "repository", Name: "*", Actions: []string{"pull"}},
			repoName: "myorg/myapp",
			action:   "pull",
			want:     true,
		},

		// Org-level wildcard cases
		{
			name:     "org wildcard matches repo in org",
			scope:    &Scope{Type: "repository", Name: "myorg/*", Actions: []string{"pull"}},
			repoName: "myorg/myapp",
			action:   "pull",
			want:     true,
		},
		{
			name:     "org wildcard matches nested repo in org",
			scope:    &Scope{Type: "repository", Name: "myorg/*", Actions: []string{"pull"}},
			repoName: "myorg/team/myapp",
			action:   "pull",
			want:     true,
		},
		{
			name:     "org wildcard does not match other org",
			scope:    &Scope{Type: "repository", Name: "myorg/*", Actions: []string{"pull"}},
			repoName: "otherorg/myapp",
			action:   "pull",
			want:     false,
		},
		{
			name:     "org wildcard does not match repo without slash",
			scope:    &Scope{Type: "repository", Name: "myorg/*", Actions: []string{"pull"}},
			repoName: "myorg",
			action:   "pull",
			want:     false,
		},

		// Wildcard action cases
		{
			name:     "wildcard action grants pull",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{"*"}},
			repoName: "myrepo",
			action:   "pull",
			want:     true,
		},
		{
			name:     "wildcard action grants push",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{"*"}},
			repoName: "myrepo",
			action:   "push",
			want:     true,
		},

		// Multiple actions
		{
			name:     "multiple actions includes requested",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{"push", "pull"}},
			repoName: "myrepo",
			action:   "pull",
			want:     true,
		},
		{
			name:     "multiple actions includes push",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{"push", "pull"}},
			repoName: "myrepo",
			action:   "push",
			want:     true,
		},

		// Non-repository type
		{
			name:     "registry type does not grant repo access",
			scope:    &Scope{Type: "registry", Name: "catalog", Actions: []string{"*"}},
			repoName: "myrepo",
			action:   "pull",
			want:     false,
		},

		// Edge cases
		{
			name:     "empty actions",
			scope:    &Scope{Type: "repository", Name: "myrepo", Actions: []string{}},
			repoName: "myrepo",
			action:   "pull",
			want:     false,
		},
		{
			name:     "empty repo name in scope",
			scope:    &Scope{Type: "repository", Name: "", Actions: []string{"pull"}},
			repoName: "myrepo",
			action:   "pull",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.scope.CanAccess(tt.repoName, tt.action)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestScope_String(t *testing.T) {
	tests := []struct {
		name  string
		scope *Scope
		want  string
	}{
		{
			name:  "simple scope",
			scope: &Scope{Type: "repository", Name: "myrepo", Actions: []string{"pull"}},
			want:  "repository:myrepo:pull",
		},
		{
			name:  "multiple actions",
			scope: &Scope{Type: "repository", Name: "myrepo", Actions: []string{"push", "pull"}},
			want:  "repository:myrepo:push,pull",
		},
		{
			name:  "wildcard",
			scope: &Scope{Type: "repository", Name: "*", Actions: []string{"*"}},
			want:  "repository:*:*",
		},
		{
			name:  "org wildcard",
			scope: &Scope{Type: "repository", Name: "myorg/*", Actions: []string{"push", "pull"}},
			want:  "repository:myorg/*:push,pull",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.scope.String()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestScopesGrantRegistryAccess(t *testing.T) {
	tests := []struct {
		name     string
		granted  []string
		repoName string
		action   string
		want     bool
	}{
		{
			name:     "wildcard shorthand grants any action",
			granted:  []string{"*"},
			repoName: "myrepo",
			action:   "push",
			want:     true,
		},
		{
			name:     "pull shorthand grants pull",
			granted:  []string{"pull"},
			repoName: "myrepo",
			action:   "pull",
			want:     true,
		},
		{
			name:     "pull shorthand does not grant push",
			granted:  []string{"pull"},
			repoName: "myrepo",
			action:   "push",
			want:     false,
		},
		{
			name:     "push shorthand grants push",
			granted:  []string{"push"},
			repoName: "myrepo",
			action:   "push",
			want:     true,
		},
		{
			name:     "full scope grants matching repo and action",
			granted:  []string{"repository:myrepo:push,pull"},
			repoName: "myrepo",
			action:   "pull",
			want:     true,
		},
		{
			name:     "full scope does not grant different repo",
			granted:  []string{"repository:myrepo:push,pull"},
			repoName: "other",
			action:   "pull",
			want:     false,
		},
		{
			name:     "wildcard repo scope grants any repo",
			granted:  []string{"repository:*:pull"},
			repoName: "anything",
			action:   "pull",
			want:     true,
		},
		{
			name:     "org wildcard scope grants repo in org",
			granted:  []string{"repository:myorg/*:push"},
			repoName: "myorg/app",
			action:   "push",
			want:     true,
		},
		{
			name:     "org wildcard scope rejects repo outside org",
			granted:  []string{"repository:myorg/*:push"},
			repoName: "other/app",
			action:   "push",
			want:     false,
		},
		{
			name:     "admin scope is ignored for registry access",
			granted:  []string{"admin:routes:read"},
			repoName: "myrepo",
			action:   "pull",
			want:     false,
		},
		{
			name:     "no granted scopes",
			granted:  []string{},
			repoName: "myrepo",
			action:   "pull",
			want:     false,
		},
		{
			name:     "whitespace around scope is trimmed",
			granted:  []string{"  repository:myrepo:pull  "},
			repoName: "myrepo",
			action:   "pull",
			want:     true,
		},
		{
			name:     "invalid scope string is skipped",
			granted:  []string{"garbage", "repository:myrepo:pull"},
			repoName: "myrepo",
			action:   "pull",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScopesGrantRegistryAccess(tt.granted, tt.repoName, tt.action)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestScopesGrantAdminAccess(t *testing.T) {
	tests := []struct {
		name     string
		granted  []string
		resource string
		action   string
		want     bool
	}{
		{
			name:     "wildcard shorthand grants any admin access",
			granted:  []string{"*"},
			resource: "routes",
			action:   "read",
			want:     true,
		},
		{
			name:     "exact admin scope grants matching resource and action",
			granted:  []string{"admin:routes:read"},
			resource: "routes",
			action:   "read",
			want:     true,
		},
		{
			name:     "exact admin scope denies wrong action",
			granted:  []string{"admin:routes:read"},
			resource: "routes",
			action:   "write",
			want:     false,
		},
		{
			name:     "exact admin scope denies wrong resource",
			granted:  []string{"admin:routes:read"},
			resource: "secrets",
			action:   "read",
			want:     false,
		},
		{
			name:     "admin wildcard resource grants any resource",
			granted:  []string{"admin:*:read"},
			resource: "secrets",
			action:   "read",
			want:     true,
		},
		{
			name:     "admin wildcard action grants any action",
			granted:  []string{"admin:routes:*"},
			resource: "routes",
			action:   "write",
			want:     true,
		},
		{
			name:     "full admin wildcard grants everything",
			granted:  []string{"admin:*:*"},
			resource: "config",
			action:   "write",
			want:     true,
		},
		{
			name:     "logs read grants logs resource",
			granted:  []string{"admin:logs:read"},
			resource: AdminResourceLogs,
			action:   AdminActionRead,
			want:     true,
		},
		{
			name:     "status read does not grant logs resource",
			granted:  []string{"admin:status:read"},
			resource: AdminResourceLogs,
			action:   AdminActionRead,
			want:     false,
		},
		{
			name:     "full admin wildcard grants logs resource",
			granted:  []string{"admin:*:*"},
			resource: AdminResourceLogs,
			action:   AdminActionRead,
			want:     true,
		},
		{
			name:     "repository scope is ignored for admin access",
			granted:  []string{"repository:myrepo:push"},
			resource: "routes",
			action:   "read",
			want:     false,
		},
		{
			name:     "no granted scopes",
			granted:  []string{},
			resource: "routes",
			action:   "read",
			want:     false,
		},
		{
			name:     "whitespace around scope is trimmed",
			granted:  []string{"  admin:routes:read  "},
			resource: "routes",
			action:   "read",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScopesGrantAdminAccess(tt.granted, tt.resource, tt.action)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseScope_RoundTrip(t *testing.T) {
	// Test that parsing and stringifying produces the same result
	inputs := []string{
		"repository:myrepo:pull",
		"repository:myrepo:push,pull",
		"repository:*:*",
		"repository:myorg/*:push,pull",
		"repository:myorg/team/app:pull",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			scope, err := ParseScope(input)
			require.NoError(t, err)

			output := scope.String()
			assert.Equal(t, input, output)
		})
	}
}
