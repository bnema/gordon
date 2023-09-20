package docker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

type ContainerImage struct {
	ID         string
	Repository string
	Tag        string
	Created    time.Time
	Size       int64
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
	// Create a custom HTTP client that uses the Docker socket
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", config.Sock)
			},
		},
	}

	// Initialize Docker client
	cli, err := client.NewClientWithOpts(
		client.WithHTTPClient(httpClient),
		client.WithAPIVersionNegotiation(),
		client.WithScheme("unix"),
		client.WithHost(url.PathEscape(config.Sock)),
	)
	if err != nil {
		return err
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
			ID: image.ID,
			// Repository and Tag information may require additional processing
			Created: time.Unix(image.Created, 0),
			Size:    image.Size,
		})
	}

	return containerImages, nil
}

// // PodmanClient implements the ContainerEngineClient interface for Podman
// type PodmanClient struct{}

// func (p *PodmanClient) InitializeClient(config *Config) error {
// 	// Initialize Podman client here
// 	return nil // Return an error if something goes wrong
// }
// func InitializeEngineClient(config *Config) {
// 	if config.PodmanEnable {
// 		engineClient = &PodmanClient{}
// 	} else {
// 		engineClient = &DockerClient{}
// 	}
// 	if err := engineClient.InitializeClient(config); err != nil {
// 		log.Fatalf("Error initializing container engine client: %v", err)
// 	}
// }
