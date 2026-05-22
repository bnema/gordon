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
	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
)

func TestTLSStatus_HumanOutput(t *testing.T) {
	notAfter := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	cpMock := climocks.NewMockControlPlane(t)
	cpMock.EXPECT().GetTLSStatus(context.Background()).Return(&dto.TLSStatusResponse{
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
	}, nil).Once()

	var buf bytes.Buffer
	err := runTLSStatusCmd(context.Background(), cpMock, &buf, false)
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

	cpMock := climocks.NewMockControlPlane(t)
	cpMock.EXPECT().GetTLSStatus(context.Background()).Return(&dto.TLSStatusResponse{
		ACMEEnabled:    true,
		ConfiguredMode: "acme",
		EffectiveMode:  "acme-staging",
		TokenSource:    "env",
		Certificates: []dto.TLSCertificateEntry{
			{
				ID:        "cert-abc123",
				Names:     []string{"example.com", "www.example.com"},
				Status:    "valid",
				NotAfter:  notAfter,
				LastError: "",
			},
		},
	}, nil).Once()

	var buf bytes.Buffer
	err := runTLSStatusCmd(context.Background(), cpMock, &buf, true)
	require.NoError(t, err)

	var result dto.TLSStatusResponse
	err = json.Unmarshal(buf.Bytes(), &result)
	require.NoError(t, err)

	assert.True(t, result.ACMEEnabled)
	assert.Equal(t, "acme", result.ConfiguredMode)
	assert.Equal(t, "acme-staging", result.EffectiveMode)
	assert.Equal(t, "env", result.TokenSource)
	require.Len(t, result.Certificates, 1)
	assert.Equal(t, "cert-abc123", result.Certificates[0].ID)
	assert.Equal(t, []string{"example.com", "www.example.com"}, result.Certificates[0].Names)
	assert.Equal(t, "valid", result.Certificates[0].Status)
	assert.Equal(t, notAfter.Format(time.RFC3339), result.Certificates[0].NotAfter.Format(time.RFC3339))
}
