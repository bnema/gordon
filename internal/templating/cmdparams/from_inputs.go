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

	// Retrieve proxy port from the correct input key
	proxyPortValue := inputs["container_port"] // Changed from proxy_port

	params := docker.ContainerCommandParams{
		IsSSL:         inputs["container_protocol"] == "https",
		ContainerName: inputs["container_name"],
		ServiceName:   inputs["container_subdomain"],
		Domain:        inputs["container_domain"],
		ImageName:     inputs["image_name"],
		ImageID:       inputs["image_id"],
		Restart:       inputs["restart"],
		ProxyPort:     proxyPortValue, // Assign the retrieved value
		Volumes:       volumeSlice,
		Environment:   environmentSlice,
		Network:       a.Config.ContainerEngine.Network,
		PortMappings:  portMappings,
		// Labels will be populated below
	}

	// Initialize Labels slice if nil
	if params.Labels == nil {
		params.Labels = []string{}
	}

	// Add standard labels and the proxy port label
	params.Labels = append(params.Labels,
		fmt.Sprintf("gordon.managed=true"),
		fmt.Sprintf("gordon.domain=%s.%s", params.ServiceName, params.Domain),
		fmt.Sprintf("gordon.proxy.ssl=%t", params.IsSSL),
		fmt.Sprintf("gordon.proxy.port=%s", proxyPortValue), // Add the proxy port label
	)

	// SetupProxyRoute might need adjustment if it relies on params.ProxyPort directly
	// Assuming SetupProxyRoute uses the passed proxyPortValue or reads labels
	err = SetupProxyRoute(&params, proxyPortValue, a) // Pass the correct port value
	if err != nil {
		return docker.ContainerCommandParams{}, fmt.Errorf("error setting up proxy route: %w", err)
	}

	return params, nil
}
