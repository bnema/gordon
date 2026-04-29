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
}
