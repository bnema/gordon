package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
)

func ListContainerImages() ([]ContainerImage, error) {
	// Check if the Docker client has been initialized
	if dockerCli == nil {
		return nil, fmt.Errorf("Docker client has not been initialized")
	}

	// List images using the Docker client
	images, err := dockerCli.ImageList(context.Background(), types.ImageListOptions{})
	if err != nil {
		return nil, err
	}

	// Populate the ContainerImage slice
	var containerImages []ContainerImage
	for _, image := range images {
		containerImages = append(containerImages, ContainerImage{
			ID:          image.ID,
			ParentID:    image.ParentID,
			RepoTags:    image.RepoTags,
			RepoDigests: image.RepoDigests,
			Created:     image.Created,
			Size:        image.Size,
			SharedSize:  image.SharedSize,
			Labels:      image.Labels,
			Containers:  image.Containers,
		})
	}

	return containerImages, nil
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
