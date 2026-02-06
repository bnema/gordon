package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShouldFallbackToLocal(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "connection refused", err: errors.New(`Post "https://reg.example.com/admin/restart": dial tcp 127.0.0.1:443: connect: connection refused`), want: true},
		{name: "status 503", err: errors.New("503 Service Unavailable: Registry Unavailable"), want: true},
		{name: "status 500", err: errors.New("500 Internal Server Error: failed to deploy container"), want: true},
		{name: "timeout", err: errors.New("request timeout"), want: true},
		{name: "auth error", err: errors.New("401 Unauthorized: invalid token"), want: false},
		{name: "bad request", err: errors.New("400 Bad Request: invalid domain"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldFallbackToLocal(tt.err))
		})
	}
}
