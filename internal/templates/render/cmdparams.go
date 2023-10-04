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

	fmt.Println("environmentSlice", environmentSlice)

	params := docker.ContainerCommandParams{
		IsSSL:         inputs["container_protocol"] == "https",
		ContainerName: inputs["container_name"],
		ServiceName:   inputs["container_subdomain"],
		Domain:        inputs["container_domain"],
		ImageName:     inputs["image_name"],
		ImageID:       inputs["image_id"],
		Ports:         inputs["ports"],
		Restart:       inputs["restart"],
		Volumes:       volumeSlice,
		Environment:   environmentSlice,
		Network:       ("traefik"),
	}

	// Retain the port number for the container host for the load balancer
	exposedPort := strings.Split(params.Ports, ":")[1]

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
