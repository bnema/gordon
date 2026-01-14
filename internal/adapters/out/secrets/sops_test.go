package secrets

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConvertToSopsExtractPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "single key",
			input:    "password",
			expected: `["password"]`,
		},
		{
			name:     "two level nested",
			input:    "app.secret",
			expected: `["app"]["secret"]`,
		},
		{
			name:     "three level nested",
			input:    "app.database.password",
			expected: `["app"]["database"]["password"]`,
		},
		{
			name:     "key with underscores",
			input:    "app.database_password",
			expected: `["app"]["database_password"]`,
		},
		{
			name:     "key with hyphens",
			input:    "app.api-key",
			expected: `["app"]["api-key"]`,
		},
		{
			name:     "array index only",
			input:    "[0]",
			expected: `[0]`,
		},
		{
			name:     "key with array index",
			input:    "items[0]",
			expected: `["items"][0]`,
		},
		{
			name:     "nested key with array index",
			input:    "app.items[0]",
			expected: `["app"]["items"][0]`,
		},
		{
			name:     "nested key with array then key",
			input:    "app.items[0].name",
			expected: `["app"]["items"][0]["name"]`,
		},
		{
			name:     "multiple array indices",
			input:    "matrix[0][1]",
			expected: `["matrix"][0][1]`,
		},
		{
			name:     "deeply nested with array",
			input:    "config.databases[0].credentials.password",
			expected: `["config"]["databases"][0]["credentials"]["password"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertToSopsExtractPath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
