package cmdparams

import (
	"fmt"

	"github.com/bnema/gordon/pkg/docker"
)

// CreateTraefikLabels creates Traefik labels based on the provided parameters.
func CreateTraefikLabels(params *docker.ContainerCommandParams) {
	entryPoint := "web"
	if params.IsSSL {
		entryPoint = "websecure"
	}
	baseRouter := "traefik.http.routers." + params.ContainerName
	baseService := "traefik.http.services." + params.ContainerName
	exposedPort := params.PortMappings[0].ContainerPort

	params.Labels = []string{
		"traefik.enable=true",
		fmt.Sprintf("%s.rule=Host(`%s.%s`)", baseRouter, params.ServiceName, params.Domain),
		fmt.Sprintf("%s.entrypoints=%s", baseRouter, entryPoint),
		fmt.Sprintf("%s.loadbalancer.server.port=%s", baseService, exposedPort),
	}

	if params.IsSSL {
		params.Labels = append(params.Labels, fmt.Sprintf("%s.tls.certresolver=letsencrypt", baseRouter))
	}
}
