package auto

import (
	"context"
	"path/filepath"

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
	payload, ok := event.Data.(domain.ImagePushedPayload)
	if !ok {
		return d.routeHandler.Handle(ctx, event)
	}

	if d.config.IsPreviewEnabled() && d.matchesTagPatterns(payload.Reference) {
		return d.previewHandler.Handle(ctx, event)
	}
	return d.routeHandler.Handle(ctx, event)
}

func (d *ImagePushDispatcher) matchesTagPatterns(reference string) bool {
	for _, pattern := range d.config.GetPreviewTagPatterns() {
		if matched, _ := filepath.Match(pattern, reference); matched {
			return true
		}
	}
	return false
}
