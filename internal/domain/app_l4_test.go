package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestValidatePublishForListener_ExactTupleAccepted(t *testing.T) {
	cases := []struct {
		name       string
		publish    string
		transport  domain.NetworkProtocol
		listener   domain.EntryPointListener
		entrypoint string
	}{
		{
			name: "tcp wildcard explicit", publish: "0.0.0.0:25432", transport: domain.NetworkProtocolTCP,
			listener: domain.EntryPointListener{Address: "0.0.0.0:25432", Protocol: domain.EntryPointProtocolTCP}, entrypoint: "tcp",
		},
		{
			name: "tcp empty host equals wildcard listener", publish: "25432", transport: domain.NetworkProtocolTCP,
			listener: domain.EntryPointListener{Address: ":25432", Protocol: domain.EntryPointProtocolTCP}, entrypoint: "tcp",
		},
		{
			name: "tcp smart entrypoint", publish: "0.0.0.0:25432", transport: domain.NetworkProtocolTCP,
			listener: domain.EntryPointListener{Address: "0.0.0.0:25432", Protocol: domain.EntryPointProtocolSmartTCP}, entrypoint: "edge",
		},
		{
			name: "udp", publish: "0.0.0.0:28015", transport: domain.NetworkProtocolUDP,
			listener: domain.EntryPointListener{Address: "0.0.0.0:28015", Protocol: domain.EntryPointProtocolUDP}, entrypoint: "udp",
		},
		{
			name: "loopback listener with loopback publish", publish: "127.0.0.1:1543", transport: domain.NetworkProtocolTCP,
			listener: domain.EntryPointListener{Address: "127.0.0.1:1543", Protocol: domain.EntryPointProtocolTCP}, entrypoint: "local",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, domain.ValidatePublishForListener(tc.publish, tc.transport, tc.listener, tc.entrypoint))
		})
	}
}

func TestValidatePublishForListener_RejectsMismatch(t *testing.T) {
	wildcard := domain.EntryPointListener{Address: "0.0.0.0:25432", Protocol: domain.EntryPointProtocolTCP}
	cases := []struct {
		name      string
		publish   string
		transport domain.NetworkProtocol
		listener  domain.EntryPointListener
	}{
		{name: "loopback publish on wildcard listener", publish: "127.0.0.1:25432", transport: domain.NetworkProtocolTCP, listener: wildcard},
		{name: "port mismatch", publish: "0.0.0.0:15432", transport: domain.NetworkProtocolTCP, listener: wildcard},
		{name: "loopback vs wildcard host", publish: "127.0.0.1:25432", transport: domain.NetworkProtocolTCP, listener: wildcard},
		{name: "udp on tcp entrypoint", publish: "0.0.0.0:25432", transport: domain.NetworkProtocolUDP, listener: wildcard},
		{name: "tcp on udp entrypoint", publish: "0.0.0.0:25432", transport: domain.NetworkProtocolTCP, listener: domain.EntryPointListener{Address: "0.0.0.0:25432", Protocol: domain.EntryPointProtocolUDP}},
		{name: "hostname publish", publish: "db.internal:25432", transport: domain.NetworkProtocolTCP, listener: wildcard},
		{name: "missing port", publish: "0.0.0.0", transport: domain.NetworkProtocolTCP, listener: wildcard},
		{name: "invalid listener address", publish: "0.0.0.0:25432", transport: domain.NetworkProtocolTCP, listener: domain.EntryPointListener{Address: "25432", Protocol: domain.EntryPointProtocolTCP}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, domain.ValidatePublishForListener(tc.publish, tc.transport, tc.listener, "tcp"), domain.ErrInvalidAppSpec)
		})
	}
}

func TestCanonicalBindHost(t *testing.T) {
	assert.Equal(t, "0.0.0.0", domain.CanonicalBindHost(""))
	assert.Equal(t, "0.0.0.0", domain.CanonicalBindHost("   "))
	assert.Equal(t, "127.0.0.1", domain.CanonicalBindHost("127.0.0.1"))
	assert.Equal(t, "::1", domain.CanonicalBindHost("::1"))
}
