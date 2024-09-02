package cmdparams

import (
	"fmt"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
)

// CreateTraefikLabels creates Traefik labels based on the provided parameters.
func CreateTraefikLabels(params *docker.ContainerCommandParams, portEntryPoint string, a *server.App) error {
	entryPoint := a.Config.Traefik.EntryPoint
	if params.IsSSL {
		entryPoint = a.Config.Traefik.SecureEntryPoint
	}

	baseRouter := "traefik.http.routers." + params.ContainerName
	baseService := "traefik.http.services." + params.ContainerName

	params.Labels = []string{
		"traefik.enable=true",
		fmt.Sprintf("%s.rule=Host(`%s.%s`)", baseRouter, params.ServiceName, params.Domain),
		fmt.Sprintf("%s.entrypoints=%s", baseRouter, entryPoint),
		fmt.Sprintf("%s.loadbalancer.server.port=%s", baseService, portEntryPoint),
	}

	resolver := a.Config.Traefik.Resolver
	if resolver == "" {
		fmt.Errorf("resolver not set in config")
	}

	if params.IsSSL {
		params.Labels = append(params.Labels, fmt.Sprintf("%s.tls.certresolver=%s", baseRouter, resolver))
	}

	return nil
}
