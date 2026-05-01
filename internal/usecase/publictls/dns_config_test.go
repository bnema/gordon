package publictls

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultDNSConfig(t *testing.T) {
	cfg := DefaultDNSConfig()

	assert.Equal(t, []string{"1.1.1.1:53", "8.8.8.8:53"}, cfg.Resolvers)
	assert.Equal(t, 5*time.Minute, cfg.PropagationTimeout)
	assert.Equal(t, 5*time.Second, cfg.PollingInterval)
}

func TestDNSConfigValidateNormalizesResolvers(t *testing.T) {
	cfg := DNSConfig{
		Resolvers:          []string{" 1.1.1.1:53 ", "\t8.8.8.8:53\n"},
		PropagationTimeout: 5 * time.Minute,
		PollingInterval:    5 * time.Second,
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []string{"1.1.1.1:53", "8.8.8.8:53"}, cfg.Resolvers)
}

func TestDNSConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     DNSConfig
		wantErr string
	}{
		{
			name: "valid",
			cfg: DNSConfig{
				Resolvers:          []string{"1.1.1.1:53", "8.8.8.8:53"},
				PropagationTimeout: 5 * time.Minute,
				PollingInterval:    5 * time.Second,
			},
		},
		{
			name: "empty resolvers",
			cfg: DNSConfig{
				Resolvers:          nil,
				PropagationTimeout: 5 * time.Minute,
				PollingInterval:    5 * time.Second,
			},
			wantErr: "dns.resolvers must contain at least one resolver",
		},
		{
			name: "resolver missing port",
			cfg: DNSConfig{
				Resolvers:          []string{"1.1.1.1"},
				PropagationTimeout: 5 * time.Minute,
				PollingInterval:    5 * time.Second,
			},
			wantErr: "invalid dns.resolvers[0]",
		},
		{
			name: "whitespace-only resolver",
			cfg: DNSConfig{
				Resolvers:          []string{"   "},
				PropagationTimeout: 5 * time.Minute,
				PollingInterval:    5 * time.Second,
			},
			wantErr: "invalid dns.resolvers[0]",
		},
		{
			name: "malformed resolver port",
			cfg: DNSConfig{
				Resolvers:          []string{"1.1.1.1:abc"},
				PropagationTimeout: 5 * time.Minute,
				PollingInterval:    5 * time.Second,
			},
			wantErr: "invalid dns.resolvers[0]",
		},
		{
			name: "zero timeout",
			cfg: DNSConfig{
				Resolvers:          []string{"1.1.1.1:53"},
				PropagationTimeout: 0,
				PollingInterval:    5 * time.Second,
			},
			wantErr: "dns.propagation_timeout must be positive",
		},
		{
			name: "zero polling interval",
			cfg: DNSConfig{
				Resolvers:          []string{"1.1.1.1:53"},
				PropagationTimeout: 5 * time.Minute,
				PollingInterval:    0,
			},
			wantErr: "dns.polling_interval must be positive",
		},
		{
			name: "polling interval equals timeout",
			cfg: DNSConfig{
				Resolvers:          []string{"1.1.1.1:53"},
				PropagationTimeout: 5 * time.Second,
				PollingInterval:    5 * time.Second,
			},
			wantErr: "dns.polling_interval must be less than dns.propagation_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
