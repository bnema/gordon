package ratelimit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStore_MemoryBackend(t *testing.T) {
	store, err := NewStore("memory", 10, 5, testLogger())
	require.NoError(t, err)
	require.NotNil(t, store)

	// Verify it's a MemoryStore
	_, ok := store.(*MemoryStore)
	assert.True(t, ok, "should create a MemoryStore")
}

func TestNewStore_EmptyBackend(t *testing.T) {
	store, err := NewStore("", 10, 5, testLogger())
	require.NoError(t, err)
	require.NotNil(t, store)

	// Empty backend should default to memory
	_, ok := store.(*MemoryStore)
	assert.True(t, ok, "empty backend should create a MemoryStore")
}

func TestNewStore_RedisBackend(t *testing.T) {
	store, err := NewStore("redis", 10, 5, testLogger())
	assert.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestNewStore_UnknownBackend(t *testing.T) {
	store, err := NewStore("postgres", 10, 5, testLogger())
	assert.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "unknown rate limit backend")
}
