package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
)

// TestDiffAppSpec_VisibilityChangeIsDetected proves flipping an interface
// between public and internal is a meaningful change: it must force a
// redeploy instead of being silently ignored.
func TestDiffAppSpec_VisibilityChangeIsDetected(t *testing.T) {
	desired := visibilitySpec(domain.AppHTTPInterface{Port: 8080, Visibility: domain.AppVisibilityInternal})
	effective := visibilitySpec(domain.AppHTTPInterface{
		Host: "blog.example.com", Port: 8080, TLS: "auto", Visibility: domain.AppVisibilityPublic,
	})

	diff := domain.DiffAppSpec(desired, effective)
	assert.Contains(t, diff.Changed, "service/web/interfaces")
}

// TestDiffAppSpec_ZeroVisibilityEqualsPublic proves state written before
// the visibility field existed (zero value) is not a phantom change
// against an explicit public interface, so applying an unchanged manifest
// stays a no-op after upgrade.
func TestDiffAppSpec_ZeroVisibilityEqualsPublic(t *testing.T) {
	// Legacy stored state: same interface, visibility never serialized.
	legacy := visibilitySpec(domain.AppHTTPInterface{
		Host: "blog.example.com", Port: 8080, TLS: "auto",
	})
	desired := visibilitySpec(domain.AppHTTPInterface{
		Host: "blog.example.com", Port: 8080, TLS: "auto", Visibility: domain.AppVisibilityPublic,
	})

	diff := domain.DiffAppSpec(desired, legacy)
	assert.NotContains(t, diff.Changed, "service/web/interfaces")
}
