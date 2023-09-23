package docker

import (
	"context"
	"fmt"
	"os"

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

func ImportImageToEngine(imagePath string) (string, error) {
	fmt.Println("Importing image to Docker engine")
	fmt.Println(imagePath)
	// Check if the Docker client has been initialized
	CheckIfInitialized()

	// Open the image file
	imageFile, err := os.Open(imagePath)
	if err != nil {
		return "", err
	}
	defer imageFile.Close()

	// Import the image into Docker
	_, err = dockerCli.ImageLoad(context.Background(), imageFile, false)
	if err != nil {
		return "", err
	}

	// List all images
	images, err := dockerCli.ImageList(context.Background(), types.ImageListOptions{})
	if err != nil {
		return "", err
	}

	// Search for the image we just loaded
	var imageID string
	for _, image := range images {
		for _, tag := range image.RepoTags {
			if tag == "alpine:latest" {
				imageID = image.ID
				break
			}
		}
		if imageID != "" {
			break
		}
	}

	if imageID == "" {
		return "", fmt.Errorf("could not find the loaded image")
	}

	return imageID, nil
}
