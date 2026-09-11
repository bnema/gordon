package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// TestHandler_ImagesPrune_ExposesPerCandidateVerdicts proves the wire
// response carries the same plan shape for a dry run and an execution,
// including the reasons protected and unknown candidates were skipped.
func TestHandler_ImagesPrune_ExposesPerCandidateVerdicts(t *testing.T) {
	plan := domain.PruneReport{
		Applied: false,
		Candidates: []domain.PruneCandidateReport{
			{Kind: domain.PruneResourceRegistryTag, Ref: "app:v1", Verdict: domain.PruneVerdictEligible,
				Reasons: []domain.PruneReason{domain.PruneReasonEligibleRetention}},
			{Kind: domain.PruneResourceRuntimeImage, Ref: "sha256:active", Verdict: domain.PruneVerdictProtected,
				Reasons: []domain.PruneReason{domain.PruneReasonProtectedContainerUse}},
			{Kind: domain.PruneResourceRuntimeImage, Ref: "sha256:opaque", Verdict: domain.PruneVerdictUnknown,
				Reasons: []domain.PruneReason{domain.PruneReasonUnknownProvenance}},
		},
		Gaps: []domain.InventoryGap{{
			Source: domain.InventorySourceRuntimeContainers,
			Reason: domain.PruneReasonUnknownContainerUse,
			Detail: "list containers failed",
		}},
		Failures: []domain.PruneFailure{{
			Kind: domain.PruneResourceRegistryTag, Ref: "app:v9", Err: "storage unavailable",
		}},
	}

	var dryRunSeen bool
	imageSvc := &stubImageService{
		pruneFunc: func(_ context.Context, opts domain.ImagePruneOptions) (domain.ImagePruneReport, error) {
			dryRunSeen = opts.DryRun
			return domain.ImagePruneReport{Plan: plan}, nil
		},
	}

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ImageSvc = imageSvc
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/images/prune", bytes.NewBufferString(`{"dry_run": true}`))
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, dryRunSeen, "the dry-run flag must reach the use case")

	var resp dto.ImagePruneResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp.Plan.Applied)
	assert.Equal(t, 1, resp.Plan.Eligible)
	assert.Equal(t, 1, resp.Plan.Protected)
	assert.Equal(t, 1, resp.Plan.Unknown)
	require.Len(t, resp.Plan.Candidates, 3)
	assert.Equal(t, "protected-container-use", resp.Plan.Candidates[1].Reasons[0])
	require.Len(t, resp.Plan.Failures, 1)
	assert.Equal(t, "storage unavailable", resp.Plan.Failures[0].Error)
	require.Len(t, resp.Plan.Gaps, 1)
	assert.Equal(t, "runtime-containers", resp.Plan.Gaps[0].Source)
}

// TestHandler_VolumesPrune_ExposesPerCandidateVerdicts proves the volume
// endpoint returns the same shared plan shape, including a zero-deletion
// success.
func TestHandler_VolumesPrune_ExposesPerCandidateVerdicts(t *testing.T) {
	report := &domain.VolumePruneReport{
		Plan: domain.PruneReport{
			Applied: true,
			Candidates: []domain.PruneCandidateReport{
				{Kind: domain.PruneResourceVolume, Ref: "gordon-shop--web--vol--data", Verdict: domain.PruneVerdictProtected,
					Reasons: []domain.PruneReason{domain.PruneReasonProtectedOwnership}},
			},
		},
	}

	volumeSvc := inmocks.NewMockVolumeService(t)
	volumeSvc.EXPECT().PruneVolumes(mock.Anything, false).Return(report, []*domain.VolumeInfo{}, nil).Once()
	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.VolumeSvc = volumeSvc
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/volumes/prune", bytes.NewBufferString(`{"dry_run": false}`))
	req = req.WithContext(ctxWithScopes("admin:volumes:write"))
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.VolumePruneResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Plan.Applied)
	assert.Zero(t, resp.VolumesRemoved)
	assert.Equal(t, 1, resp.Plan.Protected)
	assert.Zero(t, resp.Plan.Eligible, "a protected candidate is not eligible")
	require.Len(t, resp.Plan.Candidates, 1)
	assert.Equal(t, "protected-ownership", resp.Plan.Candidates[0].Reasons[0])
}
