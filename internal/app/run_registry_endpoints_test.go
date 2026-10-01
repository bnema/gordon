package app

import "testing"

func TestNeedsInternalRegistryListener(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "specific IPv4", addr: "100.64.0.10", want: true},
		{name: "specific IPv6", addr: "fd00::1", want: true},
		{name: "IPv4 wildcard covers loopback", addr: "0.0.0.0", want: false},
		{name: "IPv6 wildcard", addr: "::", want: false},
		{name: "IPv4 loopback", addr: "127.0.0.1", want: false},
		{name: "IPv6 loopback", addr: "::1", want: false},
		{name: "empty wildcard", addr: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsInternalRegistryListener(tt.addr); got != tt.want {
				t.Fatalf("needsInternalRegistryListener(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}
