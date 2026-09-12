package domain_test

import (
	"testing"

	"github.com/bnema/gordon/internal/domain"
)

// TestLabelConstantsValues guards against accidental value changes that
// would silently break container discovery.
func TestLabelConstantsValues(t *testing.T) {
	tests := []struct {
		constant string
		expected string
	}{
		{domain.LabelDomain, "gordon.domain"},
		{domain.LabelImage, "gordon.image"},
		{domain.LabelManaged, "gordon.managed"},
		{domain.LabelCreated, "gordon.created"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.constant != tt.expected {
				t.Errorf("constant value changed: got %q, want %q", tt.constant, tt.expected)
			}
		})
	}
}
