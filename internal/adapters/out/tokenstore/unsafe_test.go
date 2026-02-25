package tokenstore

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// newTestUnsafeStore creates a UnsafeStore backed by a temp directory.
func newTestUnsafeStore(t *testing.T) *UnsafeStore {
	t.Helper()
	dir := t.TempDir()
	log := zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard})
	store, err := NewUnsafeStore(dir, log)
	if err != nil {
		t.Fatalf("NewUnsafeStore: %v", err)
	}
	return store
}

func TestUnsafeStoreRevokeConcurrentNoLoss(t *testing.T) {
	// Run with: go test -race ./internal/adapters/out/tokenstore/... -run TestUnsafeStoreRevokeConcurrentNoLoss -v
	store := newTestUnsafeStore(t)

	ctx := context.Background()
	const n = 10

	// Save n tokens
	tokenIDs := make([]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("token-%d", i)
		tokenIDs[i] = id
		tok := &domain.Token{
			ID:      id,
			Subject: fmt.Sprintf("user-%d", i),
			Scopes:  []string{"admin"},
		}
		if err := store.SaveToken(ctx, tok, "jwt-"+id); err != nil {
			t.Fatal(err)
		}
	}

	// Revoke all concurrently; collect errors via a buffered channel.
	var wg sync.WaitGroup
	errs := make(chan error, len(tokenIDs))
	for _, id := range tokenIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := store.Revoke(ctx, id); err != nil {
				errs <- fmt.Errorf("Revoke(%q): %w", id, err)
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// Verify all n token IDs are in the revoked list
	for _, id := range tokenIDs {
		revoked, err := store.IsRevoked(ctx, id)
		if err != nil {
			t.Errorf("IsRevoked(%q): %v", id, err)
			continue
		}
		if !revoked {
			t.Errorf("token %q should be revoked but IsRevoked returned false", id)
		}
	}
}
