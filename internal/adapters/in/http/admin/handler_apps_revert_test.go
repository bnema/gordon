package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	in "github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

var handlerRevert = domain.AppSetImageRevert{
	Service: "web", ServerImage: "reg/web@sha256:new", FileImage: "img:1",
	Revision: "rev-2", Principal: "oidc:https://ci#repo", ChangedAt: time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC),
}

func TestHandler_AppApply_SetImageRevertIs409WithReverts(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().Apply(mock.Anything, mock.Anything).Return(nil, nil,
		fmt.Errorf("apps: %w", &domain.SetImageRevertError{DesiredRevision: "rev-2", Reverts: []domain.AppSetImageRevert{handlerRevert}}),
	).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/apply",
		dto.AppApplyRequest{ManifestTOML: validAppManifest}, "admin:apps:write")

	require.Equal(t, http.StatusConflict, rec.Code)
	var body dto.AppSetImageRevertConflict
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, dto.AppSetImageRevertCode, body.Error)
	assert.Equal(t, "rev-2", body.DesiredRevision)
	assert.Equal(t, []dto.AppSetImageRevertDTO{{
		Service: "web", ServerImage: "reg/web@sha256:new", FileImage: "img:1",
		Revision: "rev-2", Principal: "oidc:https://ci#repo", ChangedAt: handlerRevert.ChangedAt,
	}}, body.SetImageReverts)
}

func TestHandler_AppApply_ForwardsRevertOptions(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().Apply(mock.Anything, mock.MatchedBy(func(input in.AppApplyInput) bool {
		return input.AllowSetImageRevert && input.ExpectedRevision == "rev-2" && !input.DryRun
	})).Return(&in.AppApplyResult{
		App: "blog", ResultingRevision: "rev-3", Pending: true,
		SetImageReverts: []domain.AppSetImageRevert{handlerRevert},
	}, nil, nil).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/apply", dto.AppApplyRequest{
		ManifestTOML: validAppManifest, AllowSetImageRevert: true, ExpectedRevision: "rev-2",
	}, "admin:apps:write")

	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AppApplyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.SetImageReverts, 1)
	assert.Equal(t, "rev-2", resp.SetImageReverts[0].Revision)
}

func TestHandler_AppApply_DryRunReportsReverts(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().Apply(mock.Anything, mock.MatchedBy(func(input in.AppApplyInput) bool { return input.DryRun })).Return(
		nil, &in.AppDryRunResult{App: "blog", Valid: true, SetImageReverts: []domain.AppSetImageRevert{handlerRevert}}, nil,
	).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/apply",
		dto.AppApplyRequest{ManifestTOML: validAppManifest, DryRun: true}, "admin:apps:write")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"set_image_reverts"`)
	assert.Contains(t, rec.Body.String(), `"server_image":"reg/web@sha256:new"`)
}

func TestHandler_AppApply_StaleExpectedRevisionIsStateConflict(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)
	appSvc.EXPECT().Apply(mock.Anything, mock.Anything).Return(nil, nil,
		fmt.Errorf("apps: desired revision is rev-3, not rev-2: %w", domain.ErrAppStateConflict)).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/apply",
		dto.AppApplyRequest{ManifestTOML: validAppManifest, ExpectedRevision: "rev-2"}, "admin:apps:write")

	require.Equal(t, http.StatusConflict, rec.Code)
	var envelope dto.AppError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "state-conflict", envelope.Error)
}
