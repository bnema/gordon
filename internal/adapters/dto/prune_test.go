package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestPruneSummaryFromDomain_CarriesWarnings(t *testing.T) {
	summary := PruneSummaryFromDomain(domain.PruneReport{Warnings: []string{"index app@latest lists missing child manifest sha256:abc"}})

	data, err := json.Marshal(summary)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"warnings":["index app@latest lists missing child manifest sha256:abc"]`)

	empty, err := json.Marshal(PruneSummaryFromDomain(domain.PruneReport{}))
	require.NoError(t, err)
	assert.NotContains(t, string(empty), "warnings")
}
