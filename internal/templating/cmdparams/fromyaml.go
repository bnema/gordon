package cmdparams

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/pkg/docker"
)

type YAMLContainerParams struct {
	Name        string            `yaml:"Name"`
	Image       string            `yaml:"Image"`
	Hostname    string            `yaml:"Hostname"`
	Ports       []string          `yaml:"Ports"`
	Volumes     []string          `yaml:"Volumes"`
	Environment []string          `yaml:"Environment"`
	Labels      map[string]string `yaml:"Labels"`
	Network     []string          `yaml:"Network"`
	Restart     string            `yaml:"Restart"`
}

// FromYAMLStructToCmdParams converts a YAMLContainerParams struct to a ContainerCommandParams struct
func FromYAMLStructToCmdParams(yamlParams YAMLContainerParams) (docker.ContainerCommandParams, error) {
	// with the image name find the image id
	imageID, err := docker.GetImageID(yamlParams.Image)
	if err != nil {
		return docker.ContainerCommandParams{}, err
	}

	// Convert the map of labels to a slice of strings
	var labels []string
	for k, v := range yamlParams.Labels {
		labels = append(labels, fmt.Sprintf("%s=%s", k, v))
	}

	// Ports input parsing
	portMappings, err := docker.ParsePortsSpecs(yamlParams.Ports)
	if err != nil {
		return docker.ContainerCommandParams{}, err
	}

	// Convert the slice of strings to a comma separated string
	network := strings.Join(yamlParams.Network, ",")

	params := docker.ContainerCommandParams{
		ContainerName: yamlParams.Name,
		ImageName:     yamlParams.Image,
		ImageID:       imageID,
		Volumes:       yamlParams.Volumes,
		Environment:   yamlParams.Environment,
		Labels:        labels,
		Network:       network,
		Restart:       yamlParams.Restart,
		PortMappings:  portMappings,
	}

	return params, nil
}
