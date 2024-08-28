package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
)

func ListContainerImages() ([]image.Summary, error) {
	// Check if the Docker client has been initialized
	CheckIfInitialized()

	// List images using the Docker client
	images, err := dockerCli.ImageList(context.Background(), image.ListOptions{})
	if err != nil {
		return nil, err
	}

	return images, nil
}

// DeleteContainerImage deletes an image from the Docker engine
func DeleteContainerImage(imageID string) error {
	_, err := dockerCli.ImageRemove(context.Background(), imageID, image.RemoveOptions{})
	if err != nil {
		return err
	}

	return nil
}

// CheckIfImageExists checks if an image exists in the Docker engine
func CheckIfImageExists(imageID string) (bool, error) {
	// Get the image information using the Docker client
	_, err := GetImageInfo(imageID)
	if err != nil {
		return false, nil
	}

	return true, nil
}

func GetImageIDByName(imageName string) (string, error) {
	images, err := dockerCli.ImageList(context.Background(), image.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list images: %w", err)
	}

	normalizedName, tag := normalizeImageName(imageName)
	fmt.Printf("Searching for image: %s:%s\n", normalizedName, tag)

	tagPattern := createTagPattern(tag)

	for _, img := range images {
		for _, repoTag := range img.RepoTags {
			normalizedRepoTag, repoTagValue := normalizeImageName(repoTag)
			fmt.Printf("Comparing with: %s:%s\n", normalizedRepoTag, repoTagValue)

			// Check for exact match (including with docker.io/ prefix)
			if repoTag == imageName || repoTag == "docker.io/"+imageName {
				return img.ID, nil
			}

			// Check for localhost match
			if repoTag == "localhost/"+imageName {
				return img.ID, nil
			}

			// Check for match with normalized name and tag
			if (normalizedRepoTag == normalizedName ||
				normalizedRepoTag == "docker.io/"+normalizedName) &&
				tagPattern.MatchString(repoTagValue) {
				return img.ID, nil
			}
		}
	}

	return "", fmt.Errorf("image not found: %s", imageName)
}

func normalizeImageName(name string) (string, string) {
	// Split off the tag if present
	parts := strings.SplitN(name, ":", 2)
	normalizedName := parts[0]
	tag := "latest"
	if len(parts) > 1 {
		tag = parts[1]
	}

	// Remove any leading "docker.io/" if present
	normalizedName = strings.TrimPrefix(normalizedName, "docker.io/")

	// Handle repository names with multiple segments
	segments := strings.Split(normalizedName, "/")
	if len(segments) > 1 {
		// If there are multiple segments, join all but the last
		normalizedName = strings.Join(segments[:len(segments)-1], "/") + "/" + segments[len(segments)-1]
	}

	return normalizedName, tag
}

func createTagPattern(tag string) *regexp.Regexp {
	if tag == "" || tag == "latest" {
		// Match any tag if the requested tag is empty or "latest"
		return regexp.MustCompile(`.*`)
	}

	// Escape special regex characters in the tag
	escapedTag := regexp.QuoteMeta(tag)

	// Create a pattern that matches the tag exactly or as a prefix
	// This allows matching "1.0" with "1.0.1", "1.0-alpine", etc.
	return regexp.MustCompile(`^` + escapedTag + `($|[\.-])`)
}

// ImportImageToEngine imports an image to the Docker engine and returns the image ID
func ImportImageToEngine(imageFilePath string) (string, error) {
	log.Printf("Starting to import image from file: %s", imageFilePath)

	imageFile, err := os.Open(imageFilePath)
	if err != nil {
		log.Printf("Failed to open image file: %v", err)
		return "", fmt.Errorf("failed to open image file: %w", err)
	}
	defer imageFile.Close()

	log.Printf("Image file opened successfully")

	resp, err := dockerCli.ImageLoad(context.Background(), imageFile, true)
	if err != nil {
		log.Printf("Failed to load image: %v", err)
		return "", fmt.Errorf("failed to import image: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("Image load initiated, parsing response")

	var imageName string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		log.Printf("Response line: %s", line)

		var message struct {
			Stream string `json:"stream"`
		}
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			log.Printf("Failed to unmarshal JSON: %v", err)
			continue // Skip lines that aren't JSON
		}

		log.Printf("Parsed message stream: %s", message.Stream)

		if strings.HasPrefix(message.Stream, "Loaded image: ") {
			imageName = strings.TrimSpace(strings.TrimPrefix(message.Stream, "Loaded image: "))
			log.Printf("Found image name: %s", imageName)
			break
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading response: %v", err)
		return "", fmt.Errorf("error reading response: %w", err)
	}

	if imageName == "" {
		log.Printf("Failed to find image name in import response")
		return "", fmt.Errorf("failed to find image name in import response")
	}

	// Now we need to get the image ID from the image name
	imageID, err := GetImageIDByName(imageName)
	if err != nil {
		log.Printf("Failed to get image ID for name %s: %v", imageName, err)
		return "", fmt.Errorf("failed to get image ID: %w", err)
	}

	log.Printf("Successfully imported image with ID: %s", imageID)
	return imageID, nil
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

// list all images
func GetAllImages() ([]image.Summary, error) {
	// List images using the Docker client
	images, err := dockerCli.ImageList(context.Background(), image.ListOptions{})
	if err != nil {
		return nil, err
	}

	return images, nil
}
