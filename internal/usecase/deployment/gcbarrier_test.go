package deployment_test

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// TestPreflight_HoldsSharedGCBarrierAcrossResourceSelection proves the
// shared lease is taken before resource selection and released only
// when the mutation ends. Prune can therefore never observe a resource
// that was selected but whose protection is not yet durable.
func TestPreflight_HoldsSharedGCBarrierAcrossResourceSelection(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	barrier := outmocks.NewMockGCBarrier(t)
	lease := outmocks.NewMockGCLease(t)

	held := false
	barrier.EXPECT().AcquireShared(mock.Anything).Run(func(context.Context) {
		held = true
	}).Return(lease, nil).Once()
	lease.EXPECT().Release().Run(func() {
		held = false
	}).Once()

	rev := testRevision("blog", webService())
	// Image resolution is the resource-selection step: it must happen
	// inside the shared lease.
	expectFullPreflight(state, runtime, images, secrets, rev, nil, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "s3cr3t", nil)
	images.ExpectedCalls = nil
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Run(func(context.Context, string) {
		require.True(t, held, "image resolution must run inside the shared GC lease")
	}).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		ImagePolicy: domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}},
	}, zerowrap.Default()).WithGCBarrier(barrier)

	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.False(t, held, "the shared lease must be released when the operation ends")
}
