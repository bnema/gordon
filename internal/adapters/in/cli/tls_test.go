package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

// tlsTestControlPlane implements ControlPlane for TLS tests.
type tlsTestControlPlane struct {
	getTLSStatus func(context.Context) (*dto.TLSStatusResponse, error)
}

var _ ControlPlane = (*tlsTestControlPlane)(nil)

func (c *tlsTestControlPlane) ListRoutesWithDetails(context.Context) ([]remote.RouteInfo, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetHealth(context.Context) (map[string]*remote.RouteHealth, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetRoute(context.Context, string) (*domain.Route, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) FindRoutesByImage(context.Context, string) ([]domain.Route, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) AddRoute(context.Context, domain.Route) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) UpdateRoute(context.Context, domain.Route) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) RemoveRoute(context.Context, string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) Bootstrap(context.Context, dto.BootstrapRequest) (*dto.BootstrapResponse, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) ListSecretsWithAttachments(context.Context, string) (*remote.SecretsListResult, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) SetSecrets(context.Context, string, map[string]string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) DeleteSecret(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) SetAttachmentSecrets(context.Context, string, string, map[string]string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) DeleteAttachmentSecret(context.Context, string, string, string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetAllAttachmentsConfig(context.Context) (map[string][]string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetAttachmentsConfig(context.Context, string) ([]string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) FindAttachmentTargetsByImage(context.Context, string) ([]string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) AddAttachment(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) RemoveAttachment(context.Context, string, string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetAutoRouteAllowedDomains(context.Context) ([]string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) AddAutoRouteAllowedDomain(context.Context, string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) RemoveAutoRouteAllowedDomain(context.Context, string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetTLSStatus(ctx context.Context) (*dto.TLSStatusResponse, error) {
	if c.getTLSStatus != nil {
		return c.getTLSStatus(ctx)
	}
	return nil, nil
}

func (c *tlsTestControlPlane) GetStatus(context.Context) (*remote.Status, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) Reload(context.Context) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetConfig(context.Context) (*remote.Config, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) DeployIntent(context.Context, string) error {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) Deploy(context.Context, string) (*remote.DeployResult, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) Restart(context.Context, string, bool) (*remote.RestartResult, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) ListTags(context.Context, string) ([]string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) ListBackups(context.Context, string) ([]dto.BackupJob, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) BackupStatus(context.Context) ([]dto.BackupJob, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) RunBackup(context.Context, string, string) (*dto.BackupRunResponse, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) DetectDatabases(context.Context, string) ([]dto.DatabaseInfo, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetProcessLogs(context.Context, int) ([]string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) GetContainerLogs(context.Context, string, int) ([]string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) StreamProcessLogs(context.Context, int) (<-chan string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) StreamContainerLogs(context.Context, string, int) (<-chan string, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) ListVolumes(context.Context) ([]dto.Volume, error) {
	panic("unexpected call")
}

func (c *tlsTestControlPlane) PruneVolumes(context.Context, dto.VolumePruneRequest) (*dto.VolumePruneResponse, error) {
	panic("unexpected call")
}

func TestTLSStatus_HumanOutput(t *testing.T) {
	notAfter := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	cp := &tlsTestControlPlane{
		getTLSStatus: func(_ context.Context) (*dto.TLSStatusResponse, error) {
			return &dto.TLSStatusResponse{
				ACMEEnabled:     true,
				ConfiguredMode:  "acme",
				EffectiveMode:   "acme-staging",
				SelectionReason: "configured",
				TokenSource:     "env",
				Certificates: []dto.TLSCertificateEntry{
					{
						ID:        "cert-abc123",
						Names:     []string{"example.com", "www.example.com"},
						Status:    "valid",
						NotAfter:  notAfter,
						LastError: "",
					},
				},
				Routes: []dto.TLSRouteCoverage{
					{
						Domain:       "example.com",
						Covered:      true,
						CoveredBy:    "cert-abc123",
						RequiredACME: true,
					},
					{
						Domain:       "internal.local",
						Covered:      false,
						RequiredACME: false,
						Error:        "self-signed cert",
					},
				},
				Errors: []string{"route internal.local has no ACME cert"},
			}, nil
		},
	}

	var buf bytes.Buffer
	err := runTLSStatusCmd(context.Background(), cp, &buf, false)
	require.NoError(t, err)

	output := buf.String()
	t.Log("Human output:\n", output)

	// Check mode info is present
	assert.Contains(t, output, "ACME:")
	assert.Contains(t, output, "enabled")
	assert.Contains(t, output, "Configured Mode:")
	assert.Contains(t, output, "acme")
	assert.Contains(t, output, "Effective Mode:")
	assert.Contains(t, output, "acme-staging")

	// Check token source (not value)
	assert.Contains(t, output, "Token Source:")
	assert.Contains(t, output, "env")
	assert.NotContains(t, output, "token_secret_value")

	// Check certificate ID
	assert.Contains(t, output, "cert-abc123")
	assert.Contains(t, output, "example.com, www.example.com")
	assert.Contains(t, output, "valid")
	assert.Contains(t, output, notAfter.Format(time.DateTime))

	// Check route coverage
	assert.Contains(t, output, "example.com")
	assert.Contains(t, output, "covered=yes")
	assert.Contains(t, output, "covered_by=cert-abc123")
	assert.Contains(t, output, "internal.local")
	assert.Contains(t, output, "covered=no")

	// Check errors section
	assert.Contains(t, output, "Errors")
	assert.Contains(t, output, "route internal.local has no ACME cert")
}

func TestTLSStatus_JSONOutput(t *testing.T) {
	notAfter := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	cp := &tlsTestControlPlane{
		getTLSStatus: func(_ context.Context) (*dto.TLSStatusResponse, error) {
			return &dto.TLSStatusResponse{
				ACMEEnabled:    true,
				ConfiguredMode: "acme",
				EffectiveMode:  "acme-staging",
				TokenSource:    "env",
			}, nil
		},
	}

	var buf bytes.Buffer
	err := runTLSStatusCmd(context.Background(), cp, &buf, true)
	require.NoError(t, err)

	var result dto.TLSStatusResponse
	err = json.Unmarshal(buf.Bytes(), &result)
	require.NoError(t, err)

	assert.True(t, result.ACMEEnabled)
	assert.Equal(t, "acme", result.ConfiguredMode)
	assert.Equal(t, "acme-staging", result.EffectiveMode)
	assert.Equal(t, "env", result.TokenSource)

	_ = notAfter
}
