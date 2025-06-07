package events

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
	"gordon/internal/config"
	"gordon/internal/container"
	"gordon/pkg/runtime"
)

type ContainerEventHandler struct {
	manager *container.Manager
	config  *config.Config
}

func NewContainerEventHandler(manager *container.Manager, config *config.Config) *ContainerEventHandler {
	return &ContainerEventHandler{
		manager: manager,
		config:  config,
	}
}

func (h *ContainerEventHandler) CanHandle(eventType EventType) bool {
	switch eventType {
	case ImagePushed, ConfigReload, ContainerStop, ContainerStart:
		return true
	default:
		return false
	}
}

func (h *ContainerEventHandler) Handle(event Event) error {
	switch event.Type {
	case ImagePushed:
		return h.handleImagePushed(event)
	case ConfigReload:
		return h.handleConfigReload(event)
	case ContainerStop:
		return h.handleContainerStop(event)
	case ContainerStart:
		return h.handleContainerStart(event)
	default:
		return fmt.Errorf("unsupported event type: %s", event.Type)
	}
}

func (h *ContainerEventHandler) handleImagePushed(event Event) error {
	imageName := event.ImageName
	tag := event.Tag
	
	if imageName == "" {
		return fmt.Errorf("image name is required for ImagePushed event")
	}
	
	if tag == "" {
		tag = "latest"
	}
	
	fullImageName := fmt.Sprintf("%s:%s", imageName, tag)
	
	log.Info().
		Str("image", fullImageName).
		Str("event_id", event.ID).
		Msg("Processing image push event")
	
	routes := h.findRoutesForImage(fullImageName)
	if len(routes) == 0 {
		log.Debug().
			Str("image", fullImageName).
			Msg("No routes configured for pushed image")
		return nil
	}
	
	for _, route := range routes {
		if err := h.deployContainerForRoute(route, fullImageName); err != nil {
			log.Error().
				Err(err).
				Str("route", route).
				Str("image", fullImageName).
				Msg("Failed to deploy container for route")
		}
	}
	
	return nil
}

func (h *ContainerEventHandler) handleConfigReload(event Event) error {
	log.Info().
		Str("event_id", event.ID).
		Msg("Processing configuration reload event")
	
	ctx := context.Background()
	
	// Get current containers managed by Gordon
	currentContainers := h.manager.ListContainers()
	
	// Track which routes are currently active
	activeRoutes := make(map[string]*runtime.Container)
	for _, container := range currentContainers {
		if route, exists := container.Labels["gordon.route"]; exists {
			activeRoutes[route] = container
		}
	}
	
	// Process each configured route
	for route, imageName := range h.config.Routes {
		if container, exists := activeRoutes[route]; exists {
			// Route exists - check if image has changed
			currentImage := container.Labels["gordon.image"]
			if currentImage != imageName {
				log.Info().
					Str("route", route).
					Str("old_image", currentImage).
					Str("new_image", imageName).
					Msg("Image changed for route, redeploying")
				
				if err := h.deployContainerForRoute(route, imageName); err != nil {
					log.Error().
						Err(err).
						Str("route", route).
						Msg("Failed to redeploy container for route")
				}
			}
			// Remove from active routes so we know it's been processed
			delete(activeRoutes, route)
		} else {
			// New route - deploy container
			log.Info().
				Str("route", route).
				Str("image", imageName).
				Msg("New route detected, deploying container")
			
			if err := h.deployContainerForRoute(route, imageName); err != nil {
				log.Error().
					Err(err).
					Str("route", route).
					Msg("Failed to deploy container for new route")
			}
		}
	}
	
	// Stop containers for routes that are no longer configured
	for route, container := range activeRoutes {
		log.Info().
			Str("route", route).
			Str("container_id", container.ID).
			Msg("Route no longer configured, stopping container")
		
		if err := h.manager.StopContainer(ctx, container.ID); err != nil {
			log.Error().
				Err(err).
				Str("container_id", container.ID).
				Msg("Failed to stop container for removed route")
		}
		
		if err := h.manager.RemoveContainer(ctx, container.ID, true); err != nil {
			log.Error().
				Err(err).
				Str("container_id", container.ID).
				Msg("Failed to remove container for removed route")
		}
	}
	
	return nil
}

func (h *ContainerEventHandler) handleContainerStop(event Event) error {
	containerID := event.ContainerID
	if containerID == "" {
		return fmt.Errorf("container ID is required for ContainerStop event")
	}
	
	log.Info().
		Str("container_id", containerID).
		Str("event_id", event.ID).
		Msg("Processing container stop event")
	
	ctx := context.Background()
	if err := h.manager.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", containerID, err)
	}
	
	return nil
}

func (h *ContainerEventHandler) handleContainerStart(event Event) error {
	containerID := event.ContainerID
	if containerID == "" {
		return fmt.Errorf("container ID is required for ContainerStart event")
	}
	
	log.Info().
		Str("container_id", containerID).
		Str("event_id", event.ID).
		Msg("Processing container start event")
	
	// StartContainer method doesn't exist on Manager - containers are started via DeployContainer
	// For manual start, we'd need to add this method or use the runtime directly
	return fmt.Errorf("container start not implemented - use automatic deployment instead")
}

func (h *ContainerEventHandler) findRoutesForImage(imageName string) []string {
	var routes []string
	
	for route, configuredImage := range h.config.Routes {
		if strings.EqualFold(configuredImage, imageName) {
			routes = append(routes, route)
		}
	}
	
	return routes
}

func (h *ContainerEventHandler) deployContainerForRoute(route, imageName string) error {
	ctx := context.Background()
	
	containers := h.manager.ListContainers()
	
	containerName := fmt.Sprintf("gordon-%s", strings.ReplaceAll(route, ".", "-"))
	
	// Stop and remove existing container if it exists
	for _, container := range containers {
		if container.Name == containerName {
			log.Info().
				Str("container_name", containerName).
				Str("old_container_id", container.ID).
				Msg("Stopping existing container for route")
			
			if err := h.manager.StopContainer(ctx, container.ID); err != nil {
				log.Warn().
					Err(err).
					Str("container_id", container.ID).
					Msg("Failed to stop existing container")
			}
			
			if err := h.manager.RemoveContainer(ctx, container.ID, true); err != nil {
				log.Warn().
					Err(err).
					Str("container_id", container.ID).
					Msg("Failed to remove existing container")
			}
		}
	}
	
	// Use DeployContainer which handles creation and starting
	routeConfig := config.Route{
		Domain: route,
		Image:  imageName,
		HTTPS:  true,
	}
	
	container, err := h.manager.DeployContainer(ctx, routeConfig)
	if err != nil {
		return fmt.Errorf("failed to deploy container for route %s: %w", route, err)
	}
	
	log.Info().
		Str("route", route).
		Str("image", imageName).
		Str("container_id", container.ID).
		Str("container_name", containerName).
		Msg("Successfully deployed container for route")
	
	return nil
}

type AutoRouteHandler struct {
	config  *config.Config
	manager *container.Manager
}

func NewAutoRouteHandler(cfg *config.Config, manager *container.Manager) *AutoRouteHandler {
	return &AutoRouteHandler{
		config:  cfg,
		manager: manager,
	}
}

func (h *AutoRouteHandler) CanHandle(eventType EventType) bool {
	return eventType == ImagePushed
}

func (h *AutoRouteHandler) Handle(event Event) error {
	if !h.config.AutoRoute.Enabled {
		return nil
	}

	if event.Type != ImagePushed {
		return nil
	}

	payload, ok := event.Data.(ImagePushedPayload)
	if !ok {
		return fmt.Errorf("invalid payload type for ImagePushed event")
	}

	imageName := payload.Name
	reference := payload.Reference
	fullImageName := fmt.Sprintf("%s:%s", imageName, reference)
	
	// Build the full registry image path for deployment
	registryImageName := fullImageName
	if h.config.Server.RegistryDomain != "" {
		registryImageName = fmt.Sprintf("%s/%s", h.config.Server.RegistryDomain, fullImageName)
	}

	// Try to extract domain from the image name part (before the colon)
	domain, isDomain := config.ExtractDomainFromImageName(imageName)
	if !isDomain {
		log.Debug().
			Str("image", imageName).
			Msg("Image name does not contain a valid domain, skipping auto-route creation")
		return nil
	}

	// Check if route already exists
	if _, exists := h.config.Routes[domain]; exists {
		log.Debug().
			Str("domain", domain).
			Str("existing_image", h.config.Routes[domain]).
			Str("new_image", fullImageName).
			Msg("Route already exists for domain, skipping auto-route creation")
		return nil
	}

	// Add the route to config with the full registry image path
	if err := h.config.AddRoute(domain, registryImageName); err != nil {
		return fmt.Errorf("failed to add auto-route for domain %s: %w", domain, err)
	}

	log.Info().
		Str("domain", domain).
		Str("image", registryImageName).
		Msg("Successfully auto-created route - deployment will be handled by config reload event")

	return nil
}

