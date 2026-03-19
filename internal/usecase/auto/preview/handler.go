package preview

import (
	"context"

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
	registryDomain string
}

func NewAutoPreviewHandler(
	serviceCtx context.Context,
	config auto.AutoConfigProvider,
	blobStorage out.BlobStorage,
	previewService *Service,
	registryDomain string,
) *AutoPreviewHandler {
	return &AutoPreviewHandler{
		serviceCtx:     serviceCtx,
		config:         config,
		blobStorage:    blobStorage,
		previewService: previewService,
		registryDomain: registryDomain,
	}
}

func (h *AutoPreviewHandler) CanHandle(t domain.EventType) bool {
	return t == domain.EventImagePushed
}

func (h *AutoPreviewHandler) Handle(ctx context.Context, event domain.Event) error {
	payload, ok := event.Data.(domain.ImagePushedPayload)
	if !ok {
		return nil
	}

	// Extract labels from manifest
	labels, err := auto.ExtractLabels(ctx, payload.Manifest, h.blobStorage)
	if err != nil {
		return nil
	}

	previewConfig := h.config.GetPreviewConfig()
	previewName := ExtractPreviewName(payload.Reference, previewConfig.TagPatterns)

	// Resolve base route from labels
	domains := collectDomains(labels)
	if len(domains) == 0 {
		return nil
	}
	baseRouteDomain := domains[0]

	// Validate allowlist
	allowedDomains := h.config.GetAllowedDomains()
	previewDomain, err := GeneratePreviewDomain(baseRouteDomain, previewName, previewConfig.Separator)
	if err != nil {
		return nil
	}
	if !auto.MatchesDomainAllowlist(previewDomain, allowedDomains) {
		return nil
	}

	imageName := payload.Name + ":" + payload.Reference

	// Launch async — volume cloning may exceed event bus timeout
	go h.previewService.CreatePreview(h.serviceCtx, CreatePreviewRequest{
		Name:          previewName,
		Domain:        previewDomain,
		BaseRoute:     baseRouteDomain,
		Image:         imageName,
		HTTPS:         true,
		PreviewConfig: previewConfig,
	})

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
