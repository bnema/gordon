package app

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_ACMEDefaults(t *testing.T) {
	v := viper.New()
	require.NoError(t, loadConfig(v, ""))
	assert.False(t, v.GetBool("tls.acme.enabled"))
	assert.Equal(t, "", v.GetString("tls.acme.email"))
	assert.Equal(t, "auto", v.GetString("tls.acme.challenge"))
	assert.Equal(t, 1, v.GetInt("tls.acme.obtain_batch_size"))
	assert.Equal(t, []string{"1.1.1.1:53", "8.8.8.8:53"}, v.GetStringSlice("dns.resolvers"))
	assert.Equal(t, "5m", v.GetString("dns.propagation_timeout"))
	assert.Equal(t, "5s", v.GetString("dns.polling_interval"))
}
