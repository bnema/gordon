package proxy

import (
	"context"
	"fmt"
	"strconv"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// TargetMetadata holds resolved backend connection metadata for a container image.
type TargetMetadata struct {
	Port     int    // The port to proxy traffic to
	Protocol string // "" for HTTP/1.1, "h2c" for cleartext HTTP/2
}

// resolveTargetMetadata determines the port and protocol for an image by reading
// image labels. It checks gordon.proxy.port first, then the deprecated gordon.port
// alias, then falls back to the first exposed port. Protocol is read from
// gordon.proxy.protocol (only "h2c" is recognized; unknown values are ignored).
func (s *Service) resolveTargetMetadata(ctx context.Context, imageRef string) (TargetMetadata, error) {
	log := zerowrap.FromCtx(ctx)
	log.Debug().Str("image_ref", imageRef).Msg("resolving target metadata for image")

	labels, err := s.runtime.GetImageLabels(ctx, imageRef)
	if err != nil {
		log.Debug().Err(err).Msg("failed to get image labels, falling back to exposed ports")
		labels = nil
	}

	port := 0
	if labels != nil {
		for _, key := range []string{domain.LabelProxyPort, domain.LabelPort} {
			if portStr, ok := labels[key]; ok && portStr != "" {
				p, convErr := strconv.Atoi(portStr)
				if convErr == nil && p > 0 && p <= 65535 {
					log.Debug().Int("port", p).Str("label", key).Msg("using proxy port from image label")
					port = p
					break
				}
				log.Warn().Str("label", key).Str("port_value", portStr).Msg("invalid port label value")
			}
		}
	}

	if port == 0 {
		exposedPorts, err := s.runtime.GetImageExposedPorts(ctx, imageRef)
		if err != nil {
			return TargetMetadata{}, err
		}
		if len(exposedPorts) == 0 {
			return TargetMetadata{}, fmt.Errorf("no exposed ports found for image %s", imageRef)
		}
		port = exposedPorts[0]
		log.Debug().Int("port", port).Msg("using first exposed port")
	}

	protocol := ""
	if labels != nil {
		proto := labels[domain.LabelProxyProtocol]
		switch proto {
		case "h2c":
			protocol = "h2c"
			log.Debug().Str("protocol", proto).Msg("container opts into h2c")
		case "":
		default:
			log.Warn().Str("protocol", proto).Msg("unknown proxy protocol label value, ignoring")
		}
	}

	return TargetMetadata{Port: port, Protocol: protocol}, nil
}
