// Package admin implements the HTTP adapter for the admin API.
package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/in"
	"gordon/internal/boundaries/out"
	"gordon/internal/domain"
)

// Handler implements the HTTP handler for the admin API.
type Handler struct {
	configSvc    in.ConfigService
	authSvc      in.AuthService
	containerSvc in.ContainerService
	eventBus     out.EventPublisher
	envDir       string
	log          zerowrap.Logger
}

// NewHandler creates a new admin HTTP handler.
func NewHandler(
	configSvc in.ConfigService,
	authSvc in.AuthService,
	containerSvc in.ContainerService,
	eventBus out.EventPublisher,
	envDir string,
	log zerowrap.Logger,
) *Handler {
	return &Handler{
		configSvc:    configSvc,
		authSvc:      authSvc,
		containerSvc: containerSvc,
		eventBus:     eventBus,
		envDir:       envDir,
		log:          log,
	}
}

// RegisterRoutes registers the admin routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/admin/", h.handleAdminRoutes)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handleAdminRoutes(w, r)
}

func (h *Handler) handleAdminRoutes(w http.ResponseWriter, r *http.Request) {
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "http",
		zerowrap.FieldHandler: "admin",
		zerowrap.FieldMethod:  r.Method,
		zerowrap.FieldPath:    r.URL.Path,
	})
	r = r.WithContext(ctx)

	path := strings.TrimPrefix(r.URL.Path, "/admin")

	// Route to appropriate handler
	switch {
	case path == "/routes" || strings.HasPrefix(path, "/routes/"):
		h.handleRoutes(w, r, path)
	case path == "/secrets" || strings.HasPrefix(path, "/secrets/"):
		h.handleSecrets(w, r, path)
	case path == "/status":
		h.handleStatus(w, r)
	case path == "/reload":
		h.handleReload(w, r)
	case path == "/config":
		h.handleConfig(w, r)
	default:
		http.NotFound(w, r)
	}
}

// sendJSON sends a JSON response.
func (h *Handler) sendJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// sendError sends an error response.
func (h *Handler) sendError(w http.ResponseWriter, status int, message string) {
	h.sendJSON(w, status, map[string]string{"error": message})
}

// handleRoutes handles /admin/routes endpoints.
func (h *Handler) handleRoutes(w http.ResponseWriter, r *http.Request, path string) {
	// Parse domain from path if present
	routeDomain := strings.TrimPrefix(path, "/routes/")
	if routeDomain == "/routes" {
		routeDomain = ""
	}

	switch r.Method {
	case http.MethodGet:
		h.handleRoutesGet(w, r, routeDomain)
	case http.MethodPost:
		h.handleRoutesPost(w, r)
	case http.MethodPut:
		h.handleRoutesPut(w, r, routeDomain)
	case http.MethodDelete:
		h.handleRoutesDelete(w, r, routeDomain)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleRoutesGet(w http.ResponseWriter, r *http.Request, routeDomain string) {
	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for routes:read")
		return
	}

	routes := h.configSvc.GetRoutes(ctx)

	if routeDomain == "" {
		h.sendJSON(w, http.StatusOK, map[string]any{"routes": routes})
		return
	}

	for _, route := range routes {
		if route.Domain == routeDomain {
			h.sendJSON(w, http.StatusOK, route)
			return
		}
	}
	h.sendError(w, http.StatusNotFound, "route not found")
}

func (h *Handler) handleRoutesPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission
	if !HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for routes:write")
		return
	}

	var route domain.Route
	if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
		log.Warn().Err(err).Msg("invalid route JSON")
		h.sendError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if route.Domain == "" || route.Image == "" {
		h.sendError(w, http.StatusBadRequest, "domain and image are required")
		return
	}

	if err := h.configSvc.AddRoute(ctx, route); err != nil {
		log.Error().Err(err).Str("domain", route.Domain).Msg("failed to add route")
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("domain", route.Domain).Str("image", route.Image).Msg("route added")
	h.sendJSON(w, http.StatusCreated, route)
}

func (h *Handler) handleRoutesPut(w http.ResponseWriter, r *http.Request, routeDomain string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission
	if !HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for routes:write")
		return
	}

	if routeDomain == "" {
		h.sendError(w, http.StatusBadRequest, "domain required in path")
		return
	}

	var route domain.Route
	if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
		log.Warn().Err(err).Msg("invalid route JSON")
		h.sendError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	route.Domain = routeDomain

	if err := h.configSvc.UpdateRoute(ctx, route); err != nil {
		log.Error().Err(err).Str("domain", routeDomain).Msg("failed to update route")
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("domain", route.Domain).Str("image", route.Image).Msg("route updated")
	h.sendJSON(w, http.StatusOK, route)
}

func (h *Handler) handleRoutesDelete(w http.ResponseWriter, r *http.Request, routeDomain string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission
	if !HasAccess(ctx, domain.AdminResourceRoutes, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for routes:write")
		return
	}

	if routeDomain == "" {
		h.sendError(w, http.StatusBadRequest, "domain required in path")
		return
	}

	if err := h.configSvc.RemoveRoute(ctx, routeDomain); err != nil {
		log.Error().Err(err).Str("domain", routeDomain).Msg("failed to remove route")
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("domain", routeDomain).Msg("route removed")
	h.sendJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// handleSecrets handles /admin/secrets endpoints.
func (h *Handler) handleSecrets(w http.ResponseWriter, r *http.Request, path string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Parse path: /secrets/{domain} or /secrets/{domain}/{key}
	parts := strings.Split(strings.TrimPrefix(path, "/secrets/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		h.sendError(w, http.StatusBadRequest, "domain required")
		return
	}

	secretDomain := parts[0]
	secretKey := ""
	if len(parts) > 1 {
		secretKey = parts[1]
	}

	switch r.Method {
	case http.MethodGet:
		// Check read permission
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionRead) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:read")
			return
		}
		// List secrets for domain (names only, not values)
		secrets, err := h.listSecrets(secretDomain)
		if err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Msg("failed to list secrets")
			h.sendError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.sendJSON(w, http.StatusOK, map[string]any{"domain": secretDomain, "keys": secrets})

	case http.MethodPost:
		// Check write permission
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
			return
		}
		// Set secret(s)
		var data map[string]string
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			log.Warn().Err(err).Msg("invalid secrets JSON")
			h.sendError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		if err := h.setSecrets(secretDomain, data); err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Msg("failed to set secrets")
			h.sendError(w, http.StatusInternalServerError, err.Error())
			return
		}

		log.Info().Str("domain", secretDomain).Int("count", len(data)).Msg("secrets set")
		h.sendJSON(w, http.StatusOK, map[string]string{"status": "updated"})

	case http.MethodDelete:
		// Check write permission
		if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
			return
		}
		if secretKey == "" {
			h.sendError(w, http.StatusBadRequest, "key required in path")
			return
		}

		if err := h.deleteSecret(secretDomain, secretKey); err != nil {
			log.Error().Err(err).Str("domain", secretDomain).Str("key", secretKey).Msg("failed to delete secret")
			h.sendError(w, http.StatusInternalServerError, err.Error())
			return
		}

		log.Info().Str("domain", secretDomain).Str("key", secretKey).Msg("secret deleted")
		h.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStatus handles /admin/status endpoint.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	routes := h.configSvc.GetRoutes(ctx)

	// Get container statuses
	statuses := make(map[string]string)
	for _, route := range routes {
		status := "unknown"
		container, ok := h.containerSvc.Get(ctx, route.Domain)
		if ok && container != nil {
			status = container.Status
		}
		statuses[route.Domain] = status
	}

	status := map[string]any{
		"routes":            len(routes),
		"registry_domain":   h.configSvc.GetRegistryDomain(),
		"registry_port":     h.configSvc.GetRegistryPort(),
		"server_port":       h.configSvc.GetServerPort(),
		"auto_route":        h.configSvc.IsAutoRouteEnabled(),
		"network_isolation": h.configSvc.IsNetworkIsolationEnabled(),
		"container_status":  statuses,
	}

	h.sendJSON(w, http.StatusOK, status)
}

// handleReload handles /admin/reload endpoint.
// This reloads configuration from file into memory and triggers container sync.
func (h *Handler) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission (reload modifies state)
	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:write")
		return
	}

	// Reload configuration from file into memory
	if err := h.configSvc.Load(ctx); err != nil {
		log.Error().Err(err).Msg("failed to reload config")
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Publish manual reload event to sync containers
	// ManualReloadHandler starts missing containers without restarting running ones
	if h.eventBus != nil {
		if err := h.eventBus.Publish(domain.EventManualReload, nil); err != nil {
			log.Warn().Err(err).Msg("failed to publish manual reload event")
		}
	}

	log.Info().Msg("config reloaded via admin API")
	h.sendJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// handleConfig handles /admin/config endpoint.
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:read")
		return
	}

	config := map[string]any{
		"server": map[string]any{
			"port":            h.configSvc.GetServerPort(),
			"registry_port":   h.configSvc.GetRegistryPort(),
			"registry_domain": h.configSvc.GetRegistryDomain(),
			"data_dir":        h.configSvc.GetDataDir(),
		},
		"auto_route": map[string]any{
			"enabled": h.configSvc.IsAutoRouteEnabled(),
		},
		"network_isolation": map[string]any{
			"enabled": h.configSvc.IsNetworkIsolationEnabled(),
			"prefix":  h.configSvc.GetNetworkPrefix(),
		},
		"routes":          h.configSvc.GetRoutes(ctx),
		"external_routes": h.configSvc.GetExternalRoutes(),
	}

	h.sendJSON(w, http.StatusOK, config)
}
