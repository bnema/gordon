package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

func TestCliRenderPruneSummary_ShowsGapDetail(t *testing.T) {
	var out bytes.Buffer
	summary := dto.PruneSummary{
		Gaps: []dto.PruneGap{{
			Source: "manifests",
			Reason: "unknown-manifest-unreadable",
			Detail: "manifest not found for app:v1: manifest not found: app/v1",
		}},
	}

	require.NoError(t, cliRenderPruneSummary(&out, summary))

	assert.Contains(t, out.String(), "unknown-manifest-unreadable")
	assert.Contains(t, out.String(), "manifest not found for app:v1")
}
