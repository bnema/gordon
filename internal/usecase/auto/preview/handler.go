package preview

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auto"
)

// AutoPreviewHandler processes EventImagePushed for preview tag patterns.
type AutoPreviewHandler struct {
	serviceCtx     context.Context
	config         auto.AutoConfigProvider
	previewService *Service
}

func NewAutoPreviewHandler(
	serviceCtx context.Context,
	config auto.AutoConfigProvider,
	previewService *Service,
) *AutoPreviewHandler {
	return &AutoPreviewHandler{
		serviceCtx:     serviceCtx,
		config:         config,
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
func resolveBaseRoute(routes []domain.Route, previewConfig domain.PreviewConfig) string {
	// Build a set of all route domains for context-aware preview detection.
	domainSet := make(map[string]struct{}, len(routes))
	for _, r := range routes {
		domainSet[r.Domain] = struct{}{}
	}

	// Always use trusted route config, ignoring labels entirely.
	// Skip preview domains to avoid using a preview as the base for another preview.
	for _, r := range routes {
		if !isPreviewDomain(r.Domain, previewConfig.Separator, domainSet) {
			return r.Domain
		}
	}
	return ""
}

// isPreviewDomain checks if a domain is a preview domain by examining the first
// DNS label for the separator AND verifying that the implied base domain exists
// in the route set. This avoids false positives like "my--app.example.com" when
// no route "my.example.com" exists — that domain just happens to contain the
// separator in its name.
// If separator is empty, no domain is treated as a preview domain.
func isPreviewDomain(d, separator string, routeSet map[string]struct{}) bool {
	if separator == "" {
		return false
	}
	// Extract the first label (subdomain) before the first dot
	firstLabel := d
	rest := ""
	if idx := strings.Index(d, "."); idx != -1 {
		firstLabel = d[:idx]
		rest = d[idx:] // includes leading dot
	}
	// Check if the first label contains the separator
	sepIdx := strings.Index(firstLabel, separator)
	if sepIdx < 0 {
		return false
	}
	// Reconstruct the potential base domain: everything before the separator + rest
	baseLabel := firstLabel[:sepIdx]
	if baseLabel == "" {
		return false
	}
	baseDomain := baseLabel + rest
	// Only treat as preview if the base domain actually exists in the route set
	_, exists := routeSet[baseDomain]
	return exists
}
