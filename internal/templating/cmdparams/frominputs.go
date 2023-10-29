package cmdparams

import (
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
)

// FromInputsToCmdParams transforms the inputs map into a ContainerCommandParams struct
func FromInputsToCmdParams(inputs map[string]string, a *server.App) (docker.ContainerCommandParams, error) {
	volumeSlice := ParseVolumeSlice(inputs["volumes"])
	environmentSlice := ParseEnvironmentSlice(inputs["environment_variables"])
	portMappings, err := ParsePortMappingsSlice(inputs["ports"])
	if err != nil {
		return docker.ContainerCommandParams{}, err
	}

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
		Network:       a.Config.ContainerEngine.Network,
		PortMappings:  portMappings,
	}

	CreateTraefikLabels(&params)

	return params, nil
}
