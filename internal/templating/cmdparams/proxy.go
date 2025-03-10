package cmdparams

import (
	"github.com/bnema/gordon/internal/interfaces"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/charmbracelet/log"
)

// SetupProxyRoute configures the container for our reverse proxy
func SetupProxyRoute(params *docker.ContainerCommandParams, containerPort string, a interfaces.AppInterface) error {
	// Set the container port to be used by the reverse proxy
	params.ProxyPort = containerPort

	// Create the full domain (service.domain)
	fullDomain := params.ServiceName + "." + params.Domain

	// Add custom labels to identify this container as manageable by Gordon
	params.Labels = []string{
		"gordon.managed=true",
		"gordon.domain=" + fullDomain,
		"gordon.proxy.port=" + containerPort,
		"gordon.proxy.ssl=" + boolToString(params.IsSSL),
	}

	log.Debug("Container proxy configuration set up",
		"domain", fullDomain,
		"proxyPort", containerPort,
		"ssl", params.IsSSL)

	return nil
}

// boolToString converts a boolean to a string "true" or "false"
func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
