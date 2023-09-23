package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/pkg/utils/docker"
	"github.com/docker/docker/api/types"
)

type StorageConfig struct {
	StorageDir string
	ImageID    string
	Images     []types.ImageSummary
}

func NewStorageConfig(config *app.AppConfig) *StorageConfig {
	return &StorageConfig{
		StorageDir: config.General.StorageDir,
	}
}

func (sc *StorageConfig) SaveImageToStorage(imagePath string) (string, error) {

	imageId, err := docker.LoadImage(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to load image: %v", err)
	}

	// Generate the new path for the image in the storage directory
	newPath := filepath.Join(sc.StorageDir, imageId+".tar")

	err = os.Rename(imagePath, newPath)
	if err != nil {
		return "", fmt.Errorf("failed to move image to storage directory: %v", err)
	}

	return imageId, nil
}
