package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/docker/api/types"
)

func ListContainerImages() ([]types.ImageSummary, error) {
	// Check if the Docker client has been initialized
	CheckIfInitialized()

	// List images using the Docker client
	images, err := dockerCli.ImageList(context.Background(), types.ImageListOptions{})
	if err != nil {
		return nil, err
	}

	return images, nil
}
func DeleteContainerImage(imageID string) error {
	// Check if the Docker client has been initialized

	// Delete the image using the Docker client
	_, err := dockerCli.ImageRemove(context.Background(), imageID, types.ImageRemoveOptions{})
	if err != nil {
		return err
	}

	return nil
}

func LoadImage(imagePath string) (string, error) {
	// Check if the Docker client has been initialized
	CheckIfInitialized()

	// Open the image file
	imageFile, err := os.Open(imagePath)
	if err != nil {
		return "", err
	}
	defer imageFile.Close()

	// Load the image using the Docker client
	resp, err := dockerCli.ImageLoad(context.Background(), imageFile, false)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	// Read the JSON response to get the image ID
	decoder := json.NewDecoder(resp.Body)

	var imageID string
	for {
		var jm map[string]interface{}
		if err := decoder.Decode(&jm); err == io.EOF {
			break
		} else if err != nil {
			return "", fmt.Errorf("could not decode response: %v", err)
		}

		if id, exists := jm["stream"]; exists {
			if strID, ok := id.(string); ok {
				// Extract image ID from the stream.
				// This is a naive example; you may need to adjust this part
				// to correctly parse your specific Docker version's output.
				imageID = strings.TrimSpace(strID)
			}
		}
	}

	if imageID == "" {
		return "", fmt.Errorf("could not find image ID in response")
	}

	return imageID, nil
}
