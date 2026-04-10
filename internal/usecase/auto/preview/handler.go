package preview

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auto"
)

// AutoPreviewHandler processes EventImagePushed for preview tag patterns.
type AutoPreviewHandler struct {
	serviceCtx     context.Context
	config         auto.AutoConfigProvider
	blobStorage    out.BlobStorage
	previewService *Service
}

func NewAutoPreviewHandler(
	serviceCtx context.Context,
	config auto.AutoConfigProvider,
	blobStorage out.BlobStorage,
	previewService *Service,
) *AutoPreviewHandler {
	return &AutoPreviewHandler{
		serviceCtx:     serviceCtx,
		config:         config,
		blobStorage:    blobStorage,
		previewService: previewService,
	}
}

func (h *AutoPreviewHandler) CanHandle(t domain.EventType) bool {
	return t == domain.EventImagePushed
}

func (h *AutoPreviewHandler) Handle(ctx context.Context, event domain.Event) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "AutoPreviewHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	payload, ok := event.Data.(domain.ImagePushedPayload)
	if !ok {
		log.Debug().Str("actual_type", fmt.Sprintf("%T", event.Data)).Msg("unexpected event data type, skipping")
		return nil
	}

	previewConfig := h.config.GetPreviewConfig()
	previewName := ExtractPreviewName(payload.Reference, previewConfig.TagPatterns)

	// Use trusted routes only. See resolveBaseRoute.
	routes := h.config.FindRoutesByImage(ctx, payload.Name)
	baseRouteDomain := resolveBaseRoute(routes, previewConfig)
	if baseRouteDomain == "" {
		log.Debug().Str("image", payload.Name).Msg("no trusted route found for preview base, skipping")
		return nil
	}

	// Validate allowlist
	allowedDomains := h.config.GetAllowedDomains()
	previewDomain, err := GeneratePreviewDomain(baseRouteDomain, previewName, previewConfig.Separator)
	if err != nil {
		log.Debug().Err(err).Str("base_domain", baseRouteDomain).Msg("failed to generate preview domain")
		return nil
	}
	if !auto.MatchesDomainAllowlist(previewDomain, allowedDomains) {
		log.Debug().Str("domain", previewDomain).Strs("patterns", allowedDomains).Msg("preview domain not in allowlist, skipping")
		return nil
	}

	imageName := payload.Name + ":" + payload.Reference

	// Launch async — volume cloning may exceed event bus timeout
	go func() {
		if err := h.previewService.CreatePreview(h.serviceCtx, CreatePreviewRequest{
			Name:          previewName,
			Domain:        previewDomain,
			BaseRoute:     baseRouteDomain,
			Image:         imageName,
			HTTPS:         true,
			PreviewConfig: previewConfig,
		}); err != nil {
			log.Error().Err(err).Str("preview", previewName).Msg("failed to create preview")
		}
	}()

	return nil
}

// resolveBaseRoute determines the base route for preview env/data inheritance.
// Security: This MUST only use trusted route config, never untrusted image labels.
// Labels are attacker-controlled and must not determine which route's env/data
// is inherited by a preview.
func resolveBaseRoute(routes []domain.Route, previewConfig domain.PreviewConfig) string {
	// Always use trusted route config, ignoring labels entirely.
	// Skip preview domains to avoid using a preview as the base for another preview.
	for _, r := range routes {
		if !isPreviewDomain(r.Domain, previewConfig.Separator) {
			return r.Domain
		}
	}
	return ""
}

// isPreviewDomain checks if a domain looks like a preview domain by examining
// only the first DNS label (before the first dot) for the separator.
// If separator is empty, no domain is treated as a preview domain.
func isPreviewDomain(domain, separator string) bool {
	if separator == "" {
		return false
	}
	// Extract the first label (subdomain) before the first dot
	firstLabel := domain
	if idx := strings.Index(domain, "."); idx != -1 {
		firstLabel = domain[:idx]
	}
	return strings.Contains(firstLabel, separator)
}
