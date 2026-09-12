package docker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStopTimeout_ConvertsGraceToAPISeconds proves the runtime API never
// receives a truncated or negative grace: a non-positive grace keeps the
// runtime default (nil), and a fractional grace rounds up so the
// container always gets at least the declared time.
func TestStopTimeout_ConvertsGraceToAPISeconds(t *testing.T) {
	cases := []struct {
		name  string
		grace time.Duration
		want  *int
	}{
		{name: "zero keeps the runtime default", grace: 0, want: nil},
		{name: "negative keeps the runtime default", grace: -time.Second, want: nil},
		{name: "default app grace", grace: 10 * time.Second, want: intPtr(10)},
		{name: "sub-second rounds up", grace: 500 * time.Millisecond, want: intPtr(1)},
		{name: "fractional rounds up", grace: 1500 * time.Millisecond, want: intPtr(2)},
		{name: "exact seconds are preserved", grace: 25 * time.Second, want: intPtr(25)},
		{name: "the app cap is preserved", grace: 5 * time.Minute, want: intPtr(300)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stopTimeout(tc.grace)
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tc.want, *got)
		})
	}
}

func intPtr(v int) *int { return &v }
