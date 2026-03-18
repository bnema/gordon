package app

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestResolveRuntimeSocket_DefaultIsAuto(t *testing.T) {
	v := viper.New()
	setConfigDefaults(v)
	assert.Equal(t, "auto", v.GetString("server.runtime"))
}

func TestResolveRuntimeSocket_ExplicitPathPassedThrough(t *testing.T) {
	socket := resolveRuntimeConfig("/custom/path.sock")
	assert.Equal(t, "/custom/path.sock", socket)
}

func TestResolveRuntimeSocket_AutoReturnsEmpty(t *testing.T) {
	socket := resolveRuntimeConfig("auto")
	assert.Equal(t, "", socket)
}
