package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bnema/gordon/internal/common"
)

var imagePath string

func SaveImageToStorage(config *common.Config, filename string, buf io.Reader) (string, error) {
	// Check if the folder exist if not create it
	if _, err := os.Stat(config.General.StorageDir); os.IsNotExist(err) {
		err := os.MkdirAll(config.General.StorageDir, 0755)
		if err != nil {
			return "", fmt.Errorf("failed to create storage directory: %v", err)
		}
	}

	// Define the path where the image will be saved
	imagePath = filepath.Join(config.General.StorageDir, filename)

	// Create or open a file for appending.
	outFile, err := os.Create(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %v", err)
	}

	// Write the uploaded file's content to the outFile
	_, err = io.Copy(outFile, buf)
	if err != nil {
		return "", fmt.Errorf("failed to write file: %v", err)
	}

	// Check if file exists
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		return "", fmt.Errorf("file does not exist: %v", err)
	}

	return imagePath, nil
}

func RemoveFromStorage() error {
	// Delete the file
	err := os.Remove(imagePath)
	if err != nil {
		return fmt.Errorf("failed to remove file: %v", err)
	}

	// clean up the path
	imagePath = ""

	return nil
}
