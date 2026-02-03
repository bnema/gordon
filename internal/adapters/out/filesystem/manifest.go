package filesystem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/pkg/validation"
)

// ManifestStorage implements the ManifestStorage interface using the local filesystem.
type ManifestStorage struct {
	rootDir string
	log     zerowrap.Logger
}

// NewManifestStorage creates a new filesystem manifest storage instance.
func NewManifestStorage(rootDir string, log zerowrap.Logger) (*ManifestStorage, error) {
	// Create directory structure if it doesn't exist
	reposDir := filepath.Join(rootDir, "repositories")
	if err := os.MkdirAll(reposDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create repositories directory: %w", err)
	}

	log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("root_dir", rootDir).
		Msg("manifest storage initialized")

	return &ManifestStorage{
		rootDir: rootDir,
		log:     log,
	}, nil
}

// GetManifest retrieves a manifest by name and reference.
// Returns the manifest data and content type.
func (s *ManifestStorage) GetManifest(name, reference string) ([]byte, string, error) {
	manifestPath, err := s.getManifestPath(name, reference)
	if err != nil {
		return nil, "", fmt.Errorf("invalid path: %w", err)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("manifest not found: %s/%s", name, reference)
		}
		return nil, "", fmt.Errorf("failed to read manifest: %w", err)
	}

	contentType, err := s.getManifestContentType(name, reference)
	if err != nil {
		// Fallback for manifests stored before content type tracking
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "filesystem").
			Err(err).
			Str("name", name).
			Str("reference", reference).
			Msg("could not get manifest content type, falling back to default")
		contentType = "application/vnd.docker.distribution.manifest.v2+json"
	}

	return data, contentType, nil
}

// PutManifest stores a manifest.
func (s *ManifestStorage) PutManifest(name, reference, contentType string, data []byte) error {
	manifestPath, err := s.getManifestPath(name, reference)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0750); err != nil {
		return fmt.Errorf("failed to create manifest directory: %w", err)
	}

	// Write manifest file
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	// Store content type
	if err := s.putManifestContentType(name, reference, contentType); err != nil {
		// Don't fail the whole operation, but log a warning
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "filesystem").
			Err(err).
			Str("name", name).
			Str("reference", reference).
			Msg("failed to store manifest content type")
	}

	// Update tags list
	if err := s.updateTagsList(name, reference); err != nil {
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "filesystem").
			Err(err).
			Str("name", name).
			Str("reference", reference).
			Msg("failed to update tags list")
	}

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("name", name).
		Str("reference", reference).
		Msg("manifest stored")

	return nil
}

// DeleteManifest removes a manifest.
func (s *ManifestStorage) DeleteManifest(name, reference string) error {
	manifestPath, err := s.getManifestPath(name, reference)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	if err := os.Remove(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("manifest not found: %s/%s", name, reference)
		}
		return fmt.Errorf("failed to delete manifest: %w", err)
	}

	// Delete content type file
	if err := s.deleteManifestContentType(name, reference); err != nil {
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "filesystem").
			Err(err).
			Str("name", name).
			Str("reference", reference).
			Msg("failed to delete manifest content type")
	}

	// Remove from tags list
	if err := s.removeFromTagsList(name, reference); err != nil {
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "filesystem").
			Err(err).
			Str("name", name).
			Str("reference", reference).
			Msg("failed to update tags list")
	}

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("name", name).
		Str("reference", reference).
		Msg("manifest deleted")

	return nil
}

// ListTags returns all tags for a repository.
func (s *ManifestStorage) ListTags(name string) ([]string, error) {
	tagsPath, err := s.getTagsPath(name)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	data, err := os.ReadFile(tagsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil // No tags file means no tags
		}
		return nil, fmt.Errorf("failed to read tags file: %w", err)
	}

	var tags []string
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, fmt.Errorf("failed to parse tags file: %w", err)
	}

	return tags, nil
}

// ListRepositories returns all repository names.
func (s *ManifestStorage) ListRepositories() ([]string, error) {
	reposDir := filepath.Join(s.rootDir, "repositories")

	var repositories []string
	err := filepath.Walk(reposDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() && path != reposDir {
			// Get relative path from repositories directory
			relPath, err := filepath.Rel(reposDir, path)
			if err != nil {
				return err
			}

			// Check if this directory contains manifests
			manifestsDir := filepath.Join(path, "manifests")
			if _, err := os.Stat(manifestsDir); err == nil {
				repositories = append(repositories, relPath)
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list repositories: %w", err)
	}

	return repositories, nil
}

// Helper methods for path generation with security validation

func (s *ManifestStorage) getManifestPath(name, reference string) (string, error) {
	// Validate name to prevent path traversal (defense in depth)
	if _, err := validation.ValidatePath(name); err != nil {
		return "", fmt.Errorf("invalid repository name: %w", err)
	}
	if _, err := validation.ValidatePath(reference); err != nil {
		return "", fmt.Errorf("invalid reference: %w", err)
	}

	path := filepath.Join(s.rootDir, "repositories", name, "manifests", reference)

	// Verify the path stays within root directory
	if err := validation.ValidatePathWithinRoot(s.rootDir, path); err != nil {
		return "", fmt.Errorf("path validation failed: %w", err)
	}

	return path, nil
}

func (s *ManifestStorage) getManifestContentTypePath(name, reference string) (string, error) {
	manifestPath, err := s.getManifestPath(name, reference)
	if err != nil {
		return "", err
	}
	return manifestPath + ".contenttype", nil
}

func (s *ManifestStorage) getTagsPath(name string) (string, error) {
	// Validate name to prevent path traversal
	if _, err := validation.ValidatePath(name); err != nil {
		return "", fmt.Errorf("invalid repository name: %w", err)
	}

	path := filepath.Join(s.rootDir, "repositories", name, "tags.json")

	// Verify the path stays within root directory
	if err := validation.ValidatePathWithinRoot(s.rootDir, path); err != nil {
		return "", fmt.Errorf("path validation failed: %w", err)
	}

	return path, nil
}

// Content type helpers

func (s *ManifestStorage) getManifestContentType(name, reference string) (string, error) {
	contentTypePath, err := s.getManifestContentTypePath(name, reference)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(contentTypePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *ManifestStorage) putManifestContentType(name, reference, contentType string) error {
	contentTypePath, err := s.getManifestContentTypePath(name, reference)
	if err != nil {
		return err
	}
	return os.WriteFile(contentTypePath, []byte(contentType), 0600)
}

func (s *ManifestStorage) deleteManifestContentType(name, reference string) error {
	contentTypePath, err := s.getManifestContentTypePath(name, reference)
	if err != nil {
		return err
	}
	err = os.Remove(contentTypePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Tags management helpers

func (s *ManifestStorage) updateTagsList(name, reference string) error {
	if validation.IsDigest(reference) {
		return nil
	}

	tags, err := s.ListTags(name)
	if err != nil {
		return err
	}

	// Drop any legacy digest entries before persisting
	filtered := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !validation.IsDigest(tag) {
			filtered = append(filtered, tag)
		}
	}
	tags = filtered

	// Add tag if not already present
	for _, tag := range tags {
		if tag == reference {
			return nil // Already exists
		}
	}

	tags = append(tags, reference)
	return s.saveTagsList(name, tags)
}

func (s *ManifestStorage) removeFromTagsList(name, reference string) error {
	tags, err := s.ListTags(name)
	if err != nil {
		return err
	}

	// Remove tag if present
	var newTags []string
	for _, tag := range tags {
		if tag != reference {
			newTags = append(newTags, tag)
		}
	}

	return s.saveTagsList(name, newTags)
}

func (s *ManifestStorage) saveTagsList(name string, tags []string) error {
	tagsPath, err := s.getTagsPath(name)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(tagsPath), 0750); err != nil {
		return fmt.Errorf("failed to create tags directory: %w", err)
	}

	data, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("failed to marshal tags: %w", err)
	}

	if err := os.WriteFile(tagsPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write tags file: %w", err)
	}

	return nil
}
