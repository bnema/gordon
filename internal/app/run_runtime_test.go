package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveRuntimeConfig_ExplicitPathPassedThrough(t *testing.T) {
	assert.Equal(t, "/custom/path.sock", resolveRuntimeConfig("/custom/path.sock"))
}

func TestResolveRuntimeConfig_AutoReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", resolveRuntimeConfig("auto"))
}

func TestResolveRuntimeConfig_EmptyReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", resolveRuntimeConfig(""))
}

func TestResolveRuntimeConfig_StripsUnixScheme(t *testing.T) {
	assert.Equal(t, "/var/run/docker.sock", resolveRuntimeConfig("unix:///var/run/docker.sock"))
}
