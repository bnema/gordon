package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

type previewStoreData struct {
	Previews []domain.PreviewRoute `json:"previews"`
}

// PreviewStore persists preview routes to a JSON file.
type PreviewStore struct {
	path string
	mu   sync.Mutex
}

// NewPreviewStore creates a new filesystem-backed preview store.
func NewPreviewStore(path string) *PreviewStore {
	return &PreviewStore{path: path}
}

func (s *PreviewStore) Load(_ context.Context) ([]domain.PreviewRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var store previewStoreData
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	return store.Previews, nil
}

func (s *PreviewStore) Save(_ context.Context, previews []domain.PreviewRoute) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(previews) == 0 {
		err := os.Remove(s.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	data, err := json.MarshalIndent(previewStoreData{Previews: previews}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
