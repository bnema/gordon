package app

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerResourceLimitsCPUConversion(t *testing.T) {
	for _, tc := range []struct {
		cpus    float64
		want    int64
		invalid bool
	}{
		{cpus: 0}, {cpus: 1.001, want: 1001000000},
		{cpus: 1.0000000004, want: 1000000000}, {cpus: 1.0000000006, want: 1000000001},
		{cpus: -1, invalid: true}, {cpus: math.NaN(), invalid: true},
		{cpus: math.Inf(1), invalid: true}, {cpus: float64(math.MaxInt64) / 1e9, invalid: true},
	} {
		cfg := Config{}
		cfg.Containers.CPULimit = tc.cpus
		got, err := containerResourceLimits(cfg)
		if tc.invalid {
			require.ErrorContains(t, err, "containers.cpu_limit")
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, tc.want, got.NanoCPUs)
	}
}
