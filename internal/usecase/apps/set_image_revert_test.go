package apps_test

import (
	"context"
	"errors"
	"fmt"
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

// multiRevision is a revision of app blog with one service per name→image pair
// (in order). setImage names the service a set-image revision changed; empty
// marks an ordinary apply.
func multiRevision(id, supersedes, setImage string, images ...string) domain.AppDesiredRevision {
	spec := domain.AppSpec{Name: "blog"}
	for i := 0; i+1 < len(images); i += 2 {
		svc := testSpec("blog").Services[0]
		svc.Name, svc.Image = images[i], images[i+1]
		svc.HTTP = nil
		spec.Services = append(spec.Services, svc)
	}
	rev := domain.AppDesiredRevision{
		Revision: id, App: "blog", Supersedes: supersedes, Spec: spec, AcceptedAt: revertAcceptedAt,
	}
	if setImage != "" {
		for _, svc := range spec.Services {
			if svc.Name == setImage {
				rev.SetImage = &domain.AppSetImageProvenance{Service: setImage, Image: svc.Image, Principal: "oidc:https://ci#repo"}
			}
		}
	}
	return rev
}

func multiFileSpec(images ...string) domain.AppSpec {
	return multiRevision("file", "", "", images...).Spec
}

func revertSummary(reverts []domain.AppSetImageRevert) map[string]string {
	got := map[string]string{}
	for _, r := range reverts {
		got[r.Service] = r.Revision
	}
	return got
}

func TestApply_DetectsSetImageReverts_MultiService(t *testing.T) {
	// longChain is a set-image revision followed by gap unrelated applies that
	// keep its image. The walk limit (64 revisions, unexported) is hardcoded.
	longChain := func(gap int) []domain.AppDesiredRevision {
		history := []domain.AppDesiredRevision{multiRevision("rev-0", "", "web", "web", "img:2")}
		for i := 1; i <= gap; i++ {
			history = append(history, multiRevision(fmt.Sprintf("rev-%d", i), fmt.Sprintf("rev-%d", i-1), "", "web", "img:2"))
		}
		return history
	}
	tests := []struct {
		name    string
		history []domain.AppDesiredRevision
		file    domain.AppSpec
		want    map[string]string // service → flagging revision
	}{
		{
			name: "service added in the file is not flagged",
			history: []domain.AppDesiredRevision{
				multiRevision("rev-1", "", "", "web", "img:1"),
				multiRevision("rev-2", "rev-1", "web", "web", "img:2"),
			},
			file: multiFileSpec("web", "img:2", "worker", "w:1"),
			want: map[string]string{},
		},
		{
			name: "service removed from the file is not flagged",
			history: []domain.AppDesiredRevision{
				multiRevision("rev-1", "", "", "web", "img:1", "worker", "w:1"),
				multiRevision("rev-2", "rev-1", "worker", "web", "img:1", "worker", "w:2"),
			},
			file: multiFileSpec("web", "img:1"),
			want: map[string]string{},
		},
		{
			name: "set-image after the apply that introduced the service is flagged",
			history: []domain.AppDesiredRevision{
				multiRevision("rev-1", "", "", "web", "img:1"),
				multiRevision("rev-2", "rev-1", "", "web", "img:1", "worker", "w:1"),
				multiRevision("rev-3", "rev-2", "worker", "web", "img:1", "worker", "w:2"),
			},
			file: multiFileSpec("web", "img:1", "worker", "w:1"),
			want: map[string]string{"worker": "rev-3"},
		},
		{
			name: "value introduced by the apply that added the service is not flagged",
			history: []domain.AppDesiredRevision{
				multiRevision("rev-1", "", "", "web", "img:1"),
				multiRevision("rev-2", "rev-1", "", "web", "img:1", "worker", "w:1"),
				multiRevision("rev-3", "rev-2", "", "web", "img:1", "worker", "w:1", "extra", "x:1"),
			},
			file: multiFileSpec("web", "img:1", "worker", "w:9", "extra", "x:1"),
			want: map[string]string{},
		},
		{
			name: "two services changed by separate set-images are both flagged",
			history: []domain.AppDesiredRevision{
				multiRevision("rev-1", "", "", "web", "img:1", "worker", "w:1"),
				multiRevision("rev-2", "rev-1", "web", "web", "img:2", "worker", "w:1"),
				multiRevision("rev-3", "rev-2", "worker", "web", "img:2", "worker", "w:2"),
			},
			file: multiFileSpec("web", "img:1", "worker", "w:1"),
			want: map[string]string{"web": "rev-2", "worker": "rev-3"},
		},
		{
			name:    "set-image at the last visited revision is flagged",
			history: longChain(63),
			file:    multiFileSpec("web", "img:1"),
			want:    map[string]string{"web": "rev-0"},
		},
		{
			name:    "set-image beyond the walk limit is not flagged",
			history: longChain(64),
			file:    multiFileSpec("web", "img:1"),
			want:    map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := apps.NewService(historyStore(t, tt.history...), zerowrap.Default())

			_, dry, err := svc.Apply(context.Background(), tt.file, []byte("m"), apps.ApplyOptions{DryRun: true})

			require.NoError(t, err)
			require.NotNil(t, dry)
			assert.Equal(t, tt.want, revertSummary(dry.SetImageReverts))
		})
	}
}

func TestApply_SetImageRevertRevisionLoadErrorIsPropagated(t *testing.T) {
	loadErr := errors.New("disk on fire")
	desired := multiRevision("rev-2", "rev-1", "", "web", "img:2")
	store := outmocks.NewMockAppState(t)
	store.EXPECT().Recover(mock.Anything).Return(nil).Maybe()
	store.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Maybe()
	store.EXPECT().LoadDesired(mock.Anything, "blog").Return(desired, true, nil).Maybe()
	store.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Maybe()
	store.EXPECT().LoadRevision(mock.Anything, "blog", "rev-1").Return(domain.AppDesiredRevision{}, loadErr).Maybe()
	svc := apps.NewService(store, zerowrap.Default())

	_, _, err := svc.Apply(context.Background(), multiFileSpec("web", "img:1"), []byte("m"), apps.ApplyOptions{DryRun: true})

	require.ErrorIs(t, err, loadErr)
	assert.NotErrorIs(t, err, domain.ErrAppSetImageRevert)
}
