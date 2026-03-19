package preview

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractPreviewName(t *testing.T) {
	tests := []struct {
		name     string
		tag      string
		patterns []string
		want     string
	}{
		{"simple prefix", "preview-login", []string{"preview-*"}, "login"},
		{"pr prefix", "pr-42", []string{"pr-*"}, "42"},
		{"multi-word", "preview-login-redesign", []string{"preview-*"}, "login-redesign"},
		{"second pattern matches", "pr-99", []string{"preview-*", "pr-*"}, "99"},
		{"no match", "v1.0.0", []string{"preview-*"}, "v1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractPreviewName(tt.tag, tt.patterns))
		})
	}
}

func TestGeneratePreviewDomain(t *testing.T) {
	tests := []struct {
		name        string
		baseRoute   string
		previewName string
		separator   string
		want        string
		wantErr     bool
	}{
		{"flat default", "myapp.example.com", "login", "--", "myapp--login.example.com", false},
		{"flat with dashes", "my-app.example.com", "feat-123", "--", "my-app--feat-123.example.com", false},
		{"multi-level base", "myapp.sub.example.com", "feat", "--", "myapp--feat.sub.example.com", false},
		{"invalid domain", "nodot", "feat", "--", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GeneratePreviewDomain(tt.baseRoute, tt.previewName, tt.separator)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSanitizeBranchName(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		want   string
	}{
		{"feature branch", "feat/login-redesign", "login-redesign"},
		{"fix branch", "fix/bug-123", "bug-123"},
		{"simple", "main", "main"},
		{"nested", "feat/ui/button", "ui-button"},
		{"uppercase", "Feat/MyBranch", "mybranch"},
		{"dots and underscores", "feat/my_branch.name", "my-branch-name"},
		{"special chars", "feat/hello@world!", "helloworld"},
		{"long branch", strings.Repeat("a", 70), strings.Repeat("a", 63)},
		{"consecutive dashes", "feat/a--b--c", "a-b-c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizeBranchName(tt.branch))
		})
	}
}
