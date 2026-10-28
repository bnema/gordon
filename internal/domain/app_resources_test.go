package domain

import (
	"math"
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

func TestCPUsToNanoCPUs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cpus    float64
		want    int64
		invalid bool
	}{
		{name: "unlimited", cpus: 0, want: 0},
		{name: "fractional", cpus: 1.001, want: 1001000000},
		{name: "round down", cpus: 1.0000000004, want: 1000000000},
		{name: "round up", cpus: 1.0000000006, want: 1000000001},
		{name: "half", cpus: 0.5e-9, want: 1},
		{name: "below half", cpus: math.Nextafter(0.5e-9, 0), invalid: true},
		{name: "negative", cpus: -1, invalid: true},
		{name: "nan", cpus: math.NaN(), invalid: true},
		{name: "infinity", cpus: math.Inf(1), invalid: true},
		{name: "multiply overflow", cpus: math.MaxFloat64, invalid: true},
		{name: "int64 overflow", cpus: float64(math.MaxInt64) / 1e9, invalid: true},
		{name: "below int64 overflow", cpus: math.Nextafter(float64(math.MaxInt64)/1e9, 0), want: 9223372036854774784},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CPUsToNanoCPUs(tc.cpus)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
