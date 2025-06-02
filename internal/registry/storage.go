package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Storage interface defines the contract for registry storage backends
type Storage interface {
	// Manifest operations
	GetManifest(name, reference string) ([]byte, error)
	PutManifest(name, reference string, data []byte) error
	DeleteManifest(name, reference string) error
	
	// Blob operations
	GetBlob(digest string) (io.ReadCloser, error)
	PutBlob(digest string, data io.Reader, size int64) error
	DeleteBlob(digest string) error
	BlobExists(digest string) bool
	
	// Upload operations
	StartBlobUpload(name string) (string, error) // returns upload UUID
	GetBlobUpload(uuid string) (io.WriteCloser, error)
	FinishBlobUpload(uuid, digest string) error
	CancelBlobUpload(uuid string) error
	
	// Tag operations
	ListTags(name string) ([]string, error)
	
	// Repository operations
	ListRepositories() ([]string, error)
}

// FilesystemStorage implements Storage using the local filesystem
type FilesystemStorage struct {
	rootDir string
}

// NewFilesystemStorage creates a new filesystem storage instance
func NewFilesystemStorage(rootDir string) (*FilesystemStorage, error) {
	// Create directory structure if it doesn't exist
	dirs := []string{
		filepath.Join(rootDir, "repositories"),
		filepath.Join(rootDir, "blobs"),
		filepath.Join(rootDir, "uploads"),
	}
	
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	
	log.Info().Str("root_dir", rootDir).Msg("Filesystem storage initialized")
	
	return &FilesystemStorage{
		rootDir: rootDir,
	}, nil
}

// Manifest operations

func (fs *FilesystemStorage) GetManifest(name, reference string) ([]byte, error) {
	manifestPath := fs.getManifestPath(name, reference)
	
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("manifest not found: %s/%s", name, reference)
		}
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}
	
	return data, nil
}

func (fs *FilesystemStorage) PutManifest(name, reference string, data []byte) error {
	manifestPath := fs.getManifestPath(name, reference)
	
	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err != nil {
		return fmt.Errorf("failed to create manifest directory: %w", err)
	}
	
	// Write manifest file
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}
	
	// Update tags list
	if err := fs.updateTagsList(name, reference); err != nil {
		log.Warn().Err(err).Str("name", name).Str("reference", reference).Msg("Failed to update tags list")
	}
	
	log.Info().Str("name", name).Str("reference", reference).Msg("Manifest stored")
	return nil
}

func (fs *FilesystemStorage) DeleteManifest(name, reference string) error {
	manifestPath := fs.getManifestPath(name, reference)
	
	if err := os.Remove(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("manifest not found: %s/%s", name, reference)
		}
		return fmt.Errorf("failed to delete manifest: %w", err)
	}
	
	// Remove from tags list
	if err := fs.removeFromTagsList(name, reference); err != nil {
		log.Warn().Err(err).Str("name", name).Str("reference", reference).Msg("Failed to update tags list")
	}
	
	log.Info().Str("name", name).Str("reference", reference).Msg("Manifest deleted")
	return nil
}

// Blob operations

func (fs *FilesystemStorage) GetBlob(digest string) (io.ReadCloser, error) {
	blobPath := fs.getBlobPath(digest)
	
	file, err := os.Open(blobPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("blob not found: %s", digest)
		}
		return nil, fmt.Errorf("failed to open blob: %w", err)
	}
	
	return file, nil
}

func (fs *FilesystemStorage) PutBlob(digest string, data io.Reader, size int64) error {
	blobPath := fs.getBlobPath(digest)
	
	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err != nil {
		return fmt.Errorf("failed to create blob directory: %w", err)
	}
	
	// Create temporary file first
	tmpPath := blobPath + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create temporary blob file: %w", err)
	}
	defer file.Close()
	
	// Copy data to file
	written, err := io.Copy(file, data)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write blob data: %w", err)
	}
	
	// Verify size if provided
	if size > 0 && written != size {
		os.Remove(tmpPath)
		return fmt.Errorf("blob size mismatch: expected %d, got %d", size, written)
	}
	
	// Move to final location
	if err := os.Rename(tmpPath, blobPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to move blob to final location: %w", err)
	}
	
	log.Info().Str("digest", digest).Int64("size", written).Msg("Blob stored")
	return nil
}

func (fs *FilesystemStorage) DeleteBlob(digest string) error {
	blobPath := fs.getBlobPath(digest)
	
	if err := os.Remove(blobPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("blob not found: %s", digest)
		}
		return fmt.Errorf("failed to delete blob: %w", err)
	}
	
	log.Info().Str("digest", digest).Msg("Blob deleted")
	return nil
}

func (fs *FilesystemStorage) BlobExists(digest string) bool {
	blobPath := fs.getBlobPath(digest)
	_, err := os.Stat(blobPath)
	return err == nil
}

// Upload operations

func (fs *FilesystemStorage) StartBlobUpload(name string) (string, error) {
	// Generate UUID-like upload ID using timestamp
	uuid := fmt.Sprintf("%d-%s", time.Now().UnixNano(), strings.ReplaceAll(name, "/", "_"))
	
	uploadPath := fs.getUploadPath(uuid)
	
	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(uploadPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create upload directory: %w", err)
	}
	
	// Create empty upload file
	file, err := os.Create(uploadPath)
	if err != nil {
		return "", fmt.Errorf("failed to create upload file: %w", err)
	}
	file.Close()
	
	log.Info().Str("uuid", uuid).Str("name", name).Msg("Blob upload started")
	return uuid, nil
}

func (fs *FilesystemStorage) GetBlobUpload(uuid string) (io.WriteCloser, error) {
	uploadPath := fs.getUploadPath(uuid)
	
	file, err := os.OpenFile(uploadPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("upload not found: %s", uuid)
		}
		return nil, fmt.Errorf("failed to open upload file: %w", err)
	}
	
	return file, nil
}

func (fs *FilesystemStorage) FinishBlobUpload(uuid, digest string) error {
	uploadPath := fs.getUploadPath(uuid)
	blobPath := fs.getBlobPath(digest)
	
	// Create blob directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err != nil {
		return fmt.Errorf("failed to create blob directory: %w", err)
	}
	
	// Move upload file to blob location
	if err := os.Rename(uploadPath, blobPath); err != nil {
		return fmt.Errorf("failed to move upload to blob location: %w", err)
	}
	
	log.Info().Str("uuid", uuid).Str("digest", digest).Msg("Blob upload finished")
	return nil
}

func (fs *FilesystemStorage) CancelBlobUpload(uuid string) error {
	uploadPath := fs.getUploadPath(uuid)
	
	if err := os.Remove(uploadPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("upload not found: %s", uuid)
		}
		return fmt.Errorf("failed to cancel upload: %w", err)
	}
	
	log.Info().Str("uuid", uuid).Msg("Blob upload cancelled")
	return nil
}

// Tag operations

func (fs *FilesystemStorage) ListTags(name string) ([]string, error) {
	tagsPath := fs.getTagsPath(name)
	
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

// Repository operations

func (fs *FilesystemStorage) ListRepositories() ([]string, error) {
	reposDir := filepath.Join(fs.rootDir, "repositories")
	
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

// Helper methods for path generation

func (fs *FilesystemStorage) getManifestPath(name, reference string) string {
	return filepath.Join(fs.rootDir, "repositories", name, "manifests", reference)
}

func (fs *FilesystemStorage) getBlobPath(digest string) string {
	// Split digest into directory structure (e.g., sha256:abc123... -> sha256/ab/abc123...)
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) != 2 {
		// Fallback if digest format is unexpected
		return filepath.Join(fs.rootDir, "blobs", digest)
	}
	
	algorithm := parts[0]
	hash := parts[1]
	
	// Create two-level directory structure for better performance
	if len(hash) >= 2 {
		return filepath.Join(fs.rootDir, "blobs", algorithm, hash[:2], hash)
	}
	
	return filepath.Join(fs.rootDir, "blobs", algorithm, hash)
}

func (fs *FilesystemStorage) getUploadPath(uuid string) string {
	return filepath.Join(fs.rootDir, "uploads", uuid)
}

func (fs *FilesystemStorage) getTagsPath(name string) string {
	return filepath.Join(fs.rootDir, "repositories", name, "tags.json")
}

// Helper methods for tags management

func (fs *FilesystemStorage) updateTagsList(name, reference string) error {
	tags, err := fs.ListTags(name)
	if err != nil {
		return err
	}
	
	// Add tag if not already present
	for _, tag := range tags {
		if tag == reference {
			return nil // Already exists
		}
	}
	
	tags = append(tags, reference)
	return fs.saveTagsList(name, tags)
}

func (fs *FilesystemStorage) removeFromTagsList(name, reference string) error {
	tags, err := fs.ListTags(name)
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
	
	return fs.saveTagsList(name, newTags)
}

func (fs *FilesystemStorage) saveTagsList(name string, tags []string) error {
	tagsPath := fs.getTagsPath(name)
	
	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(tagsPath), 0755); err != nil {
		return fmt.Errorf("failed to create tags directory: %w", err)
	}
	
	data, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("failed to marshal tags: %w", err)
	}
	
	if err := os.WriteFile(tagsPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write tags file: %w", err)
	}
	
	return nil
}