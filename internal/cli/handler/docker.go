package handler

import (
	"fmt"
	"io"

	"github.com/bnema/gordon/pkg/docker"
)

func ExportDockerImage(imageName string) (io.ReadCloser, int64, error) {
	exists, err := docker.CheckIfImageExists(imageName)
	if err != nil {
		return nil, 0, fmt.Errorf("error checking image existence: %w", err)
	}

	var imageID string
	if exists {
		imageID = imageName
	} else {
		imageID, err = docker.GetImageIDByName(imageName)
		if err != nil {
			return nil, 0, fmt.Errorf("error searching for image by name: %w", err)
		}
	}

	actualSize, err := docker.GetImageSizeFromReader(imageID)
	if err != nil {
		return nil, 0, fmt.Errorf("error retrieving image size: %w", err)
	}

	reader, err := docker.ExportImageFromEngine(imageID)
	if err != nil {
		return nil, 0, fmt.Errorf("error exporting image: %w", err)
	}

	return reader, actualSize, nil
}
