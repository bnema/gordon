package domain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsInternalDeploy(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		expected bool
	}{
		{
			name:     "returns false for plain context",
			ctx:      context.Background(),
			expected: false,
		},
		{
			name:     "returns true for internal deploy context",
			ctx:      WithInternalDeploy(context.Background()),
			expected: true,
		},
		{
			name:     "returns false for context with wrong type",
			ctx:      context.WithValue(context.Background(), ContextKeyInternalDeploy, "true"),
			expected: false,
		},
		{
			name:     "returns false for context with false value",
			ctx:      context.WithValue(context.Background(), ContextKeyInternalDeploy, false),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsInternalDeploy(tt.ctx)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestWithInternalDeploy(t *testing.T) {
	ctx := context.Background()

	// Before marking
	assert.False(t, IsInternalDeploy(ctx))

	// After marking
	internalCtx := WithInternalDeploy(ctx)
	assert.True(t, IsInternalDeploy(internalCtx))

	// Original context unchanged
	assert.False(t, IsInternalDeploy(ctx))
}
