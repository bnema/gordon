package auto

import (
	"context"
	"path/filepath"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// AutoConfigProvider exposes unified auto config to the dispatcher.
type AutoConfigProvider interface {
	IsAutoEnabled() bool
	IsPreviewEnabled() bool
	GetPreviewTagPatterns() []string
	GetPreviewConfig() domain.PreviewConfig
	GetAllowedDomains() []string
}

// ImagePushDispatcher classifies image push events and delegates
// to either the auto-route handler or the auto-preview handler.
type ImagePushDispatcher struct {
	config         AutoConfigProvider
	routeHandler   out.EventHandler
	previewHandler out.EventHandler
}

func NewImagePushDispatcher(config AutoConfigProvider, routeHandler, previewHandler out.EventHandler) *ImagePushDispatcher {
	return &ImagePushDispatcher{
		config:         config,
		routeHandler:   routeHandler,
		previewHandler: previewHandler,
	}
}

func (d *ImagePushDispatcher) CanHandle(t domain.EventType) bool {
	return t == domain.EventImagePushed
}

func (d *ImagePushDispatcher) Handle(ctx context.Context, event domain.Event) error {
	log := zerowrap.FromCtx(ctx)

	payload, ok := event.Data.(domain.ImagePushedPayload)
	if !ok {
		log.Warn().Str("event_type", string(event.Type)).Msg("dispatcher received event with unexpected payload type; forwarding to route handler")
		return d.routeHandler.Handle(ctx, event)
	}

	if d.config.IsPreviewEnabled() && d.matchesTagPatterns(payload.Reference) {
		return d.previewHandler.Handle(ctx, event)
	}
	return d.routeHandler.Handle(ctx, event)
}

func (d *ImagePushDispatcher) matchesTagPatterns(reference string) bool {
	for _, pattern := range d.config.GetPreviewTagPatterns() {
		matched, err := filepath.Match(pattern, reference)
		if err != nil {
			continue // skip malformed patterns
		}
		if matched {
			return true
		}
	}
	return false
}
