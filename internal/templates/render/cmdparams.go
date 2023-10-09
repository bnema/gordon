package render

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/pkg/docker"
)

// FromInputsToCmdParams transforms the inputs map into a ContainerCommandParams struct
func FromInputsToCmdParams(inputs map[string]string) (docker.ContainerCommandParams, error) {

	volumeStr := inputs["volumes"]
	volumeSlice := strings.Split(volumeStr, ",")

	// Retreive the content of the textarea environment_variables and create a slice of strings
	environmentStr := inputs["environment_variables"]
	environmentSlice := strings.Split(environmentStr, "\n")

	// Ports input parsing
	portMappingsStr := inputs["ports"]
	portMappingsSliceRaw := strings.Split(portMappingsStr, ",")

	// Trim whitespace from each port mapping string
	portMappingsSlice := make([]string, len(portMappingsSliceRaw))
	for i, spec := range portMappingsSliceRaw {
		portMappingsSlice[i] = strings.TrimSpace(spec)
	}

	portMappings, err := docker.ParsePortsSpecs(portMappingsSlice)
	if err != nil {
		return docker.ContainerCommandParams{}, err
	}

	// Get the exposed port for traefik labels
	exposedPort := portMappings[0].ExposedPort

	params := docker.ContainerCommandParams{
		IsSSL:         inputs["container_protocol"] == "https",
		ContainerName: inputs["container_name"],
		ServiceName:   inputs["container_subdomain"],
		Domain:        inputs["container_domain"],
		ImageName:     inputs["image_name"],
		ImageID:       inputs["image_id"],
		Restart:       inputs["restart"],
		Volumes:       volumeSlice,
		Environment:   environmentSlice,
		Network:       ("traefik"),
		PortMappings:  portMappings,
	}

	entryPoint := "web"
	if params.IsSSL {
		entryPoint = "websecure"
	}

	baseRouter := "traefik.http.routers." + params.ContainerName
	baseService := "traefik.http.services." + params.ContainerName

	params.Labels = []string{
		"traefik.enable=true",
		fmt.Sprintf("%s.rule=Host(`%s.%s`)", baseRouter, params.ServiceName, params.Domain),
		fmt.Sprintf("%s.entrypoints=%s", baseRouter, entryPoint),
		fmt.Sprintf("%s.loadbalancer.server.port=%s", baseService, exposedPort),
	}

	if params.IsSSL {
		params.Labels = append(params.Labels, fmt.Sprintf("%s.tls.certresolver=letsencrypt", baseRouter))
	}

	return params, nil
}

type YAMLContainerParams struct {
	Name        string            `yaml:"Name"`
	Image       string            `yaml:"Image"`
	Hostname    string            `yaml:"Hostname"`
	Ports       []string          `yaml:"Ports"`
	Volumes     []string          `yaml:"Volumes"`
	Environment []string          `yaml:"Environment"`
	Labels      map[string]string `yaml:"Labels"`
	// Network is a slice of strings in the textarea
	Network []string `yaml:"Network"`
	Restart string   `yaml:"Restart"`
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
	}

	return params, nil
}
