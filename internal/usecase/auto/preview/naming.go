package preview

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ExtractPreviewName strips the matched pattern prefix from a tag.
// "preview-login-redesign" with pattern "preview-*" → "login-redesign"
func ExtractPreviewName(tag string, patterns []string) string {
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, tag); matched {
			prefix := strings.TrimSuffix(pattern, "*")
			return strings.TrimPrefix(tag, prefix)
		}
	}
	return tag
}

// GeneratePreviewDomain creates the preview domain from a base route domain.
// "myapp.example.com" + "login" + "--" → "myapp--login.example.com"
func GeneratePreviewDomain(baseRoute, previewName, separator string) (string, error) {
	dot := strings.Index(baseRoute, ".")
	if dot < 0 {
		return "", fmt.Errorf("invalid base route domain: %s", baseRoute)
	}
	app := baseRoute[:dot]
	rest := baseRoute[dot:] // includes leading dot
	return app + separator + previewName + rest, nil
}

// SanitizeBranchName converts a git branch name to a valid preview name.
// "feat/login-redesign" → "login-redesign"
func SanitizeBranchName(branch string) string {
	branch = strings.ToLower(branch)
	for _, prefix := range []string{"feat/", "fix/", "feature/", "hotfix/", "release/", "chore/"} {
		if strings.HasPrefix(branch, prefix) {
			branch = strings.TrimPrefix(branch, prefix)
			break
		}
	}
	branch = strings.ReplaceAll(branch, "/", "-")
	return branch
}
