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
		{domain.LabelRoute, "gordon.route"},
		{domain.LabelAttachment, "gordon.attachment"},
		{domain.LabelAttachedTo, "gordon.attached-to"},
		{domain.LabelCreated, "gordon.created"},
		{domain.LabelComponent, "gordon.component"},
		{domain.LabelComponentRole, "gordon.component.role"},
		{domain.LabelComponentVersion, "gordon.component.version"},
		{domain.LabelComponentGeneration, "gordon.component.generation"},
		{domain.LabelComponentMigrationID, "gordon.component.migration-id"},
		{domain.LabelComponentOwner, "gordon.component.owner"},
		{domain.LabelComponentDesiredStateHash, "gordon.component.desired-state-hash"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.constant != tt.expected {
				t.Errorf("constant value changed: got %q, want %q", tt.constant, tt.expected)
			}
		})
	}
}
