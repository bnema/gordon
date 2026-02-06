// Package filesystem implements storage adapters using the local filesystem.
package filesystem

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"

	"github.com/bnema/gordon/pkg/validation"
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
	blobPath, err := s.getBlobPath(digest)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

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
	path, err := s.getBlobPath(digest)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
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
	blobPath, err := s.getBlobPath(digest)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

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
	blobPath, err := s.getBlobPath(digest)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

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
	blobPath, err := s.getBlobPath(digest)
	if err != nil {
		return false // Invalid path means blob doesn't exist
	}
	_, err = os.Stat(blobPath)
	return err == nil
}

// StartBlobUpload starts a new blob upload and returns the upload UUID.
func (s *BlobStorage) StartBlobUpload(name string) (string, error) {
	uploadID := uuid.New().String()

	uploadPath, err := s.getUploadPath(uploadID)
	if err != nil {
		return "", fmt.Errorf("invalid upload path: %w", err)
	}

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
		Str("uuid", uploadID).
		Str("name", name).
		Msg("blob upload started")

	return uploadID, nil
}

// AppendBlobChunk appends data to an in-progress upload.
func (s *BlobStorage) AppendBlobChunk(name, uuid string, chunk []byte) (int64, error) {
	uploadPath, err := s.getUploadPath(uuid)
	if err != nil {
		return 0, fmt.Errorf("invalid upload path: %w", err)
	}

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
	uploadPath, err := s.getUploadPath(uuid)
	if err != nil {
		return nil, fmt.Errorf("invalid upload path: %w", err)
	}

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
	uploadPath, err := s.getUploadPath(uuid)
	if err != nil {
		return fmt.Errorf("invalid upload path: %w", err)
	}
	blobPath, err := s.getBlobPath(digest)
	if err != nil {
		return fmt.Errorf("invalid blob path: %w", err)
	}

	// Verify digest before moving to ensure integrity
	if err := s.verifyUploadDigest(uploadPath, digest); err != nil {
		return fmt.Errorf("digest verification failed: %w", err)
	}

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
	uploadPath, err := s.getUploadPath(uuid)
	if err != nil {
		return fmt.Errorf("invalid upload path: %w", err)
	}

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

// Helper methods for path generation with security validation

func (s *BlobStorage) getBlobPath(digest string) (string, error) {
	// Validate digest to prevent path traversal (defense in depth)
	if err := validation.ValidateDigest(digest); err != nil {
		return "", fmt.Errorf("invalid digest: %w", err)
	}

	// Split digest into directory structure (e.g., sha256:abc123... -> sha256/ab/abc123...)
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid digest format: missing algorithm separator")
	}

	algorithm := parts[0]
	hashPart := parts[1]

	var path string
	// Create two-level directory structure for better performance
	if len(hashPart) >= 2 {
		path = filepath.Join(s.rootDir, "blobs", algorithm, hashPart[:2], hashPart)
	} else {
		path = filepath.Join(s.rootDir, "blobs", algorithm, hashPart)
	}

	// Verify the path stays within root directory
	if err := validation.ValidatePathWithinRoot(s.rootDir, path); err != nil {
		return "", fmt.Errorf("path validation failed: %w", err)
	}

	return path, nil
}

func (s *BlobStorage) getUploadPath(uuid string) (string, error) {
	// Validate UUID to prevent path traversal
	if err := validation.ValidateUUID(uuid); err != nil {
		return "", fmt.Errorf("invalid UUID: %w", err)
	}

	path := filepath.Join(s.rootDir, "uploads", uuid)

	// Verify the path stays within root directory
	if err := validation.ValidatePathWithinRoot(s.rootDir, path); err != nil {
		return "", fmt.Errorf("path validation failed: %w", err)
	}

	return path, nil
}

// verifyUploadDigest computes and verifies the digest of an uploaded file.
func (s *BlobStorage) verifyUploadDigest(uploadPath, expectedDigest string) error {
	parts := strings.SplitN(expectedDigest, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid digest format")
	}

	algorithm := parts[0]
	expectedHash := parts[1]

	var hasher hash.Hash
	switch algorithm {
	case "sha256":
		hasher = sha256.New()
	case "sha512":
		hasher = sha512.New()
	default:
		return fmt.Errorf("unsupported hash algorithm: %s", algorithm)
	}

	file, err := os.Open(uploadPath)
	if err != nil {
		return fmt.Errorf("failed to open upload file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("failed to compute digest: %w", err)
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != expectedHash {
		return fmt.Errorf("digest mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}
