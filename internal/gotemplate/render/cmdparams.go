package render

import (
	"fmt"

	"github.com/bnema/gordon/pkg/utils/docker"
)

// FromInputsToCmdParams transforms a map to a ContainerCommandParams struct
func FromInputsToCmdParams(inputs map[string]string) (docker.ContainerCommandParams, error) {
	fmt.Println("FromInputsToCmdParams:", inputs)
	// Populate the ContainerCommandParams struct directly from the map
	params := docker.ContainerCommandParams{
		Https:         inputs["container_protocol"] == "https",
		ContainerName: inputs["container_name"],
		ContainerHost: inputs["container_subdomain"],
		Domain:        inputs["container_domain"],
		ImageName:     inputs["image_name"],
		ImageID:       inputs["image_id"],
		Ports:         inputs["ports"],
		Restart:       inputs["restart"],
		Data:          inputs["volumes"],
		EnvVar:        inputs["environment_variables"],
		Network:       "traefik",
	}

	entryPoint := "web"
	if params.Https {
		entryPoint = "websecure"
	}

	baseRouter := "traefik.http.routers." + params.ContainerName + "-router"
	baseService := "traefik.http.services." + params.ContainerName + "-service"

	params.TraefikLabels = []string{
		"traefik.enable=true",
		fmt.Sprintf("%s.rule=Host(`%s.%s`)", baseRouter, params.ContainerHost, params.Domain),
		fmt.Sprintf("%s.entrypoints=%s", baseRouter, entryPoint),
		fmt.Sprintf("%s.loadbalancer.server.port=%s", baseService, params.Ports),
	}

	if params.Https {
		params.TraefikLabels = append(params.TraefikLabels, fmt.Sprintf("%s.tls.certresolver=letsencrypt", baseRouter))
	}

	return params, nil
}
