package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

func TestClientApplyApp_ConflictBodies(t *testing.T) {
	revertBody := dto.AppSetImageRevertConflict{
		Error: dto.AppSetImageRevertCode, Message: "apply would revert a set-image change",
		DesiredRevision: "rev-2",
		SetImageReverts: []dto.AppSetImageRevertDTO{{Service: "web", ServerImage: "reg/web@sha256:new", FileImage: "img:1", Revision: "rev-2"}},
	}
	tests := []struct {
		name       string
		body       any
		wantRevert bool
	}{
		{name: "set-image revert decodes the reverts", body: revertBody, wantRevert: true},
		{name: "other conflict keeps the HTTP error shape", body: dto.AppError{Error: "state-conflict", Message: "desired revision is rev-3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq dto.AppApplyRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(tt.body)
			}))
			defer srv.Close()

			_, err := NewClient(srv.URL).ApplyApp(context.Background(), dto.AppApplyRequest{
				ManifestTOML: "x", AllowSetImageRevert: true, ExpectedRevision: "rev-1",
			})

			require.Error(t, err)
			assert.True(t, gotReq.AllowSetImageRevert, "allow_set_image_revert is sent")
			assert.Equal(t, "rev-1", gotReq.ExpectedRevision, "expected_revision is sent")
			var revert *SetImageRevertError
			if !tt.wantRevert {
				assert.False(t, errors.As(err, &revert))
				var httpErr *HTTPError
				require.ErrorAs(t, err, &httpErr)
				assert.Equal(t, "state-conflict", httpErr.Code)
				return
			}
			require.ErrorAs(t, err, &revert)
			assert.Equal(t, revertBody, revert.Conflict)
		})
	}
}
