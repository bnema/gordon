package cmdparams

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
)

// FromYAMLStructToCmdParams converts a YAMLContainerParams struct to a ContainerCommandParams struct
func FromPayloadStructToCmdParams(ppl *common.DeployPayload, a *server.App, imageID string) (docker.ContainerCommandParams, error) {

	// volume empty for now
	volumeSlice := ParseVolumeSlice("")

	environmentSlice := ParseEnvironmentSlice("")

	// from ppl.TargetDomain, (eg. "https://test.example.com") get container_name (eg. "test"), get container_subdomain (eg. "test"), get container_domain (eg. "example.com") and get container_protocol (eg. "https")
	// Parse TargetDomain to get the various components
	parsedURL, err := url.Parse(ppl.TargetDomain)
	if err != nil {
		return docker.ContainerCommandParams{}, err
	}

	// Extract the subdomain and domain from the host
	hostParts := strings.Split(parsedURL.Host, ".")
	if len(hostParts) < 2 {
		return docker.ContainerCommandParams{}, fmt.Errorf("invalid domain: %s", parsedURL.Host)
	}

	// Protocol (http/https), Container name, Container subdomain, and Container domain
	protocol := parsedURL.Scheme
	containerSubdomain := hostParts[0]
	containerDomain := strings.Join(hostParts[1:], ".")

	params := docker.ContainerCommandParams{
		IsSSL:         protocol == "https",
		ContainerName: containerSubdomain,
		ServiceName:   containerSubdomain,
		Domain:        containerDomain,
		ImageName:     ppl.ImageName,
		ImageID:       imageID,
		Restart:       "always",
		Volumes:       volumeSlice,
		Environment:   environmentSlice,
		Network:       a.Config.ContainerEngine.Network,
		ProxyPort:     ppl.Port,
	}

	err = SetupProxyRoute(&params, params.ProxyPort, a)
	if err != nil {
		return docker.ContainerCommandParams{}, fmt.Errorf("error setting up proxy route: %w", err)
	}

	return params, nil
}
