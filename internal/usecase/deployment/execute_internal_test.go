package deployment

import "testing"

func TestStripImageTag(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"localhost:15500/e2e/web:v1", "localhost:15500/e2e/web"},
		{"localhost:15500/e2e/web", "localhost:15500/e2e/web"},
		{"reg.example.com/blog/web:1.4.2", "reg.example.com/blog/web"},
		{"web:v1", "web"},
		{"web", "web"},
		{"reg.example.com/blog/web@sha256:abc", "reg.example.com/blog/web"},
	}
	for _, tc := range cases {
		if got := stripImageTag(tc.ref); got != tc.want {
			t.Errorf("stripImageTag(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}
