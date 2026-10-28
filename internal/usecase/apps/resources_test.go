package apps_test

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

func TestApplyRejectsResourceCeilingBeforePersistence(t *testing.T) {
	svc := apps.NewAppServiceImpl(newMockAppState(t), newMockDeployEngine(t), newMockSecretWriter(t), zerowrap.Default()).
		WithResourceLimits(deployment.ResourceLimits{NanoCPUs: 1e9})
	spec := testSpec("blog")
	spec.Services[0].Resources = domain.AppResources{CPUs: 2}
	_, _, err := svc.Apply(context.Background(), in.AppApplyInput{Spec: spec})
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	require.ErrorContains(t, err, `service "web" resources.cpus requested 2 exceeds installation limit 1`)
}
