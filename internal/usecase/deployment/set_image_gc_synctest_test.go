package deployment_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// pruneBarrier models prune holding the exclusive GC lease: shared leases
// block on a channel until the exclusive holder releases.
type pruneBarrier struct{ released chan struct{} }

type nopLease struct{}

func (nopLease) Release() {}

func (b *pruneBarrier) AcquireShared(ctx context.Context) (out.GCLease, error) {
	select {
	case <-b.released:
		return nopLease{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *pruneBarrier) AcquireExclusive(context.Context) (out.GCLease, error) {
	return nil, errors.New("not used")
}

// TestStartSetImage_RefusedBeforeAnyLockOrState proves authorization and
// request validation are pure: an unauthorized principal, a malformed image,
// or a daemon without an installation registry never takes the GC lease,
// never recovers the store, and never reconciles the app. The mock state has
// no expectations, so any state call fails the test. Synchronous: no
// goroutines or time are involved.
func TestStartSetImage_RefusedBeforeAnyLockOrState(t *testing.T) {
	validate := func(domain.AppSpec) error { return nil }
	policies, err := domain.NewSetImagePolicySet([]domain.SetImagePolicy{{
		Issuer: ciPrincipal.Issuer, Subjects: []string{ciPrincipal.Subject}, App: "blog", Service: "web", Repository: "web",
	}})
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		registry string
		input    deployment.SetImageInput
		want     error
	}{
		{name: "unauthorized principal", registry: promoRegistry, want: domain.ErrSetImageDenied, input: deployment.SetImageInput{
			App: "blog", Service: "web", Image: "web@" + setImageDigest("c"), Op: "k", Validate: validate,
			Principal: domain.WorkloadPrincipal{Issuer: ciPrincipal.Issuer, Subject: "repo:acme/other"},
		}},
		{name: "tag image", registry: promoRegistry, want: domain.ErrInvalidImageReference, input: deployment.SetImageInput{
			App: "blog", Service: "web", Image: "web:latest", Op: "k", Validate: validate, Principal: ciPrincipal,
		}},
		{name: "no installation registry", want: domain.ErrSetImageUnavailable, input: deployment.SetImageInput{
			App: "blog", Service: "web", Image: "web@" + setImageDigest("c"), Op: "k", Validate: validate, Principal: ciPrincipal,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := outmocks.NewMockAppState(t)
			barrier := outmocks.NewMockGCBarrier(t)
			svc := deployment.NewService(deployment.Deps{
				State: state, Registry: deployment.RegistryConfig{Domain: tc.registry},
				Manifests: outmocks.NewMockManifestStorage(t),
			}, zerowrap.Default()).WithGCBarrier(barrier)
			svc.SetSetImageConfig(deployment.SetImageConfig{Policies: policies})
			_, err := svc.StartSetImage(context.Background(), tc.input)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// TestStartSetImage_WaitsForPruneExclusiveLease proves an image change racing
// prune never reads or writes state while prune holds the exclusive lease:
// it is durably blocked on the barrier, and it proceeds only after release.
// The bubble holds only mocks and channels (no bbolt, no sockets), so
// synctest.Wait observes the blocked goroutine deterministically.
func TestStartSetImage_WaitsForPruneExclusiveLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := outmocks.NewMockAppState(t)
		barrier := &pruneBarrier{released: make(chan struct{})}
		svc := deployment.NewService(deployment.Deps{
			State: state, Registry: deployment.RegistryConfig{Domain: promoRegistry},
			Manifests: outmocks.NewMockManifestStorage(t),
		}, zerowrap.Default()).WithGCBarrier(barrier)

		recovered := false
		state.EXPECT().Recover(mock.Anything).RunAndReturn(func(context.Context) error {
			recovered = true
			return domain.ErrAppStateIO
		}).Once()

		var err error
		done := false
		go func() {
			_, err = svc.StartSetImage(context.Background(), deployment.SetImageInput{
				App: "blog", Service: "web", Image: "web@" + setImageDigest("c"), Op: "k",
				Validate: func(domain.AppSpec) error { return nil }, LocalOwner: true,
			})
			done = true
		}()
		synctest.Wait()
		assert.False(t, recovered, "no state access while prune holds the exclusive lease")
		assert.False(t, done)

		close(barrier.released)
		synctest.Wait()
		require.True(t, done)
		assert.True(t, recovered)
		require.ErrorIs(t, err, domain.ErrAppStateIO)
	})
}
