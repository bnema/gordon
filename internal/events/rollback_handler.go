package events

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
	"gordon/internal/config"
	"gordon/internal/container"
	"gordon/pkg/manifest"
)

// VersionHandler handles version deployments based on manifest annotations
type VersionHandler struct {
	manager *container.Manager
	config  *config.Config
}

// NewVersionHandler creates a new version handler
func NewVersionHandler(manager *container.Manager, config *config.Config) *VersionHandler {
	return &VersionHandler{
		manager: manager,
		config:  config,
	}
}

// CanHandle returns true for ImagePushed events
func (h *VersionHandler) CanHandle(eventType EventType) bool {
	return eventType == ImagePushed
}

// Handle processes ImagePushed events and performs version deployments if necessary
func (h *VersionHandler) Handle(event Event) error {
	if event.Type != ImagePushed {
		return nil
	}

	payload, ok := event.Data.(ImagePushedPayload)
	if !ok {
		return fmt.Errorf("invalid payload type for ImagePushed event")
	}

	// Skip if no annotations
	if len(payload.Annotations) == 0 {
		return nil
	}

	imageName := payload.Name
	reference := payload.Reference
	annotations := payload.Annotations

	log.Debug().
		Str("image", imageName).
		Str("reference", reference).
		Interface("annotations", annotations).
		Msg("Processing annotations for version deployment")

	// Check if this is a versioned deployment
	if manifest.IsVersionedDeployment(annotations) {
		return h.handleVersionedDeployment(imageName, reference, annotations)
	}

	return nil
}

// handleVersionedDeployment handles a versioned deployment
func (h *VersionHandler) handleVersionedDeployment(imageName, reference string, annotations map[string]string) error {
	version := manifest.GetDeploymentVersion(annotations)
	if version == "" {
		return nil
	}

	log.Info().
		Str("image", imageName).
		Str("reference", reference).
		Str("version", version).
		Msg("Deploying version via manifest annotation")

	// Deploy the versioned image - treat this as a deployment to the specified version
	return h.performVersionedDeployment(imageName, reference, version, annotations)
}

// performVersionedDeployment deploys a specific version based on manifest annotations
func (h *VersionHandler) performVersionedDeployment(imageName, reference, version string, annotations map[string]string) error {
	ctx := context.Background()
	
	// Find routes that use this image  
	routes := h.findRoutesForImage(fmt.Sprintf("%s:%s", imageName, reference))
	if len(routes) == 0 {
		log.Debug().
			Str("image", imageName).
			Str("reference", reference).
			Msg("No routes configured for versioned deployment image")
		return nil
	}

	var errors []error
	for _, route := range routes {
		if err := h.deployVersion(ctx, route, imageName, version, annotations); err != nil {
			log.Error().
				Err(err).
				Str("route", route).
				Str("version", version).
				Msg("Failed to deploy version to route")
			errors = append(errors, fmt.Errorf("version deployment failed for route %s: %w", route, err))
		} else {
			log.Info().
				Str("route", route).
				Str("version", version).
				Msg("Version deployment completed successfully")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("version deployment completed with %d errors: %v", len(errors), errors)
	}

	return nil
}


// findRoutesForImage finds all routes that match the given image
func (h *VersionHandler) findRoutesForImage(imageName string) []string {
	var routes []string
	
	for route, configuredImage := range h.config.Routes {
		if strings.EqualFold(configuredImage, imageName) {
			routes = append(routes, route)
		}
	}
	
	return routes
}

// deployVersion deploys a specific version to a route
func (h *VersionHandler) deployVersion(ctx context.Context, route, imageName, version string, annotations map[string]string) error {
	// Construct the versioned image name
	var versionedImage string
	
	if strings.Contains(version, ":") {
		// Full image reference provided
		versionedImage = version
	} else {
		// Just a version/tag provided, construct full image reference
		versionedImage = fmt.Sprintf("%s:%s", imageName, version)
	}

	log.Info().
		Str("route", route).
		Str("current_image", h.config.Routes[route]).
		Str("versioned_image", versionedImage).
		Msg("Performing version deployment")

	// Update the route configuration to the versioned image
	if err := h.config.UpdateRoute(route, versionedImage); err != nil {
		return fmt.Errorf("failed to update route configuration for version deployment: %w", err)
	}

	// Deploy the versioned image
	routeConfig := config.Route{
		Domain: route,
		Image:  versionedImage,
		HTTPS:  true,
	}

	_, err := h.manager.DeployContainer(ctx, routeConfig)
	if err != nil {
		// Attempt to restore original configuration
		if originalImage := h.config.Routes[route]; originalImage != versionedImage {
			log.Warn().
				Str("route", route).
				Str("original_image", originalImage).
				Msg("Version deployment failed, attempting to restore original configuration")
			_ = h.config.UpdateRoute(route, originalImage)
		}
		return fmt.Errorf("failed to deploy versioned container: %w", err)
	}

	// Log successful deployment
	log.Info().
		Str("route", route).
		Str("versioned_image", versionedImage).
		Interface("version_annotations", annotations).
		Msg("Version deployment completed successfully")

	return nil
}