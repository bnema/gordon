package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestComponentLabelsAreStableAndValidated(t *testing.T) {
	request := ComponentLabelRequest{
		Role:             domain.ComponentRoleRuntime,
		Version:          "v2.4.0",
		Generation:       7,
		MigrationID:      "migration-20260702",
		Owner:            "gordon",
		DesiredStateHash: strings.Repeat("a", 64),
	}
	labels, err := BuildComponentLabels(request)
	require.NoError(t, err)
	assert.Equal(t, "true", labels[domain.LabelComponent])
	assert.Equal(t, "runtime", labels[domain.LabelComponentRole])
	assert.Equal(t, "v2.4.0", labels[domain.LabelComponentVersion])
	assert.Equal(t, "7", labels[domain.LabelComponentGeneration])
	assert.Equal(t, "migration-20260702", labels[domain.LabelComponentMigrationID])
	assert.Equal(t, "gordon", labels[domain.LabelComponentOwner])
	assert.Equal(t, strings.Repeat("a", 64), labels[domain.LabelComponentDesiredStateHash])

	for _, request := range []ComponentLabelRequest{
		{Role: "unknown", Version: "v1", Generation: 1, MigrationID: "m", Owner: "gordon", DesiredStateHash: strings.Repeat("a", 64)},
		{Role: domain.ComponentRoleEdge, Version: "", Generation: 1, MigrationID: "m", Owner: "gordon", DesiredStateHash: strings.Repeat("a", 64)},
		{Role: domain.ComponentRoleEdge, Version: "v1", Generation: 0, MigrationID: "m", Owner: "gordon", DesiredStateHash: strings.Repeat("a", 64)},
		{Role: domain.ComponentRoleEdge, Version: "v1", Generation: 1, MigrationID: "bad value", Owner: "gordon", DesiredStateHash: strings.Repeat("a", 64)},
		{Role: domain.ComponentRoleEdge, Version: "v1", Generation: 1, MigrationID: "m", Owner: "gordon", DesiredStateHash: "not-a-sha256"},
	} {
		_, err := BuildComponentLabels(request)
		assert.Error(t, err)
	}
}
