package cmdparams

import (
	"strings"

	"github.com/bnema/gordon/pkg/docker"
)

// ParseVolumeSlice parses the volumes string into a slice.
func ParseVolumeSlice(volumeStr string) []string {
	volumeSlice := strings.Split(volumeStr, ",")
	if len(volumeSlice) == 1 && volumeSlice[0] == "" {
		return []string{}
	}
	return volumeSlice
}

// ParseEnvironmentSlice parses the environment variables string into a slice.
func ParseEnvironmentSlice(environmentStr string) []string {
	environmentSlice := strings.Split(environmentStr, "\n")
	if len(environmentSlice) == 1 && environmentSlice[0] == "" {
		return []string{}
	}
	return environmentSlice
}

// ParsePortMappingsSlice parses the port mappings string into a slice and a struct.
func ParsePortMappingsSlice(portMappingsStr string) ([]docker.PortMapping, error) {
	portMappingsSliceRaw := strings.Split(portMappingsStr, ",")
	portMappingsSlice := make([]string, len(portMappingsSliceRaw))
	for i, spec := range portMappingsSliceRaw {
		portMappingsSlice[i] = strings.TrimSpace(spec)
	}
	return docker.ParsePortsSpecs(portMappingsSlice)
}
