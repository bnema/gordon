package apptraffic

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestHoldsLifecycle(t *testing.T) {
	h := NewHolds()
	now := time.Now()
	h.now = func() time.Time { return now }
	deadline := now.Add(time.Minute)
	_, ok := h.Until("a", "s")
	require.False(t, ok)
	h.Set("a", "s", deadline)
	until, ok := h.Until("a", "s")
	require.True(t, ok)
	require.Equal(t, deadline, until)
	h.Drop("a", "s")
	_, ok = h.Until("a", "s")
	require.False(t, ok)
	h.Set("a", "s", now)
	_, ok = h.Until("a", "s")
	require.False(t, ok)
	require.Empty(t, h.until)
}

func TestHoldsPrune(t *testing.T) {
	for _, tc := range []struct {
		name          string
		services      map[string]domain.AppEffectiveService
		expired, keep bool
	}{
		{name: "live", services: map[string]domain.AppEffectiveService{"s": {}}, keep: true},
		{name: "missing app"},
		{name: "missing service", services: map[string]domain.AppEffectiveService{}},
		{name: "tcp binds", services: map[string]domain.AppEffectiveService{"s": {BackendBinds: map[int]int{80: 8080}}}},
		{name: "udp binds", services: map[string]domain.AppEffectiveService{"s": {UDPBackendBinds: map[int]int{80: 8080}}}},
		{name: "expired", services: map[string]domain.AppEffectiveService{"s": {}}, expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHolds()
			now := time.Now()
			h.now = func() time.Time { return now }
			deadline := now.Add(time.Minute)
			if tc.expired {
				deadline = now
			}
			h.Set("a", "s", deadline)
			active := map[string]domain.AppActive{}
			if tc.services != nil {
				active["a"] = domain.AppActive{Services: tc.services}
			}
			h.Prune(active)
			require.Equal(t, tc.keep, len(h.until) == 1)
		})
	}
}
