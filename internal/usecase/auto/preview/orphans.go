package preview

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const orphanGracePeriod = 5 * time.Minute

// IsOrphanPreview returns true if the container looks like a preview (by image
// tag pattern and domain separator) but is not in the tracked preview list.
// Containers younger than 5 minutes are skipped to avoid racing with
// CreatePreview.
func IsOrphanPreview(c *domain.Container, tracked []domain.PreviewRoute, tagPatterns []string, separator string) bool {
	imgLabel := c.Labels[domain.LabelImage]
	domLabel := c.Labels[domain.LabelDomain]
	if imgLabel == "" || domLabel == "" {
		return false
	}

	// Grace period: skip very recent containers.
	if !c.Created.IsZero() && time.Since(c.Created) < orphanGracePeriod {
		return false
	}

	// Condition 1: image tag matches a preview pattern.
	tag := tagFromImage(imgLabel)
	if !matchesAnyPattern(tag, tagPatterns) {
		return false
	}

	// Condition 2: domain contains the separator.
	if !strings.Contains(domLabel, separator) {
		return false
	}

	// Not an orphan if tracked.
	for _, p := range tracked {
		if p.Domain == domLabel {
			return false
		}
	}
	return true
}

// IsExpiredOrphan returns true if the container has exceeded the TTL based on
// its creation time.
func IsExpiredOrphan(c *domain.Container, ttl time.Duration) bool {
	if c.Created.IsZero() {
		return false // unknown creation time — don't delete, play it safe
	}
	return time.Since(c.Created) > ttl
}

// tagFromImage extracts the tag portion from an image reference.
// "myapp:preview-feat" -> "preview-feat"
// "registry.example.com/myapp:preview-feat" -> "preview-feat"
// "myapp" -> "" (no tag)
func tagFromImage(image string) string {
	if idx := strings.Index(image, "@"); idx != -1 {
		return ""
	}
	if idx := strings.LastIndex(image, ":"); idx != -1 {
		return image[idx+1:]
	}
	return ""
}

// matchesAnyPattern checks if the tag matches any of the glob patterns.
func matchesAnyPattern(tag string, patterns []string) bool {
	for _, p := range patterns {
		if matched, _ := filepath.Match(p, tag); matched {
			return true
		}
	}
	return false
}
