package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
