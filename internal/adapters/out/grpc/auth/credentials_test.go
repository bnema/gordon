package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBearerTokenCredentialsMetadata(t *testing.T) {
	creds, err := NewBearerTokenCredentials("component-token")
	require.NoError(t, err)
	require.NotNil(t, creds)

	metadata, err := creds.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"authorization": "Bearer component-token",
	}, metadata)
}

func TestBearerTokenCredentialsTrimsToken(t *testing.T) {
	creds, err := NewBearerTokenCredentials("  component-token\t")
	require.NoError(t, err)

	metadata, err := creds.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Bearer component-token", metadata["authorization"])
}

func TestBearerTokenCredentialsRejectsEmptyToken(t *testing.T) {
	for _, token := range []string{"", " \t\n"} {
		creds, err := NewBearerTokenCredentials(token)
		require.Error(t, err)
		require.Nil(t, creds)
	}
}

func TestBearerTokenCredentialsRequireTransportSecurity(t *testing.T) {
	creds, err := NewBearerTokenCredentials("component-token")
	require.NoError(t, err)

	require.True(t, creds.RequireTransportSecurity())
}

func TestInsecureBearerTokenCredentialsExplicitlyPermitPlaintextTransport(t *testing.T) {
	creds, err := NewInsecureBearerTokenCredentials("component-token")
	require.NoError(t, err)

	require.False(t, creds.RequireTransportSecurity())
}
