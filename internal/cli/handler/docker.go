package handler

import (
	"fmt"
	"io"

	"github.com/bnema/gordon/pkg/docker"
)

func ExportDockerImage(imageName string) (io.ReadCloser, int64, error) {
	// Initialize Docker client
	if err := docker.CheckIfInitialized(); err != nil {
		return nil, 0, fmt.Errorf("failed to initialize Docker client: %w", err)
	}

	// Get the image ID first
	imageID, err := docker.GetImageIDByName(imageName)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get image ID: %w", err)
	}

	// Create a pipe for streaming
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		// Export the image using save
		reader, err := docker.ExportImageFromEngine(imageID)
		if err != nil {
			pw.CloseWithError(fmt.Errorf("failed to export image: %w", err))
			return
		}
		defer reader.Close()

		// Copy the data to the pipe
		_, err = io.Copy(pw, reader)
		if err != nil {
			pw.CloseWithError(fmt.Errorf("failed to copy image data: %w", err))
			return
		}
	}()

	// Get the actual size
	size, err := docker.GetImageSize(imageID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get image size: %w", err)
	}

	return pr, size, nil
}
