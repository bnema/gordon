package container

import (
	"context"
	"fmt"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
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
func (h *ImagePushedHandler) Handle(ctx context.Context, event domain.Event) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
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

	routes := h.configSvc.FindRoutesByImage(ctx, fullImageName)
	if len(routes) == 0 {
		log.Debug().Str("image", fullImageName).Msg("no routes configured for pushed image")
		return nil
	}

	// Mark context as internal deploy - the event originated from our own registry,
	// so we can use internal registry auth when pulling images.
	internalCtx := domain.WithInternalDeploy(ctx)

	for _, route := range routes {
		if _, err := h.containerSvc.Deploy(internalCtx, route); err != nil {
			log.WrapErrWithFields(err, "failed to deploy container for route", map[string]any{"domain": route.Domain})
		}
	}

	return nil
}

// CanHandle returns whether this handler can handle the given event type.
func (h *ImagePushedHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventImagePushed
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
func (h *ConfigReloadHandler) Handle(ctx context.Context, event domain.Event) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "ConfigReloadHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Msg("processing configuration reload event")

	// Sync containers first to ensure we have accurate state
	// This removes tracking for containers that were stopped externally
	if err := h.containerSvc.SyncContainers(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to sync containers before reload, proceeding with current state")
	}

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

				if _, err := h.containerSvc.Deploy(domain.WithInternalDeploy(ctx), route); err != nil {
					log.WrapErrWithFields(err, "failed to redeploy container", map[string]any{"domain": route.Domain})
				}
			}
			delete(activeRoutes, route.Domain)
		} else {
			// Route exists in config but no running container (new route or missing container)
			log.Info().
				Str("domain", route.Domain).
				Str("image", route.Image).
				Msg("route missing container, deploying")

			if _, err := h.containerSvc.Deploy(domain.WithInternalDeploy(ctx), route); err != nil {
				log.WrapErrWithFields(err, "failed to deploy container for route", map[string]any{"domain": route.Domain})
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
// It starts containers for configured routes that don't have a running container.
// Running containers are NEVER restarted to ensure 100% uptime.
func (h *ManualReloadHandler) Handle(ctx context.Context, event domain.Event) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "ManualReloadHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	log.Info().Msg("processing manual reload event - starting missing containers")

	// Sync containers first to ensure we have accurate state
	// This removes tracking for containers that were stopped externally
	if err := h.containerSvc.SyncContainers(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to sync containers before reload, proceeding with current state")
	}

	routes := h.configSvc.GetRoutes(ctx)
	startedCount := 0
	skippedCount := 0
	errorCount := 0

	for _, route := range routes {
		if _, exists := h.containerSvc.Get(ctx, route.Domain); exists {
			log.Debug().Str("domain", route.Domain).Msg("container already running, skipping")
			skippedCount++
			continue
		}

		log.Info().
			Str("domain", route.Domain).
			Str("image", route.Image).
			Msg("starting container for route")

		if _, err := h.containerSvc.Deploy(domain.WithInternalDeploy(ctx), route); err != nil {
			log.WrapErrWithFields(err, "failed to start container", map[string]any{"domain": route.Domain})
			errorCount++
			continue
		}

		startedCount++
		log.Info().Str("domain", route.Domain).Msg("container started successfully")
	}

	if errorCount > 0 {
		log.Warn().
			Int("started", startedCount).
			Int("skipped", skippedCount).
			Int("errors", errorCount).
			Msg("manual reload completed with some errors")
		return fmt.Errorf("manual reload completed with %d errors", errorCount)
	}

	log.Info().
		Int("started", startedCount).
		Int("skipped", skippedCount).
		Int("total", len(routes)).
		Msg("manual reload completed successfully")
	return nil
}

// CanHandle returns whether this handler can handle the given event type.
func (h *ManualReloadHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventManualReload
}

// ManualDeployHandler handles manual.deploy events for specific routes.
type ManualDeployHandler struct {
	containerSvc in.ContainerService
	configSvc    in.ConfigService
	ctx          context.Context
}

// NewManualDeployHandler creates a new ManualDeployHandler.
func NewManualDeployHandler(ctx context.Context, containerSvc in.ContainerService, configSvc in.ConfigService) *ManualDeployHandler {
	return &ManualDeployHandler{
		containerSvc: containerSvc,
		configSvc:    configSvc,
		ctx:          ctx,
	}
}

// Handle handles a manual deploy event for a specific route.
func (h *ManualDeployHandler) Handle(ctx context.Context, event domain.Event) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "ManualDeployHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	payload, ok := event.Data.(*domain.ManualDeployPayload)
	if !ok || payload == nil || payload.Domain == "" {
		return fmt.Errorf("invalid manual deploy payload")
	}

	log.Info().Str("domain", payload.Domain).Msg("processing manual deploy event")

	// Find the route in configuration
	routes := h.configSvc.GetRoutes(ctx)
	var targetRoute *domain.Route
	for _, r := range routes {
		if r.Domain == payload.Domain {
			targetRoute = &r
			break
		}
	}

	if targetRoute == nil {
		return fmt.Errorf("route not found for domain: %s", payload.Domain)
	}

	// Manual deploy is an internal trigger, so use internal deploy context.
	if _, err := h.containerSvc.Deploy(domain.WithInternalDeploy(ctx), *targetRoute); err != nil {
		return log.WrapErr(err, "failed to deploy container")
	}

	log.Info().Str("domain", payload.Domain).Msg("manual deploy completed successfully")
	return nil
}

// CanHandle returns whether this handler can handle the given event type.
func (h *ManualDeployHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventManualDeploy
}
