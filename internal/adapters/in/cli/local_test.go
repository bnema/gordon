package cli

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestHasLocalTLSCapableEntrypoint(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config map[string]any
		want   bool
	}{
		{name: "smart tcp", config: map[string]any{"edge": map[string]any{"protocol": "smart_tcp"}}, want: true},
		{name: "tls mux", config: map[string]any{"edge": map[string]any{"protocol": "tls_mux"}}, want: true},
		{name: "plain tcp", config: map[string]any{"edge": map[string]any{"protocol": "tcp"}}, want: false},
		{name: "empty", config: map[string]any{}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			v.Set("entrypoints", tt.config)
			require.Equal(t, tt.want, hasLocalTLSCapableEntrypoint(v))
		})
	}
}
