package docker

import (
	"context"
	"fmt"
	"io"
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

	images, err := dockerCli.ImageList(context.Background(), types.ImageListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list images: %w", err)
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
		return "", fmt.Errorf("image not found")
	}

	return imageID, nil
}

// ImportImageToEngine imports an image to the Docker engine
func ImportImageToEngine(imageFilePath string) error {
	// Open the image file
	imageFile, err := os.Open(imageFilePath)
	if err != nil {
		return fmt.Errorf("failed to open image file: %w", err)
	}
	defer imageFile.Close()

	// Import the image using the Docker client
	importedImage, err := dockerCli.ImageLoad(context.Background(), imageFile, false)
	if err != nil {
		return fmt.Errorf("failed to import image: %w", err)
	}
	defer importedImage.Body.Close()

	return nil
}

// ExportImageFromEngine exports an image from the Docker engine and returns it as an io.Reader
func ExportImageFromEngine(imageID string) (io.ReadCloser, error) {
	// Check if the Docker client has been initialized

	// Get the image information using the Docker client
	imageInfo, err := GetImageInfo(imageID)
	if err != nil {
		return nil, err
	}

	if len(imageInfo.RepoTags) == 0 {
		return nil, fmt.Errorf("image has no tag")
	}

	// Export the image using the Docker client
	imageReader, err := dockerCli.ImageSave(context.Background(), []string{imageInfo.RepoTags[0]})
	if err != nil {
		return nil, fmt.Errorf("failed to export image: %w", err)
	}

	return imageReader, nil
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

// GetImageSize returns the size of an image
func GetImageSize(imageID string) (int64, error) {
	// Get the image information using the Docker client
	imageInfo, err := GetImageInfo(imageID)
	if err != nil {
		return 0, err
	}

	return imageInfo.Size, nil
}

// GetImageTag returns the tag of an image
func GetImageTag(imageID string) (string, error) {
	// Get the image information using the Docker client
	imageInfo, err := GetImageInfo(imageID)
	if err != nil {
		return "", err
	}

	return imageInfo.RepoTags[0], nil
}

// GetImageName returns the name of an image
func GetImageName(imageID string) (string, error) {
	// Get the image information using the Docker client
	imageInfo, err := GetImageInfo(imageID)
	if err != nil {
		return "", err
	}

	return imageInfo.RepoDigests[0], nil
}

// WhoAmI attempts to identify the Docker image digest of the container running this code.
func WhoAmI() (string, error) {
	gordonImage := "ghcr.io/bnema/gordon:latest"

	// Get the image information using the Docker client
	imageInfo, _, err := dockerCli.ImageInspectWithRaw(context.Background(), gordonImage)
	if err != nil {
		return "", err
	}

	return imageInfo.ID, nil
}

func GetImageSizeFromReader(imageID string) (int64, error) {
	// Export the image using the Docker client
	imageReader, err := dockerCli.ImageSave(context.Background(), []string{imageID})
	if err != nil {
		return 0, fmt.Errorf("failed to export image: %w", err)
	}

	// Read the entire stream to get the actual size
	actualSize := int64(0)
	buf := make([]byte, 1024) // A buffer for reading the stream
	for {
		n, err := imageReader.Read(buf)
		actualSize += int64(n)
		if err != nil {
			if err == io.EOF {
				break // End of file is reached
			}
			return 0, fmt.Errorf("failed to read image: %w", err)
		}
	}

	return actualSize, nil
}
