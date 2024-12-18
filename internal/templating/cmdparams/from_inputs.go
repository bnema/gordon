package cmdparams

import (
	"fmt"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
)

// FromInputsToCmdParams transforms the inputs map into a ContainerCommandParams struct
func FromInputsToCmdParams(inputs map[string]string, a *server.App) (docker.ContainerCommandParams, error) {
	volumeSlice := ParseVolumeSlice(inputs["volumes"])
	environmentSlice := ParseEnvironmentSlice(inputs["environment_variables"])

	var portMappings []docker.PortMapping
	var err error

	// Parse port mappings if provided
	if inputs["ports"] != "" {
		portMappings, err = ParsePortMappingsSlice(inputs["ports"])
		if err != nil {
			return docker.ContainerCommandParams{}, err
		}
	}

	params := docker.ContainerCommandParams{
		IsSSL:             inputs["container_protocol"] == "https",
		ContainerName:     inputs["container_name"],
		ServiceName:       inputs["container_subdomain"],
		Domain:            inputs["container_domain"],
		ImageName:         inputs["image_name"],
		ImageID:           inputs["image_id"],
		Restart:           inputs["restart"],
		TraefikEntryPoint: inputs["traefik_entry_point"],
		Volumes:           volumeSlice,
		Environment:       environmentSlice,
		Network:           a.Config.ContainerEngine.Network,
		PortMappings:      portMappings,
	}

	err = CreateTraefikLabels(&params, params.TraefikEntryPoint, a)
	if err != nil {
		return docker.ContainerCommandParams{}, fmt.Errorf("error creating Traefik labels: %w", err)
	}

	return params, nil
}
