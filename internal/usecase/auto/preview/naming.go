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
		matched, err := filepath.Match(pattern, tag)
		if err != nil {
			continue // skip malformed patterns
		}
		if matched {
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

// SanitizeBranchName converts a git branch name to a valid DNS-safe preview name.
// "feat/login-redesign" → "login-redesign"
func SanitizeBranchName(branch string) string {
	branch = strings.ToLower(branch)
	for _, prefix := range []string{"feat/", "fix/", "feature/", "hotfix/", "release/", "chore/"} {
		if strings.HasPrefix(branch, prefix) {
			branch = strings.TrimPrefix(branch, prefix)
			break
		}
	}
	// Replace disallowed characters with dashes
	branch = strings.ReplaceAll(branch, "/", "-")
	branch = strings.ReplaceAll(branch, "_", "-")
	branch = strings.ReplaceAll(branch, ".", "-")

	// Remove any remaining non DNS-safe characters
	var result strings.Builder
	for _, c := range branch {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			result.WriteRune(c)
		}
	}
	branch = result.String()

	// Collapse consecutive dashes and trim
	for strings.Contains(branch, "--") {
		branch = strings.ReplaceAll(branch, "--", "-")
	}
	branch = strings.Trim(branch, "-")

	// Truncate to 63 chars (DNS label limit)
	if len(branch) > 63 {
		branch = branch[:63]
		branch = strings.TrimRight(branch, "-")
	}
	return branch
}
