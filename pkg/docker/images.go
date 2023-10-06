package docker

import (
	"context"
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

// DeleteContainerImage deletes an image from the Docker engine
func DeleteContainerImage(imageID string) error {
	_, err := dockerCli.ImageRemove(context.Background(), imageID, types.ImageRemoveOptions{})
	if err != nil {
		return err
	}

	return nil
}

func GetImageID(imageName string) (string, error) {
	// Check if the Docker client has been initialized
	CheckIfInitialized()

	// List all images
	images, err := dockerCli.ImageList(context.Background(), types.ImageListOptions{})
	if err != nil {
		return "", err
	}

	// Search for the image we just loaded
	var imageID string
	for _, image := range images {
		for _, tag := range image.RepoTags {
			if tag == imageName {
				imageID = image.ID
			}
		}
	}

	if imageID == "" {
		return "", err
	}

	return imageID, nil
}

// ImportImageToEngine imports an image to the Docker engine
func ImportImageToEngine(imagePath string) (string, error) {
	// Open the image file
	imageFile, err := os.Open(imagePath)
	if err != nil {
		return "", err
	}

	_, err = dockerCli.ImageLoad(context.Background(), imageFile, false)
	if err != nil {
		return "", err
	}
	// Close the image file
	imageFile.Close()

	// List all images
	images, err := dockerCli.ImageList(context.Background(), types.ImageListOptions{})
	if err != nil {
		return "", err
	}

	// Search for the image we just loaded
	var imageID string
	for _, image := range images {
		for _, tag := range image.RepoTags {
			if tag == "<none>:<none>" {
				imageID = image.ID
			}
		}
	}

	if imageID == "" {
		return "", err
	}

	return imageID, nil
}

// From an ID, get the all the information about the image
func GetImageInfo(imageID string) (*types.ImageInspect, error) {
	// Get the image information using the Docker client
	imageInfo, _, err := dockerCli.ImageInspectWithRaw(context.Background(), imageID)
	if err != nil {
		return nil, err
	}

	return &imageInfo, nil
}
