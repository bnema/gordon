package appmanifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestParseResources(t *testing.T) {
	for _, cpu := range []string{"4", "0.5"} {
		spec, _, err := Parse([]byte("name = 'blog'\n[services.web]\nimage = 'nginx:latest'\n[services.web.resources]\ncpus = "+cpu+"\nmemory = '8g'\nmemory_swap = '8g'\n"), "")
		require.NoError(t, err)
		want := 4.0
		if cpu == "0.5" {
			want = 0.5
		}
		assert.Equal(t, domain.AppResources{CPUs: want, Memory: 8 << 30, MemorySwap: 8 << 30}, spec.Services[0].Resources)
	}
	spec, _, err := Parse([]byte("name = 'blog'\n[services.web]\nimage = 'nginx:latest'\n"), "")
	require.NoError(t, err)
	assert.Zero(t, spec.Services[0].Resources)
}

func TestParseResourcesErrors(t *testing.T) {
	for _, tc := range []struct{ body, key string }{
		{"cpus = 0", "cpus"}, {"cpus = -1", "cpus"}, {"cpus = nan", "cpus"}, {"cpus = inf", "cpus"},
		{"cpus = 1e20", "cpus"}, {"cpus = 1e-10", "cpus"},
		{"memory = '0b'", "memory"}, {"memory = '-1g'", "memory"}, {"memory = '512'", "memory"},
		{"memory = '8z'", "memory"}, {"memory = ''", "memory"}, {"memory = '9999999999999999999g'", "memory"},
		{"memory_swap = '1g'", "memory_swap"}, {"memory = '2g'\nmemory_swap = '1g'", "memory_swap"},
		{"memory = '2g'\nmemory_swap = '0b'", "memory_swap"}, {"typo = 1", "typo"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			_, _, err := Parse([]byte("name = 'blog'\n[services.web]\nimage = 'nginx:latest'\n[services.web.resources]\n"+tc.body+"\n"), "")
			require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.Contains(t, err.Error(), "web")
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}
