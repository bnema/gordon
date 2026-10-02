package deployment

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// TestServiceEnv_ServiceEnvOverridesAppEnv proves per-service env is merged
// over app-wide env (service wins) and stays scoped to its own service.
func TestServiceEnv_ServiceEnvOverridesAppEnv(t *testing.T) {
	svc := NewService(Deps{}, zerowrap.Default())
	appEnv := map[string]string{"LOG": "info", "APP_ENV": "production"}

	web := pinnedService{
		name: "web", appEnv: appEnv,
		spec: domain.AppService{Name: "web", Env: map[string]string{"LOG": "debug", "PORT": "3000"}},
	}
	got, err := svc.serviceEnv(context.Background(), "blog", web)
	require.NoError(t, err)
	assert.Equal(t, []string{"APP_ENV=production", "LOG=debug", "PORT=3000"}, got)

	worker := pinnedService{name: "worker", appEnv: appEnv, spec: domain.AppService{Name: "worker"}}
	got, err = svc.serviceEnv(context.Background(), "blog", worker)
	require.NoError(t, err)
	assert.Equal(t, []string{"APP_ENV=production", "LOG=info"}, got)
	assert.Equal(t, map[string]string{"LOG": "info", "APP_ENV": "production"}, appEnv, "app env is not mutated")
}

// TestServiceEnv_ServiceEnvSecretCollisionFails proves a service env key that
// collides with a secret key is refused at deploy time with no precedence.
func TestServiceEnv_ServiceEnvSecretCollisionFails(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil).Maybe()
	svc := NewService(Deps{State: state}, zerowrap.Default())
	p := pinnedService{name: "web", spec: domain.AppService{
		Name: "web", Env: map[string]string{"DB": "x"}, Secrets: map[string]string{"DB": "db"},
	}}

	_, err := svc.serviceEnv(context.Background(), "blog", p)
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
}
