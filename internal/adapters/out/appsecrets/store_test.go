package appsecrets

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestValidateAppSecretPath(t *testing.T) {
	store := NewStore(zerowrap.Default())
	ctx := context.Background()

	// Path validation fires before any pass invocation.
	require.ErrorIs(t, func() error { return store.SetSecret(ctx, "elsewhere/x", "v") }(), domain.ErrInvalidAppSpec)
	require.ErrorIs(t, func() error { return store.SetSecret(ctx, "gordon/apps/a/../b", "v") }(), domain.ErrInvalidAppSpec)
	require.ErrorIs(t, func() error { return store.SetSecret(ctx, "gordon/apps/blog/web/db", "a\nb") }(), domain.ErrInvalidAppSpec)
	assert.NotNil(t, store)
}
