package apps_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
)

var revertAcceptedAt = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)

// revisionWithImage is one stored revision of app blog whose web service runs
// image. setImage marks it as a set-image revision for web.
func revisionWithImage(id, supersedes, image string, setImage bool) domain.AppDesiredRevision {
	spec := testSpec("blog")
	spec.Services[0].Image = image
	rev := domain.AppDesiredRevision{
		Revision: id, App: "blog", Supersedes: supersedes, Spec: spec, AcceptedAt: revertAcceptedAt,
	}
	if setImage {
		rev.SetImage = &domain.AppSetImageProvenance{Service: "web", Image: image, Principal: "oidc:https://ci#repo"}
	}
	return rev
}

// historyStore serves the given revision history; the last one is desired.
// A revision absent from history reports ErrAppRevisionNotFound.
func historyStore(t *testing.T, history ...domain.AppDesiredRevision) *outmocks.MockAppState {
	t.Helper()
	byID := map[string]domain.AppDesiredRevision{}
	for _, rev := range history {
		byID[rev.Revision] = rev
	}
	store := outmocks.NewMockAppState(t)
	store.EXPECT().Recover(mock.Anything).Return(nil).Maybe()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Maybe()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(history[len(history)-1], true, nil).Maybe()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Maybe()
	store.EXPECT().LoadRevision(mock.Anything, "blog", mock.Anything).RunAndReturn(
		func(_ context.Context, _, id string) (domain.AppDesiredRevision, error) {
			rev, ok := byID[id]
			if !ok {
				return domain.AppDesiredRevision{}, domain.ErrAppRevisionNotFound
			}
			return rev, nil
		}).Maybe()
	return store
}

func fileSpec(image string) domain.AppSpec {
	spec := testSpec("blog")
	spec.Services[0].Image = image
	return spec
}

func TestApply_DetectsSetImageReverts(t *testing.T) {
	base := revisionWithImage("rev-1", "", "img:1", false)
	tests := []struct {
		name      string
		history   []domain.AppDesiredRevision
		fileImage string
		wantRev   string
	}{
		{
			name:      "ordinary edit is not flagged",
			history:   []domain.AppDesiredRevision{base, revisionWithImage("rev-2", "rev-1", "img:2", false)},
			fileImage: "img:3",
		},
		{
			name:      "set-image value is flagged",
			history:   []domain.AppDesiredRevision{base, revisionWithImage("rev-2", "rev-1", "img:2", true)},
			fileImage: "img:1",
			wantRev:   "rev-2",
		},
		{
			name: "set-image value kept by a later unrelated apply is flagged",
			history: []domain.AppDesiredRevision{
				base,
				revisionWithImage("rev-2", "rev-1", "img:2", true),
				revisionWithImage("rev-3", "rev-2", "img:2", false),
			},
			fileImage: "img:1",
			wantRev:   "rev-2",
		},
		{
			name: "value introduced by a later apply after an older set-image is not flagged",
			history: []domain.AppDesiredRevision{
				base,
				revisionWithImage("rev-2", "rev-1", "img:2", true),
				revisionWithImage("rev-3", "rev-2", "img:3", false),
			},
			fileImage: "img:1",
		},
		{
			name:      "missing revision is not flagged",
			history:   []domain.AppDesiredRevision{revisionWithImage("rev-3", "rev-gone", "img:2", false)},
			fileImage: "img:1",
		},
		{
			name:      "file already matching the server is not flagged",
			history:   []domain.AppDesiredRevision{base, revisionWithImage("rev-2", "rev-1", "img:2", true)},
			fileImage: "img:2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := apps.NewService(historyStore(t, tt.history...), zerowrap.Default())
			spec := fileSpec(tt.fileImage)
			if tt.fileImage == "img:2" {
				spec.Env = map[string]string{"MODE": "edit"} // keep the apply from being a no-op
			}

			_, dry, err := svc.Apply(context.Background(), spec, []byte("m"), apps.ApplyOptions{DryRun: true})

			require.NoError(t, err)
			require.NotNil(t, dry)
			if tt.wantRev == "" {
				assert.Empty(t, dry.SetImageReverts)
				return
			}
			desired := tt.history[len(tt.history)-1]
			assert.Equal(t, []domain.AppSetImageRevert{{
				Service: "web", ServerImage: desired.Spec.Services[0].Image, FileImage: tt.fileImage,
				Revision: tt.wantRev, Principal: "oidc:https://ci#repo", ChangedAt: revertAcceptedAt,
			}}, dry.SetImageReverts)
		})
	}
}

func TestApply_SetImageRevertEnforcement(t *testing.T) {
	history := []domain.AppDesiredRevision{
		revisionWithImage("rev-1", "", "img:1", false),
		revisionWithImage("rev-2", "rev-1", "img:2", true),
	}
	tests := []struct {
		name       string
		opts       apps.ApplyOptions
		wantErr    error
		wantAccept bool
	}{
		{name: "refused without allow", opts: apps.ApplyOptions{}, wantErr: domain.ErrAppSetImageRevert},
		{name: "accepted with allow", opts: apps.ApplyOptions{AllowSetImageRevert: true}, wantAccept: true},
		{
			name:       "accepted with allow at the expected revision",
			opts:       apps.ApplyOptions{AllowSetImageRevert: true, ExpectedRevision: "rev-2"},
			wantAccept: true,
		},
		{
			name:    "stale expected revision is a state conflict",
			opts:    apps.ApplyOptions{AllowSetImageRevert: true, ExpectedRevision: "rev-1"},
			wantErr: domain.ErrAppStateConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := historyStore(t, history...)
			if tt.wantAccept {
				store.EXPECT().AcceptApply(mock.Anything, mock.MatchedBy(func(intent domain.AppApplyIntent) bool {
					return intent.Supersedes == "rev-2" && intent.Spec.Services[0].Image == "img:1"
				})).Return(nil).Once()
			}
			svc := apps.NewService(store, zerowrap.Default())

			result, _, err := svc.Apply(context.Background(), fileSpec("img:1"), []byte("m"), tt.opts)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				if tt.wantErr == domain.ErrAppSetImageRevert {
					var revert *domain.SetImageRevertError
					require.ErrorAs(t, err, &revert)
					assert.Equal(t, "rev-2", revert.DesiredRevision)
					require.Len(t, revert.Reverts, 1)
					assert.Equal(t, "img:2", revert.Reverts[0].ServerImage)
				}
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, result.SetImageReverts, 1, "the accepted apply reports what it replaced")
			assert.Equal(t, "rev-2", result.SetImageReverts[0].Revision)
		})
	}
}

func TestApply_StaleExpectedRevisionWithoutReverts(t *testing.T) {
	history := []domain.AppDesiredRevision{revisionWithImage("rev-1", "", "img:1", false)}
	svc := apps.NewService(historyStore(t, history...), zerowrap.Default())

	_, _, err := svc.Apply(context.Background(), fileSpec("img:9"), []byte("m"), apps.ApplyOptions{ExpectedRevision: "rev-0"})

	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	assert.NotErrorIs(t, err, domain.ErrAppSetImageRevert)
}
