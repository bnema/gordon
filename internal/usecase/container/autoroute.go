package container

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
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

	// Build full image reference for env extraction (Docker needs fully qualified name)
	fullImageRef := h.buildFullImageRef(imageName)

	for _, routeDomain := range domains {
		routeDomain = strings.TrimSpace(routeDomain)
		if routeDomain == "" {
			continue
		}

		h.createOrUpdateRoute(ctx, routeDomain, imageName)

		if labels.EnvFile != "" && h.extractor != nil && h.envDir != "" {
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
func (h *AutoRouteHandler) createOrUpdateRoute(ctx context.Context, routeDomain, imageName string) {
	log := zerowrap.FromCtx(ctx)

	route := domain.Route{
		Domain: routeDomain,
		Image:  imageName,
	}

	existingRoutes := h.configSvc.GetRoutes(ctx)
	for _, existing := range existingRoutes {
		if existing.Domain == routeDomain {
			if existing.Image != imageName {
				if err := h.configSvc.UpdateRoute(ctx, route); err != nil {
					log.Warn().Err(err).Str("domain", routeDomain).Msg("failed to update route")
				} else {
					log.Info().Str("domain", routeDomain).Str("image", imageName).Msg("auto-route updated from image labels")
					// Trigger deploy for updated route
					h.triggerDeploy(ctx, route)
				}
			}
			return
		}
	}

	if err := h.configSvc.AddRoute(ctx, route); err != nil {
		log.Warn().Err(err).Str("domain", routeDomain).Msg("failed to add route")
	} else {
		log.Info().Str("domain", routeDomain).Str("image", imageName).Msg("auto-route added from image labels")
		// Trigger deploy for new route since ImagePushedHandler may have already finished
		h.triggerDeploy(ctx, route)
	}
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

// extractAndMergeEnvFile extracts an env file from the image and merges it with existing secrets.
func (h *AutoRouteHandler) extractAndMergeEnvFile(ctx context.Context, imageRef, routeDomain, envFilePath string) error {
	log := zerowrap.FromCtx(ctx)

	log.Info().
		Str("domain", routeDomain).
		Str("image", imageRef).
		Str("env_file", envFilePath).
		Msg("extracting env file from image")

	// Extract env file from image
	envData, err := h.extractor.ExtractEnvFileFromImage(ctx, imageRef, envFilePath)
	if err != nil {
		return fmt.Errorf("failed to extract env file: %w", err)
	}

	if len(envData) == 0 {
		log.Debug().Msg("env file is empty, skipping")
		return nil
	}

	// Parse the extracted env file
	imageEnv, err := parseEnvFile(envData)
	if err != nil {
		return fmt.Errorf("failed to parse env file: %w", err)
	}

	if len(imageEnv) == 0 {
		log.Debug().Msg("no environment variables found in env file")
		return nil
	}

	// Load existing env file for this domain
	envFileName := domainToEnvFileName(routeDomain)
	envFileDst := filepath.Join(h.envDir, envFileName)

	existingEnv := make(map[string]string)
	if data, err := os.ReadFile(envFileDst); err == nil {
		existingEnv, _ = parseEnvFile(data)
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

	// Parse manifest to get config digest
	configDigest, err := parseConfigDigest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	if configDigest == "" {
		return nil, fmt.Errorf("no config digest found in manifest")
	}

	log.Debug().Str("config_digest", configDigest).Msg("found config digest")

	// Read config blob
	reader, err := h.blobStorage.GetBlob(configDigest)
	if err != nil {
		return nil, fmt.Errorf("failed to get config blob: %w", err)
	}
	defer reader.Close()

	configData, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read config blob: %w", err)
	}

	// Parse config to extract labels
	labels, err := parseImageLabels(configData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return labels, nil
}

// manifestSchema represents the relevant parts of an OCI/Docker manifest.
type manifestSchema struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Config        struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"config"`
}

// parseConfigDigest extracts the config digest from a manifest.
func parseConfigDigest(manifestData []byte) (string, error) {
	var manifest manifestSchema
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", err
	}

	return manifest.Config.Digest, nil
}

// imageConfig represents the relevant parts of an OCI/Docker image config.
type imageConfig struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

// parseImageLabels extracts Gordon labels from an image config.
func parseImageLabels(configData []byte) (*domain.ImageLabels, error) {
	var config imageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, err
	}

	labels := &domain.ImageLabels{}

	if config.Config.Labels == nil {
		return labels, nil
	}

	// Extract gordon.* labels
	if v, ok := config.Config.Labels[domain.LabelDomain]; ok {
		labels.Domain = strings.TrimSpace(v)
	}

	if v, ok := config.Config.Labels[domain.LabelDomains]; ok {
		// Parse comma-separated domains
		for _, d := range strings.Split(v, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				labels.Domains = append(labels.Domains, d)
			}
		}
	}

	if v, ok := config.Config.Labels[domain.LabelHealth]; ok {
		labels.Health = strings.TrimSpace(v)
	}

	if v, ok := config.Config.Labels[domain.LabelPort]; ok {
		labels.Port = strings.TrimSpace(v)
	}

	if v, ok := config.Config.Labels[domain.LabelEnvFile]; ok {
		labels.EnvFile = strings.TrimSpace(v)
	}

	return labels, nil
}

// parseEnvFile parses an env file into a map of key-value pairs.
func parseEnvFile(data []byte) (map[string]string, error) {
	env := make(map[string]string)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove surrounding quotes if present
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		if key != "" {
			env[key] = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return env, nil
}

// domainToEnvFileName converts a domain to an env file name.
// e.g., "app.mydomain.com" -> "app_mydomain_com.env"
// Must match the naming convention in envloader.FileLoader.getEnvFilePath
func domainToEnvFileName(domainName string) string {
	// Replace special characters with underscores (matches envloader)
	name := strings.ReplaceAll(domainName, ".", "_")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "/", "_")
	return name + ".env"
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
