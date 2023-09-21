package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

type ContainerImage struct {
	ID          string   `json:"Id"`
	ParentID    string   `json:",omitempty"`
	RepoTags    []string `json:",omitempty"`
	RepoDigests []string `json:",omitempty"`
	Created     int64
	Size        int64
	SharedSize  int64
	Labels      map[string]string `json:",omitempty"`
	Containers  int64
}

type ContainerEngineClient interface {
	InitializeClient(*Config) error
	// Add other methods that both Docker and Podman should implement
}
type Config struct {
	Sock         string
	PodmanEnable bool
}

var engineClient ContainerEngineClient
var dockerCli *client.Client

// DockerClient implements the ContainerEngineClient interface for Docker
type DockerClient struct{}

func (d *DockerClient) InitializeClient(config *Config) error {
	// Validate if the Sock field is not empty
	if config.Sock == "" {
		return fmt.Errorf("Sock field in Config is empty")
	}

	// Initialize Docker client
	cli, err := client.NewClientWithOpts(
		client.WithAPIVersionNegotiation(),
		client.WithHost("unix://"+config.Sock), // Prepend "unix://" to the Unix socket path
	)
	if err != nil {
		return fmt.Errorf("Error from DockerClient: %s", err)
	}
	dockerCli = cli
	return nil
}

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

func ListRunningContainers() ([]types.Container, error) {
	// Check if the Docker client has been initialized
	if dockerCli == nil {
		return nil, fmt.Errorf("Docker client has not been initialized")
	}

	// List containers using the Docker client
	containers, err := dockerCli.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	return containers, nil
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
