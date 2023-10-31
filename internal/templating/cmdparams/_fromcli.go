package cmdparams

import (
	"io"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/pkg/docker"
)

type PushPayload struct {
	Ports        string `json:"ports"`
	TargetDomain string `json:"targetdomain"`
	ImageName    string `json:"imagename"`
	Data         io.ReadCloser
}

// FromYAMLStructToCmdParams converts a YAMLContainerParams struct to a ContainerCommandParams struct
func FromPayloadStructToCmdParams(ppl *common.PushPayload) (docker.ContainerCommandParams, error) {

	params := docker.ContainerCommandParams{
		IsSSL:         ppl.IsSSL,
		ContainerName: ppl.ContainerName,
		ServiceName:   ppl.ServiceName,
		Domain:        ppl.Domain,
		ImageName:     ppl.ImageName,
		ImageID:       ppl.ImageID,
		Restart:       ppl.Restart,
		Volumes:       ppl.Volumes,
		Environment:   ppl.Environment,
		Network:       ppl.Network,
		PortMappings:  ppl.PortMappings,
	}

	return params, nil
}
