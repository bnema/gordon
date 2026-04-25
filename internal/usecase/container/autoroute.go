package container

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auto"
)

// EnvFileExtractor defines the interface for extracting env files from images.
type EnvFileExtractor interface {
	ExtractEnvFileFromImage(ctx context.Context, imageRef, envFilePath string) ([]byte, error)
}

// AutoRouteHandler handles image.pushed events for auto-route from labels.
type AutoRouteHandler struct {
	configSvc      in.ConfigService
	containerSvc   in.ContainerService
	blobStorage    out.BlobStorage
	extractor      EnvFileExtractor
	registryDomain string
	envDir         string
	ctx            context.Context
}

// NewAutoRouteHandler creates a new AutoRouteHandler.
func NewAutoRouteHandler(
	ctx context.Context,
	configSvc in.ConfigService,
	containerSvc in.ContainerService,
	blobStorage out.BlobStorage,
	registryDomain string,
) *AutoRouteHandler {
	return &AutoRouteHandler{
		configSvc:      configSvc,
		containerSvc:   containerSvc,
		blobStorage:    blobStorage,
		registryDomain: registryDomain,
		ctx:            ctx,
	}
}

// WithEnvExtractor sets the env file extractor for auto env-file copy feature.
func (h *AutoRouteHandler) WithEnvExtractor(extractor EnvFileExtractor, envDir string) *AutoRouteHandler {
	h.extractor = extractor
	h.envDir = envDir
	return h
}

// Handle handles an image.pushed event and creates routes from labels.
func (h *AutoRouteHandler) Handle(ctx context.Context, event domain.Event) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "AutoRouteHandler",
		zerowrap.FieldEvent:   string(event.Type),
		"event_id":            event.ID,
	})
	log := zerowrap.FromCtx(ctx)

	if !h.configSvc.IsAutoRouteEnabled() {
		log.Debug().Msg("auto-route disabled, skipping label extraction")
		return nil
	}

	payload, ok := event.Data.(domain.ImagePushedPayload)
	if !ok {
		log.Warn().Msg("invalid event payload type")
		return nil
	}

	if len(payload.Manifest) == 0 {
		log.Debug().Msg("no manifest data in event, skipping")
		return nil
	}

	log.Info().
		Str("image", payload.Name).
		Str("reference", payload.Reference).
		Msg("processing image for auto-route labels")

	labels, err := h.extractLabels(ctx, payload.Manifest)
	if err != nil {
		log.Debug().Err(err).Msg("failed to extract labels, skipping auto-route")
		return nil
	}

	domains := h.collectDomains(labels)
	if len(domains) == 0 {
		log.Debug().Str("image", payload.Name).Msg("no gordon.domain label found, skipping")
		return nil
	}

	imageName := h.buildImageName(payload.Name, payload.Reference)
	h.processRoutes(ctx, domains, imageName, labels)

	return nil
}

// collectDomains collects all domains from labels.
func (h *AutoRouteHandler) collectDomains(labels *domain.ImageLabels) []string {
	var domains []string
	if labels.Domain != "" {
		domains = append(domains, labels.Domain)
	}
	domains = append(domains, labels.Domains...)
	return domains
}

// buildImageName constructs the full image name with tag or digest.
// For digest references (sha256:...), uses @ separator per Docker spec.
// For tags, uses : separator.
func (h *AutoRouteHandler) buildImageName(name, reference string) string {
	if reference == "" {
		return name
	}
	// Digest references use @ separator, tags use :
	if strings.HasPrefix(reference, "sha256:") {
		return fmt.Sprintf("%s@%s", name, reference)
	}
	return fmt.Sprintf("%s:%s", name, reference)
}

// buildFullImageRef constructs a fully qualified image reference with registry domain.
// This is needed for Docker operations since images are stored with the registry prefix.
func (h *AutoRouteHandler) buildFullImageRef(imageName string) string {
	if h.registryDomain == "" {
		return imageName
	}

	// Don't prefix if already has registry domain
	if strings.HasPrefix(imageName, h.registryDomain+"/") {
		return imageName
	}

	// Don't prefix if it's an external registry reference (contains dots and slashes)
	repoPart := strings.Split(imageName, ":")[0]
	if strings.Contains(repoPart, "@") {
		repoPart = strings.Split(imageName, "@")[0]
	}
	if strings.Contains(repoPart, ".") && strings.Contains(repoPart, "/") {
		return imageName
	}

	return fmt.Sprintf("%s/%s", h.registryDomain, imageName)
}

// processRoutes processes each domain and creates/updates routes.
func (h *AutoRouteHandler) processRoutes(ctx context.Context, domains []string, imageName string, labels *domain.ImageLabels) {
	log := zerowrap.FromCtx(ctx)

	// Load allowed domains once for all routes in this event.
	allowedDomains, err := h.configSvc.GetAutoRouteAllowedDomains(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("failed to load auto-route allowed domains, skipping route processing")
		return
	}

	// Build full image reference for env extraction (Docker needs fully qualified name)
	fullImageRef := h.buildFullImageRef(imageName)

	for _, routeDomain := range domains {
		routeDomain = strings.TrimSpace(routeDomain)
		if routeDomain == "" {
			continue
		}

		canonicalDomain, ok := domain.CanonicalRouteDomain(routeDomain)
		if !ok {
			log.Warn().Str("domain", routeDomain).Msg("auto-route rejected invalid domain")
			continue
		}
		routeDomain = canonicalDomain

		created := h.createOrUpdateRoute(ctx, routeDomain, imageName, allowedDomains)

		if created && labels.EnvFile != "" && h.extractor != nil && h.envDir != "" {
			// Use full image ref for extraction (Docker knows image by this name)
			if err := h.extractAndMergeEnvFile(ctx, fullImageRef, routeDomain, labels.EnvFile); err != nil {
				log.Warn().
					Err(err).
					Str("domain", routeDomain).
					Str("env_file", labels.EnvFile).
					Msg("failed to extract env file from image")
			}
		}
	}
}

// createOrUpdateRoute creates a new route or updates an existing one.
// It also triggers deployment for new routes since the ImagePushedHandler
// may have already finished processing (handlers run concurrently).
func (h *AutoRouteHandler) createOrUpdateRoute(ctx context.Context, routeDomain, imageName string, allowedDomains []string) bool {
	log := zerowrap.FromCtx(ctx)

	route := domain.Route{
		Domain: routeDomain,
		Image:  imageName,
		HTTPS:  true,
	}

	existingRoutes := h.configSvc.GetRoutes(ctx)
	for _, existing := range existingRoutes {
		if existing.Domain == routeDomain {
			route.HTTPS = existing.HTTPS
			if auto.ExtractRepoName(existing.Image, h.registryDomain) != auto.ExtractRepoName(imageName, h.registryDomain) {
				log.Warn().Str("domain", routeDomain).Str("existing_image", existing.Image).Str("image", imageName).Msg("auto-route update rejected due to repository ownership mismatch")
				return false
			}
			if existing.Image != imageName {
				if err := h.configSvc.UpdateRoute(ctx, route); err != nil {
					log.Warn().Err(err).Str("domain", routeDomain).Msg("failed to update route")
				} else {
					log.Info().Str("domain", routeDomain).Str("image", imageName).Msg("auto-route updated from image labels")
					// Trigger deploy for updated route
					h.triggerDeploy(ctx, route)
				}
			}
			return false
		}
	}

	if !auto.MatchesDomainAllowlist(routeDomain, allowedDomains) {
		log.Warn().Str("domain", routeDomain).Strs("allowed_domains", allowedDomains).Msg("auto-route create rejected due to domain allowlist")
		return false
	}

	if err := h.configSvc.AddRoute(ctx, route); err != nil {
		log.Warn().Err(err).Str("domain", routeDomain).Msg("failed to add route")
	} else {
		log.Info().Str("domain", routeDomain).Str("image", imageName).Msg("auto-route added from image labels")
		// Trigger deploy for new route since ImagePushedHandler may have already finished
		h.triggerDeploy(ctx, route)
		return true
	}

	return false
}

// triggerDeploy initiates deployment for a route.
func (h *AutoRouteHandler) triggerDeploy(ctx context.Context, route domain.Route) {
	log := zerowrap.FromCtx(ctx)

	if h.containerSvc == nil {
		log.Debug().Str("domain", route.Domain).Msg("no container service, skipping deploy trigger")
		return
	}

	// Mark context as internal deploy - the event originated from our own registry,
	// so we can use internal registry (localhost) for image pulls.
	internalCtx := domain.WithInternalDeploy(ctx)

	if _, err := h.containerSvc.Deploy(internalCtx, route); err != nil {
		log.Warn().Err(err).Str("domain", route.Domain).Msg("failed to trigger deploy for auto-route")
	} else {
		log.Info().Str("domain", route.Domain).Msg("deploy triggered for auto-route")
	}
}

func allowedAbsoluteEnvFileRoots() []string {
	return []string{"/app", "/workspace", "/usr/src/app"}
}

func validateAutoRouteEnvFilePath(envFilePath string) (string, error) {
	envFilePath = strings.TrimSpace(envFilePath)
	if envFilePath == "" {
		return "", fmt.Errorf("env file path cannot be empty")
	}
	if strings.Contains(envFilePath, "\x00") {
		return "", fmt.Errorf("env file path contains NUL byte")
	}

	cleaned := filepath.Clean(envFilePath)
	if cleaned == "." || cleaned == string(filepath.Separator) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe env file path: %s", envFilePath)
	}
	if filepath.IsAbs(cleaned) && !pathHasAllowedRoot(cleaned, allowedAbsoluteEnvFileRoots()) {
		return "", fmt.Errorf("unsafe env file path: %s", envFilePath)
	}
	return cleaned, nil
}

func pathHasAllowedRoot(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// extractAndMergeEnvFile extracts an env file from the image and merges it with existing secrets.
func (h *AutoRouteHandler) extractAndMergeEnvFile(ctx context.Context, imageRef, routeDomain, envFilePath string) error {
	log := zerowrap.FromCtx(ctx)

	log.Info().
		Str("domain", routeDomain).
		Str("image", imageRef).
		Str("env_file", envFilePath).
		Msg("extracting env file from image")

	safeEnvFilePath, err := validateAutoRouteEnvFilePath(envFilePath)
	if err != nil {
		return err
	}

	// Extract env file from image
	envData, err := h.extractor.ExtractEnvFileFromImage(ctx, imageRef, safeEnvFilePath)
	if err != nil {
		return fmt.Errorf("failed to extract env file: %w", err)
	}

	if len(envData) == 0 {
		log.Debug().Msg("env file is empty, skipping")
		return nil
	}

	// Parse the extracted env file
	imageEnv, err := domain.ParseEnvData(envData)
	if err != nil {
		return fmt.Errorf("failed to parse env file: %w", err)
	}

	if len(imageEnv) == 0 {
		log.Debug().Msg("no environment variables found in env file")
		return nil
	}

	if err := rejectSecretReferences(imageEnv); err != nil {
		return err
	}

	// Load existing env file for this domain
	envFileName, err := domainToEnvFileName(routeDomain)
	if err != nil {
		return fmt.Errorf("invalid env storage domain: %w", err)
	}
	envFileDst := filepath.Join(h.envDir, envFileName)

	existingEnv := make(map[string]string)
	if data, err := os.ReadFile(envFileDst); err == nil {
		var parseErr error
		existingEnv, parseErr = domain.ParseEnvData(data)
		if parseErr != nil {
			return fmt.Errorf("failed to parse existing env file %q: %w", envFileDst, parseErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read existing env file %q: %w", envFileDst, err)
	}

	// Merge: image values are defaults, existing values win
	merged := make(map[string]string)
	for k, v := range imageEnv {
		merged[k] = v
	}
	for k, v := range existingEnv {
		merged[k] = v // Existing values override image values
	}

	// Write merged env file
	if err := os.MkdirAll(h.envDir, 0700); err != nil {
		return fmt.Errorf("failed to create env directory: %w", err)
	}

	if err := writeEnvFile(envFileDst, merged); err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}

	log.Info().
		Str("domain", routeDomain).
		Int("image_vars", len(imageEnv)).
		Int("existing_vars", len(existingEnv)).
		Int("merged_vars", len(merged)).
		Msg("env file extracted and merged from image")

	return nil
}

// CanHandle returns whether this handler can handle the given event type.
func (h *AutoRouteHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventImagePushed
}

// extractLabels extracts Gordon labels from an image manifest.
func (h *AutoRouteHandler) extractLabels(ctx context.Context, manifestData []byte) (*domain.ImageLabels, error) {
	log := zerowrap.FromCtx(ctx)

	labels, err := auto.ExtractLabels(ctx, manifestData, h.blobStorage)
	if err != nil {
		return nil, err
	}

	// Log the config digest for observability (best-effort parse for logging only)
	if digest, parseErr := auto.ParseConfigDigest(manifestData); parseErr == nil && digest != "" {
		log.Debug().Str("config_digest", digest).Msg("found config digest")
	}

	return labels, nil
}

// domainToEnvFileName converts a domain to an env file name.
// Must match the naming convention in envloader.FileLoader.getEnvFilePath.
func domainToEnvFileName(domainName string) (string, error) {
	storageKey, err := domain.NewEnvStorageKey(domainName)
	if err != nil {
		return "", err
	}

	return storageKey.FileName(), nil
}

func rejectSecretReferences(env map[string]string) error {
	// Security check: reject imported env files containing secret references
	// to prevent attacker-controlled images from persisting ${provider:path}
	// syntax that would later resolve against host secret providers.
	for key, value := range env {
		if domain.ContainsSecretReference(value) {
			return fmt.Errorf("env key %q contains secret reference: %w", key, domain.ErrEnvContainsSecretRef)
		}
	}
	return nil
}

// writeEnvFile writes environment variables to a file.
func writeEnvFile(path string, env map[string]string) error {
	var buf bytes.Buffer

	// Sort keys for consistent output
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Write each key-value pair
	for _, k := range keys {
		v := env[k]
		// Quote values that contain special characters
		if strings.ContainsAny(v, " \t\n\"'$\\") {
			v = fmt.Sprintf("\"%s\"", strings.ReplaceAll(v, "\"", "\\\""))
		}
		fmt.Fprintf(&buf, "%s=%s\n", k, v)
	}

	return os.WriteFile(path, buf.Bytes(), 0600)
}
