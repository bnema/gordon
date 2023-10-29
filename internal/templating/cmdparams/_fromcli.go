package cmdparams

import (
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/docker"
)

// FromYAMLStructToCmdParams converts a YAMLContainerParams struct to a ContainerCommandParams struct
func FromPayloadStructToCmdParams(PushPayload *common.PushPayload) (docker.ContainerCommandParams, error) {

	params := docker.ContainerCommandParams{
		ContainerName: yamlParams.Name,
		ImageName:     yamlParams.Image,
		ImageID:       imageID,
		Volumes:       yamlParams.Volumes,
		Environment:   yamlParams.Environment,
		Labels:        labels,
		Network:       network,
		Restart:       yamlParams.Restart,
		PortMappings:  portMappings,
	}

	return params, nil
}
