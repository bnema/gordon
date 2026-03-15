package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_sortPortsHTTPFirst(t *testing.T) {
	tests := []struct {
		name     string
		input    []int
		expected []int
	}{
		{
			name:     "single port unchanged",
			input:    []int{8080},
			expected: []int{8080},
		},
		{
			name:     "SSH port pushed to end",
			input:    []int{22, 3000},
			expected: []int{3000, 22},
		},
		{
			name:     "HTTP ports sorted by priority",
			input:    []int{3000, 80, 8080},
			expected: []int{80, 8080, 3000},
		},
		{
			name:     "SSH pushed behind non-priority ports",
			input:    []int{22, 4000},
			expected: []int{4000, 22},
		},
		{
			name:     "mixed priority and non-priority ports",
			input:    []int{22, 3000, 4000, 80},
			expected: []int{80, 3000, 4000, 22},
		},
		{
			name:     "all non-priority ports sorted ascending",
			input:    []int{9090, 7000, 6000},
			expected: []int{6000, 7000, 9090},
		},
		{
			name:     "empty slice is no-op",
			input:    []int{},
			expected: []int{},
		},
		{
			name:     "gitea-style multi-port image",
			input:    []int{22, 3000},
			expected: []int{3000, 22},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ports := make([]int, len(tt.input))
			copy(ports, tt.input)
			sortPortsHTTPFirst(ports)
			assert.Equal(t, tt.expected, ports)
		})
	}
}
