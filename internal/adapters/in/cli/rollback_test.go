package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type failingWriter struct{}

func (failingWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestSortSemverTags(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		wantTags []string
	}{
		{
			name:     "v-prefixed tags sorted descending",
			tags:     []string{"v1.0.0", "v2.0.0", "v1.5.0"},
			wantTags: []string{"v2.0.0", "v1.5.0", "v1.0.0"},
		},
		{
			name:     "non-v-prefixed tags sorted as semver",
			tags:     []string{"1.0.0", "2.0.0", "1.5.0"},
			wantTags: []string{"2.0.0", "1.5.0", "1.0.0"},
		},
		{
			name:     "mixed v and non-v prefixed",
			tags:     []string{"v1.0.0", "2.0.0", "1.5.0"},
			wantTags: []string{"2.0.0", "1.5.0", "v1.0.0"}, // all recognized as semver, sorted desc
		},
		{
			name:     "pre-release tags",
			tags:     []string{"v1.0.0-rc1", "v1.0.0", "1.0.0-beta"},
			wantTags: []string{"v1.0.0", "v1.0.0-rc1", "1.0.0-beta"}, // release > pre-release
		},
		{
			name:     "non-semver tags appended after",
			tags:     []string{"latest", "v1.0.0", "dev"},
			wantTags: []string{"v1.0.0", "latest", "dev"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTags := sortSemverTags(tt.tags)
			assert.Equal(t, tt.wantTags, gotTags)
		})
	}
}

func TestValidateTagExists(t *testing.T) {
	assert.True(t, validateTagExists("v1.0.0", []string{"v1.0.0", "v2.0.0"}))
	assert.False(t, validateTagExists("v3.0.0", []string{"v1.0.0", "v2.0.0"}))
}

func TestPrintRollbackTags_WritesToProvidedWriter(t *testing.T) {
	var buf bytes.Buffer

	err := printRollbackTags(&buf, "myapp.example.com", "v1.2.0", []string{"v1.2.0", "v1.1.0"})
	assert.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Available tags for")
	assert.Contains(t, output, "myapp.example.com")
	assert.Contains(t, output, "- v1.2.0 (current)")
	assert.Contains(t, output, "- v1.1.0")
}

func TestPrintRollbackTags_ReturnsWriteError(t *testing.T) {
	err := printRollbackTags(failingWriter{}, "myapp.example.com", "v1.2.0", []string{"v1.2.0"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "write failed")
}
