package container

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/in"
	"gordon/internal/domain"
)

// ImagePushedHandler handles image.pushed events.
type ImagePushedHandler struct {
	containerSvc in.ContainerService
	configSvc    in.ConfigService
	ctx          context.Context
}

// NewImagePushedHandler creates a new ImagePushedHandler.
func NewImagePushedHandler(ctx context.Context, containerSvc in.ContainerService, configSvc in.ConfigService) *ImagePushedHandler {
	return &ImagePushedHandler{
		containerSvc: containerSvc,
		configSvc:    configSvc,
		ctx:          ctx,
	}
}

// Handle handles an event.
func (h *ImagePushedHandler) Handle(event domain.Event) error {
	ctx := zerowrap.CtxWithFields(h.ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "ImagePushedHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	if event.ImageName == "" {
		return domain.ErrInvalidImageFormat
	}

	tag := event.Tag
	if tag == "" {
		tag = "latest"
	}

	fullImageName := fmt.Sprintf("%s:%s", event.ImageName, tag)

	log.Info().Str("image", fullImageName).Msg("processing image push event")

	routes := h.findRoutesForImage(fullImageName)
	if len(routes) == 0 {
		log.Debug().Str("image", fullImageName).Msg("no routes configured for pushed image")
		return nil
	}

	for _, route := range routes {
		if _, err := h.containerSvc.Deploy(ctx, route); err != nil {
			log.WrapErrWithFields(err, "failed to deploy container for route", map[string]any{"domain": route.Domain})
		}
	}

	return nil
}

// CanHandle returns whether this handler can handle the given event type.
func (h *ImagePushedHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventImagePushed
}

func (h *ImagePushedHandler) findRoutesForImage(imageName string) []domain.Route {
	var routes []domain.Route

	configuredRoutes := h.configSvc.GetRoutes(h.ctx)
	for _, route := range configuredRoutes {
		if strings.EqualFold(route.Image, imageName) {
			routes = append(routes, route)
		}
	}

	return routes
}

// ConfigReloadHandler handles config.reload events.
type ConfigReloadHandler struct {
	containerSvc in.ContainerService
	configSvc    in.ConfigService
	ctx          context.Context
}

// NewConfigReloadHandler creates a new ConfigReloadHandler.
func NewConfigReloadHandler(ctx context.Context, containerSvc in.ContainerService, configSvc in.ConfigService) *ConfigReloadHandler {
	return &ConfigReloadHandler{
		containerSvc: containerSvc,
		configSvc:    configSvc,
		ctx:          ctx,
	}
}

// Handle handles an event.
func (h *ConfigReloadHandler) Handle(event domain.Event) error {
	ctx := zerowrap.CtxWithFields(h.ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "ConfigReloadHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Msg("processing configuration reload event")

	currentContainers := h.containerSvc.List(ctx)

	activeRoutes := make(map[string]*domain.Container)
	for _, container := range currentContainers {
		if route, exists := container.Labels["gordon.route"]; exists {
			activeRoutes[route] = container
		}
	}

	routes := h.configSvc.GetRoutes(ctx)
	for _, route := range routes {
		if container, exists := activeRoutes[route.Domain]; exists {
			currentImage := container.Labels["gordon.image"]
			if currentImage != route.Image {
				log.Info().
					Str("domain", route.Domain).
					Str("old_image", currentImage).
					Str("new_image", route.Image).
					Msg("image changed for route, redeploying")

				if _, err := h.containerSvc.Deploy(ctx, route); err != nil {
					log.WrapErrWithFields(err, "failed to redeploy container", map[string]any{"domain": route.Domain})
				}
			}
			delete(activeRoutes, route.Domain)
		} else {
			log.Info().
				Str("domain", route.Domain).
				Str("image", route.Image).
				Msg("new route detected, deploying container")

			if _, err := h.containerSvc.Deploy(ctx, route); err != nil {
				log.WrapErrWithFields(err, "failed to deploy container for new route", map[string]any{"domain": route.Domain})
			}
		}
	}

	for route, container := range activeRoutes {
		log.Info().
			Str("domain", route).
			Str(zerowrap.FieldEntityID, container.ID).
			Msg("route no longer configured, stopping container")

		if err := h.containerSvc.Stop(ctx, container.ID); err != nil {
			log.WrapErrWithFields(err, "failed to stop container", map[string]any{zerowrap.FieldEntityID: container.ID})
		}

		if err := h.containerSvc.Remove(ctx, container.ID, true); err != nil {
			log.WrapErrWithFields(err, "failed to remove container", map[string]any{zerowrap.FieldEntityID: container.ID})
		}
	}

	return nil
}

// CanHandle returns whether this handler can handle the given event type.
func (h *ConfigReloadHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventConfigReload
}

// ManualReloadHandler handles manual.reload events.
type ManualReloadHandler struct {
	containerSvc in.ContainerService
	configSvc    in.ConfigService
	ctx          context.Context
}

// NewManualReloadHandler creates a new ManualReloadHandler.
func NewManualReloadHandler(ctx context.Context, containerSvc in.ContainerService, configSvc in.ConfigService) *ManualReloadHandler {
	return &ManualReloadHandler{
		containerSvc: containerSvc,
		configSvc:    configSvc,
		ctx:          ctx,
	}
}

// Handle handles an event.
func (h *ManualReloadHandler) Handle(event domain.Event) error {
	ctx := zerowrap.CtxWithFields(h.ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "ManualReloadHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Msg("processing manual reload event - redeploying all containers")

	routes := h.configSvc.GetRoutes(ctx)
	redeployedCount := 0
	errorCount := 0

	for _, route := range routes {
		if _, exists := h.containerSvc.Get(ctx, route.Domain); !exists {
			log.Debug().Str("domain", route.Domain).Msg("no container found for route, skipping")
			continue
		}

		log.Info().
			Str("domain", route.Domain).
			Str("image", route.Image).
			Msg("redeploying container with updated environment")

		if _, err := h.containerSvc.Deploy(ctx, route); err != nil {
			log.WrapErrWithFields(err, "failed to redeploy container", map[string]any{"domain": route.Domain})
			errorCount++
			continue
		}

		redeployedCount++
		log.Info().Str("domain", route.Domain).Msg("container redeployed successfully")
	}

	if errorCount > 0 {
		log.Warn().
			Int("redeployed", redeployedCount).
			Int("errors", errorCount).
			Msg("manual reload completed with some errors")
		return fmt.Errorf("manual reload completed with %d errors", errorCount)
	}

	log.Info().
		Int("redeployed", redeployedCount).
		Int("total", len(routes)).
		Msg("manual reload completed successfully")
	return nil
}

// CanHandle returns whether this handler can handle the given event type.
func (h *ManualReloadHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventManualReload
}
