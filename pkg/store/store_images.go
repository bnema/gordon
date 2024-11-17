package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/common"
	"github.com/charmbracelet/log"
	"github.com/google/uuid"
)

func SaveImageToStorage(config *common.Config, originalFilename string, buf io.Reader) (string, error) {
	// Ensure the images directory exists
	imagesDir := filepath.Join(config.General.StorageDir, "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create images directory: %w", err)
	}

	// Generate a unique ID for the image
	id := uuid.New().String()

	// Create a filename with the ID and original extension
	ext := filepath.Ext(originalFilename)
	filename := id + ext

	// Full path for the new image
	imagePath := filepath.Join(imagesDir, filename)

	log.Info("saving image", "path", imagePath)

	// Create the file
	outFile, err := os.Create(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer outFile.Close()

	// Write the content
	written, err := io.Copy(outFile, buf)
	if err != nil {
		os.Remove(imagePath) // Clean up in case of error
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	log.Info("file written successfully",
		"bytes", written,
		"path", imagePath,
	)

	return imagePath, nil
}

func RemoveFromStorage(imagePath string) error {
	if imagePath == "" {
		return fmt.Errorf("empty image path")
	}

	log.Info("attempting to remove file", "path", imagePath)

	err := os.Remove(imagePath)
	if err != nil {
		return fmt.Errorf("failed to remove file: %w", err)
	}

	log.Info("file removed successfully", "path", imagePath)

	return nil
}
