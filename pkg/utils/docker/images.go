package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
)

func ListContainerImages() ([]types.ImageSummary, error) {
	// Check if the Docker client has been initialized
	if dockerCli == nil {
		return nil, fmt.Errorf("Docker client has not been initialized")
	}

	// List images using the Docker client
	images, err := dockerCli.ImageList(context.Background(), types.ImageListOptions{})
	if err != nil {
		return nil, err
	}

	return images, nil
}
func DeleteContainerImage(imageID string) error {
	// Check if the Docker client has been initialized
	if dockerCli == nil {
		return fmt.Errorf("Docker client has not been initialized")
	}

	// Delete the image using the Docker client
	_, err := dockerCli.ImageRemove(context.Background(), imageID, types.ImageRemoveOptions{})
	if err != nil {
		return err
	}

	return nil
}
