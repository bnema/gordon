package preview

import (
	"context"
	"fmt"

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

	// Extract labels from manifest
	labels, err := auto.ExtractLabels(ctx, payload.Manifest, h.blobStorage)
	if err != nil {
		log.Debug().Err(err).Msg("failed to extract labels, skipping preview handler")
		return nil
	}

	previewConfig := h.config.GetPreviewConfig()
	previewName := ExtractPreviewName(payload.Reference, previewConfig.TagPatterns)

	// Resolve base route from labels
	domains := collectDomains(labels)
	if len(domains) == 0 {
		log.Debug().Str("image", payload.Name).Msg("no gordon.domain label found, skipping preview handler")
		return nil
	}
	baseRouteDomain := domains[0]

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

func collectDomains(labels *domain.ImageLabels) []string {
	var domains []string
	if labels.Domain != "" {
		domains = append(domains, labels.Domain)
	}
	domains = append(domains, labels.Domains...)
	return domains
}
