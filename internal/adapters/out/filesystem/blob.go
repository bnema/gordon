// Package filesystem implements storage adapters using the local filesystem.
package filesystem

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
)

// BlobStorage implements the BlobStorage interface using the local filesystem.
type BlobStorage struct {
	rootDir string
	log     zerowrap.Logger
}

// NewBlobStorage creates a new filesystem blob storage instance.
func NewBlobStorage(rootDir string, log zerowrap.Logger) (*BlobStorage, error) {
	// Create directory structure if it doesn't exist
	dirs := []string{
		filepath.Join(rootDir, "blobs"),
		filepath.Join(rootDir, "uploads"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("root_dir", rootDir).
		Msg("blob storage initialized")

	return &BlobStorage{
		rootDir: rootDir,
		log:     log,
	}, nil
}

// GetBlob retrieves a blob by digest.
func (s *BlobStorage) GetBlob(digest string) (io.ReadCloser, error) {
	blobPath := s.getBlobPath(digest)

	file, err := os.Open(blobPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("blob not found: %s", digest)
		}
		return nil, fmt.Errorf("failed to open blob: %w", err)
	}

	return file, nil
}

// GetBlobPath returns the filesystem path to a blob.
func (s *BlobStorage) GetBlobPath(digest string) (string, error) {
	path := s.getBlobPath(digest)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("blob not found: %s", digest)
		}
		return "", err
	}
	return path, nil
}

// PutBlob stores a blob with the given digest.
func (s *BlobStorage) PutBlob(digest string, data io.Reader, size int64) error {
	blobPath := s.getBlobPath(digest)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(blobPath), 0750); err != nil {
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

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("digest", digest).
		Int64(zerowrap.FieldSize, written).
		Msg("blob stored")

	return nil
}

// DeleteBlob removes a blob by digest.
func (s *BlobStorage) DeleteBlob(digest string) error {
	blobPath := s.getBlobPath(digest)

	if err := os.Remove(blobPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("blob not found: %s", digest)
		}
		return fmt.Errorf("failed to delete blob: %w", err)
	}

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("digest", digest).
		Msg("blob deleted")

	return nil
}

// BlobExists checks if a blob exists.
func (s *BlobStorage) BlobExists(digest string) bool {
	blobPath := s.getBlobPath(digest)
	_, err := os.Stat(blobPath)
	return err == nil
}

// StartBlobUpload starts a new blob upload and returns the upload UUID.
func (s *BlobStorage) StartBlobUpload(name string) (string, error) {
	// Generate UUID-like upload ID using timestamp
	uuid := fmt.Sprintf("%d-%s", time.Now().UnixNano(), strings.ReplaceAll(name, "/", "_"))

	uploadPath := s.getUploadPath(uuid)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(uploadPath), 0750); err != nil {
		return "", fmt.Errorf("failed to create upload directory: %w", err)
	}

	// Create empty upload file
	file, err := os.Create(uploadPath)
	if err != nil {
		return "", fmt.Errorf("failed to create upload file: %w", err)
	}
	file.Close()

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("uuid", uuid).
		Str("name", name).
		Msg("blob upload started")

	return uuid, nil
}

// AppendBlobChunk appends data to an in-progress upload.
func (s *BlobStorage) AppendBlobChunk(name, uuid string, chunk []byte) (int64, error) {
	uploadPath := s.getUploadPath(uuid)

	file, err := os.OpenFile(uploadPath, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("upload not found: %s", uuid)
		}
		return 0, fmt.Errorf("failed to open upload file: %w", err)
	}
	defer file.Close()

	written, err := file.Write(chunk)
	if err != nil {
		return 0, fmt.Errorf("failed to write chunk to upload file: %w", err)
	}

	fi, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to get file info: %w", err)
	}

	s.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("uuid", uuid).
		Str("name", name).
		Int("chunk_size", written).
		Int64("total_size", fi.Size()).
		Msg("appended chunk to blob upload")

	return fi.Size(), nil
}

// GetBlobUpload returns a writer for the upload.
func (s *BlobStorage) GetBlobUpload(uuid string) (io.WriteCloser, error) {
	uploadPath := s.getUploadPath(uuid)

	file, err := os.OpenFile(uploadPath, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("upload not found: %s", uuid)
		}
		return nil, fmt.Errorf("failed to open upload file: %w", err)
	}

	return file, nil
}

// FinishBlobUpload completes an upload and moves it to blob storage.
func (s *BlobStorage) FinishBlobUpload(uuid, digest string) error {
	uploadPath := s.getUploadPath(uuid)
	blobPath := s.getBlobPath(digest)

	// Create blob directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(blobPath), 0750); err != nil {
		return fmt.Errorf("failed to create blob directory: %w", err)
	}

	// Move upload file to blob location
	if err := os.Rename(uploadPath, blobPath); err != nil {
		return fmt.Errorf("failed to move upload to blob location: %w", err)
	}

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("uuid", uuid).
		Str("digest", digest).
		Msg("blob upload finished")

	return nil
}

// CancelBlobUpload cancels an in-progress upload.
func (s *BlobStorage) CancelBlobUpload(uuid string) error {
	uploadPath := s.getUploadPath(uuid)

	if err := os.Remove(uploadPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("upload not found: %s", uuid)
		}
		return fmt.Errorf("failed to cancel upload: %w", err)
	}

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "filesystem").
		Str("uuid", uuid).
		Msg("blob upload cancelled")

	return nil
}

// Helper methods for path generation

func (s *BlobStorage) getBlobPath(digest string) string {
	// Split digest into directory structure (e.g., sha256:abc123... -> sha256/ab/abc123...)
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) != 2 {
		// Fallback if digest format is unexpected
		return filepath.Join(s.rootDir, "blobs", digest)
	}

	algorithm := parts[0]
	hash := parts[1]

	// Create two-level directory structure for better performance
	if len(hash) >= 2 {
		return filepath.Join(s.rootDir, "blobs", algorithm, hash[:2], hash)
	}

	return filepath.Join(s.rootDir, "blobs", algorithm, hash)
}

func (s *BlobStorage) getUploadPath(uuid string) string {
	return filepath.Join(s.rootDir, "uploads", uuid)
}
