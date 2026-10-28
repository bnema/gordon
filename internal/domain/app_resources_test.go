package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseResourceSize(t *testing.T) {
	for value, want := range map[string]int64{"1b": 1, "2k": 2048, "512m": 512 << 20, "8g": 8 << 30, "8G": 8 << 30, "9223372036854775807b": 9223372036854775807} {
		got, err := ParseResourceSize(value)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	for _, value := range []string{"0b", "-1m", "1t", "1", "9223372036854775808b", "8589934592g"} {
		_, err := ParseResourceSize(value)
		require.Error(t, err, value)
	}
}

func TestResourcesAreRuntimeSpecChanges(t *testing.T) {
	original := AppService{Name: "web"}
	for _, limits := range []AppResources{{CPUs: .5}, {Memory: 512 << 20}, {Memory: 512 << 20, MemorySwap: 512 << 20}} {
		changed := original
		changed.Resources = limits
		assert.False(t, SameAppService(original, changed))
		assert.False(t, SameAppServiceRuntime(original, changed))
		assert.Contains(t, diffService("web", changed, original), "service/web/resources")
	}
}
