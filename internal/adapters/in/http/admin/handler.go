// Package admin implements the HTTP adapter for the admin API.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/registry"
	"github.com/bnema/gordon/pkg/validation"
)

// maxAdminRequestSize is the maximum allowed size for admin API request bodies.
const maxAdminRequestSize = 1 << 20 // 1MB

// maxLogLines is the maximum allowed number of log lines that can be requested.
const maxLogLines = 10000

type reloadTrigger interface {
	Trigger(ctx context.Context) error
}

// Handler implements the HTTP handler for the admin API.
type Handler struct {
	configSvc       in.ConfigService
	authSvc         in.AuthService
	containerSvc    in.ContainerService
	backupSvc       in.BackupService
	volumeBackupSvc in.VolumeBackupService
	imageSvc        in.ImageService
	healthSvc       in.HealthService
	secretSvc       in.SecretService
	logSvc          in.LogService
	volumeSvc       in.VolumeService
	registrySvc     in.RegistryService
	reloadTrigger   reloadTrigger
	publicTLSSvc    in.PublicTLSService
	trafficSvc      in.TrafficStatusService
	appSvc          in.AppService
	log             zerowrap.Logger
}

func toBackupJobResponse(job domain.BackupJob) dto.BackupJob {
	var startedAt *time.Time
	if !job.StartedAt.IsZero() {
		t := job.StartedAt
		startedAt = &t
	}
	var completedAt *time.Time
	if !job.CompletedAt.IsZero() {
		t := job.CompletedAt
		completedAt = &t
	}

	return dto.BackupJob{
		ID:          job.ID,
		App:         job.App,
		Service:     job.Service,
		Database:    job.DBName,
		Schedule:    string(job.Schedule),
		Type:        string(job.Type),
		Status:      string(job.Status),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		SizeBytes:   job.SizeBytes,
		Error:       job.Error,
	}
}

func toVolumeBackupJobResponse(job domain.VolumeBackupJob) dto.VolumeBackupJob {
	var startedAt *time.Time
	if !job.StartedAt.IsZero() {
		t := job.StartedAt
		startedAt = &t
	}
	var completedAt *time.Time
	if !job.CompletedAt.IsZero() {
		t := job.CompletedAt
		completedAt = &t
	}

	return dto.VolumeBackupJob{
		ID:                job.ID,
		App:               job.App,
		Service:           job.Service,
		VolumeName:        job.VolumeName,
		RuntimeVolumeName: job.RuntimeVolumeName,
		MountPath:         job.MountPath,
		Compression:       job.Metadata["compression"],
		Type:              string(job.Type),
		Status:            string(job.Status),
		StartedAt:         startedAt,
		CompletedAt:       completedAt,
		SizeBytes:         job.SizeBytes,
		ArtifactRef:       job.ArtifactRef,
		Error:             job.Error,
	}
}

// HandlerDeps contains all dependencies for the admin HTTP handler.
type HandlerDeps struct {
	ConfigSvc       in.ConfigService
	AuthSvc         in.AuthService
	ContainerSvc    in.ContainerService
	HealthSvc       in.HealthService
	SecretSvc       in.SecretService
	LogSvc          in.LogService
	RegistrySvc     in.RegistryService
	Log             zerowrap.Logger
	BackupSvc       in.BackupService
	VolumeBackupSvc in.VolumeBackupService
	ImageSvc        in.ImageService
	VolumeSvc       in.VolumeService
	ReloadTrigger   reloadTrigger
	PublicTLSSvc    in.PublicTLSService
	TrafficSvc      in.TrafficStatusService
	AppSvc          in.AppService
}

// NewHandler creates a new admin HTTP handler.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{
		configSvc:       deps.ConfigSvc,
		authSvc:         deps.AuthSvc,
		containerSvc:    deps.ContainerSvc,
		backupSvc:       deps.BackupSvc,
		volumeBackupSvc: deps.VolumeBackupSvc,
		imageSvc:        deps.ImageSvc,
		healthSvc:       deps.HealthSvc,
		secretSvc:       deps.SecretSvc,
		logSvc:          deps.LogSvc,
		volumeSvc:       deps.VolumeSvc,
		registrySvc:     deps.RegistrySvc,
		reloadTrigger:   deps.ReloadTrigger,
		publicTLSSvc:    deps.PublicTLSSvc,
		trafficSvc:      deps.TrafficSvc,
		appSvc:          deps.AppSvc,
		log:             deps.Log,
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
	if handler, ok := h.matchRoute(path); ok {
		handler(w, r, path)
		return
	}
	h.sendError(w, http.StatusNotFound, "route not found")
}

// routeHandler is the signature for path-based route handlers.
type routeHandler func(w http.ResponseWriter, r *http.Request, path string)

// matchRoute returns the handler for a given path, or false if not found.
func (h *Handler) matchRoute(path string) (routeHandler, bool) {
	// Retired legacy mutations answer 410 Gone before any other match.
	if isRetiredMutation(path) {
		return h.handleRetiredMutation, true
	}
	// Exact match routes
	exactRoutes := map[string]routeHandler{
		"/networks":       func(w http.ResponseWriter, r *http.Request, _ string) { h.handleNetworks(w, r) },
		"/status":         func(w http.ResponseWriter, r *http.Request, _ string) { h.handleStatus(w, r) },
		"/health":         func(w http.ResponseWriter, r *http.Request, _ string) { h.handleHealth(w, r) },
		"/reload":         func(w http.ResponseWriter, r *http.Request, _ string) { h.handleReload(w, r) },
		"/config":         func(w http.ResponseWriter, r *http.Request, _ string) { h.handleConfig(w, r) },
		"/auth/verify":    func(w http.ResponseWriter, r *http.Request, _ string) { h.handleAuthVerify(w, r) },
		"/volumes":        func(w http.ResponseWriter, r *http.Request, _ string) { h.handleListVolumes(w, r) },
		"/volumes/prune":  func(w http.ResponseWriter, r *http.Request, _ string) { h.handlePruneVolumes(w, r) },
		"/tls/status":     func(w http.ResponseWriter, r *http.Request, _ string) { h.handleTLSStatus(w, r) },
		"/traffic/status": func(w http.ResponseWriter, r *http.Request, _ string) { h.handleTrafficStatus(w, r) },
	}
	if handler, ok := exactRoutes[path]; ok {
		return handler, true
	}

	// Prefix match routes
	prefixRoutes := []struct {
		prefix  string
		handler routeHandler
	}{
		{"/apps", h.handleApps},
		{"/backups", h.handleBackups},
		{"/secrets", h.handleSecrets},
		{"/tags", h.handleTags},
		{"/images", h.handleImages},
		{"/logs", h.handleLogs},
	}
	for _, route := range prefixRoutes {
		if path == route.prefix || strings.HasPrefix(path, route.prefix+"/") {
			return route.handler, true
		}
	}

	return nil, false
}

func (h *Handler) sendJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// sendError sends an error response.
func (h *Handler) sendError(w http.ResponseWriter, status int, message string) {
	h.sendJSON(w, status, dto.ErrorResponse{Error: message})
}

// handleRoutes handles /admin/routes endpoints.
func (h *Handler) handleNetworks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	networks, err := h.containerSvc.ListNetworks(ctx)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to list networks")
		return
	}

	response := make([]dto.Network, 0, len(networks))
	for _, network := range networks {
		if network == nil {
			continue
		}
		response = append(response, dto.Network{
			Name:       network.Name,
			Driver:     network.Driver,
			Containers: append([]string{}, network.Containers...),
		})
	}

	h.sendJSON(w, http.StatusOK, dto.NetworksResponse{Networks: response})
}

// handleSecrets handles /admin/secrets endpoints.
func (h *Handler) handleSecrets(w http.ResponseWriter, r *http.Request, path string) {
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
		h.handleSecretsGet(w, r, secretDomain)
	case http.MethodPost:
		h.handleSecretsPost(w, r, secretDomain)
	case http.MethodDelete:
		h.handleSecretsDelete(w, r, secretDomain, secretKey)
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSecretsPost handles POST /admin/secrets/{domain} - set secrets.
func (h *Handler) handleSecretsPost(w http.ResponseWriter, r *http.Request, secretDomain string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)

	var data map[string]string
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		log.Warn().Err(err).Msg("invalid secrets JSON")
		h.sendError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.secretSvc.Set(ctx, secretDomain, data); err != nil {
		log.Error().Err(err).Str("domain", secretDomain).Msg("failed to set secrets")
		h.sendError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	log.Info().Str("domain", secretDomain).Int("count", len(data)).Msg("secrets set")
	h.sendJSON(w, http.StatusOK, dto.SecretsStatusResponse{Status: "updated"})
}

// handleSecretsDelete handles DELETE /admin/secrets/{domain}/{key} - delete a secret.
func (h *Handler) handleSecretsDelete(w http.ResponseWriter, r *http.Request, secretDomain, secretKey string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:write")
		return
	}
	if secretKey == "" {
		h.sendError(w, http.StatusBadRequest, "key required in path")
		return
	}

	if err := h.secretSvc.Delete(ctx, secretDomain, secretKey); err != nil {
		log.Error().Err(err).Str("domain", secretDomain).Str("key", secretKey).Msg("failed to delete secret")
		h.sendError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	log.Info().Str("domain", secretDomain).Str("key", secretKey).Msg("secret deleted")
	h.sendJSON(w, http.StatusOK, dto.SecretsStatusResponse{Status: "deleted"})
}

// handleSecretsGet handles GET /admin/secrets/{domain} - list secrets.
func (h *Handler) handleSecretsGet(w http.ResponseWriter, r *http.Request, secretDomain string) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceSecrets, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for secrets:read")
		return
	}

	// List secrets for domain (names only, not values).
	keys, err := h.secretSvc.ListKeys(ctx, secretDomain)
	if err != nil {
		log.Error().Err(err).Str("domain", secretDomain).Msg("failed to list secrets")
		h.sendError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	h.sendJSON(w, http.StatusOK, dto.SecretsListResponse{
		Domain: secretDomain,
		Keys:   keys,
	})
}

// handleHealth handles /admin/health endpoint.
// Returns detailed health status for all routes including HTTP probe results.
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	health := h.healthSvc.CheckAllRoutes(ctx)

	response := make(map[string]dto.HealthStatus, len(health))
	for domain, healthStatus := range health {
		if healthStatus == nil {
			continue
		}
		response[domain] = dto.HealthStatus{
			ContainerStatus: healthStatus.ContainerStatus,
			HTTPStatus:      healthStatus.HTTPStatus,
			ResponseTimeMs:  healthStatus.ResponseTimeMs,
			Healthy:         healthStatus.Healthy,
			Error:           healthStatus.Error,
		}
	}

	h.sendJSON(w, http.StatusOK, dto.HealthResponse{Health: response})
}

// handleStatus handles /admin/status endpoint.
// Reports installation identity plus the app fleet summary from
// desired/active state (no container inspection). Per-service detail
// lives under `apps show APP`.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	// App fleet summary from desired/active state.
	statuses := make(map[string]string)
	if h.appSvc != nil {
		if apps, err := h.appSvc.List(ctx); err == nil {
			for _, app := range apps {
				statuses[app.App] = appStatusLabel(app)
			}
		}
	}

	status := dto.StatusResponse{
		Apps:              len(statuses),
		RegistryDomain:    h.configSvc.GetRegistryDomain(),
		RegistryPort:      h.configSvc.GetRegistryPort(),
		ServerPort:        h.configSvc.GetServerPort(),
		NetworkIsolation:  h.configSvc.IsNetworkIsolationEnabled(),
		ContainerStatuses: statuses,
	}

	h.sendJSON(w, http.StatusOK, status)
}

// appStatusLabel renders one app's fleet status from its summary.
func appStatusLabel(app in.AppSummary) string {
	if app.Stopped {
		return "stopped"
	}
	if app.Active == "" {
		return "pending"
	}
	if app.Converged {
		return "active"
	}
	return "deploying"
}

// handleBackups handles /admin/backups endpoints.
func (h *Handler) handleBackups(w http.ResponseWriter, r *http.Request, path string) {
	path = strings.TrimSuffix(path, "/")
	if path == "/backups/volumes" || strings.HasPrefix(path, "/backups/volumes/") {
		h.handleVolumeBackups(w, r, path)
		return
	}

	if h.backupSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "backup service not available")
		return
	}

	if path == "/backups" || path == "/backups/status" {
		h.handleBackupsStatus(w, r)
		return
	}

	suffix := strings.TrimPrefix(path, "/backups/")
	parts := strings.Split(suffix, "/")
	if len(parts) == 0 || parts[0] == "" {
		h.sendError(w, http.StatusBadRequest, "app required in path")
		return
	}

	backupDomain := parts[0]
	if len(parts) == 1 {
		h.handleBackupsApp(w, r, backupDomain)
		return
	}

	h.sendError(w, http.StatusNotFound, "route not found")
}

func (h *Handler) handleVolumeBackups(w http.ResponseWriter, r *http.Request, path string) {
	if h.volumeBackupSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "volume backup service not available")
		return
	}
	if path == "/backups/volumes/status" {
		h.handleVolumeBackupsStatus(w, r)
		return
	}
	if path == "/backups/volumes" {
		h.handleVolumeBackupsDomain(w, r, "")
		return
	}
	suffix := strings.TrimPrefix(path, "/backups/volumes/")
	parts := strings.Split(suffix, "/")
	if len(parts) != 1 || parts[0] == "" {
		h.sendError(w, http.StatusNotFound, "route not found")
		return
	}
	h.handleVolumeBackupsDomain(w, r, parts[0])
}

func (h *Handler) handleVolumeBackupsStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}
	jobs, err := h.volumeBackupSvc.VolumeBackupStatus(ctx)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to get volume backup status")
		return
	}
	h.sendJSON(w, http.StatusOK, dto.VolumeBackupsResponse{Backups: mapVolumeBackupJobsResponse(jobs)})
}

func (h *Handler) handleVolumeBackupsDomain(w http.ResponseWriter, r *http.Request, backupDomain string) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
			return
		}
		jobs, err := h.volumeBackupSvc.ListVolumeBackups(ctx, backupDomain)
		if err != nil {
			h.sendError(w, http.StatusInternalServerError, "failed to list volume backups")
			return
		}
		h.sendJSON(w, http.StatusOK, dto.VolumeBackupsResponse{Backups: mapVolumeBackupJobsResponse(jobs)})
	case http.MethodPost:
		if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite) {
			h.sendError(w, http.StatusForbidden, "insufficient permissions for config:write")
			return
		}
		var req dto.VolumeBackupRunRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			h.sendError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		jobs, err := h.volumeBackupSvc.RunVolumeBackups(ctx, backupDomain, req.Service, req.Volume)
		backups := mapVolumeBackupJobsResponse(jobs)
		if err != nil {
			if len(backups) > 0 {
				h.sendJSON(w, http.StatusPartialContent, dto.VolumeBackupRunResponse{Status: "partial", Backups: backups, Error: err.Error()})
				return
			}
			h.sendError(w, http.StatusInternalServerError, "failed to run volume backups")
			return
		}
		h.sendJSON(w, http.StatusOK, dto.VolumeBackupRunResponse{Status: "ok", Backups: backups})
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleBackupsStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	jobs, err := h.backupSvc.Status(ctx)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to get backup status")
		return
	}
	h.sendJSON(w, http.StatusOK, dto.BackupsResponse{Backups: mapBackupJobsResponse(jobs)})
}

func (h *Handler) handleBackupsApp(w http.ResponseWriter, r *http.Request, app string) {
	switch r.Method {
	case http.MethodGet:
		h.handleBackupsAppList(w, r, app)
	case http.MethodPost:
		h.handleBackupsAppRun(w, r, app)
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleBackupsAppList(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	jobs, err := h.backupSvc.ListBackups(ctx, app)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to list backups")
		return
	}

	h.sendJSON(w, http.StatusOK, dto.BackupsResponse{Backups: mapBackupJobsResponse(jobs)})
}

func (h *Handler) handleBackupsAppRun(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:write")
		return
	}

	var req dto.BackupRunRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		h.sendError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	result, err := h.backupSvc.RunBackup(ctx, app, req.Service, req.Database)
	if err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Error().Err(err).Str("app", app).Msg("backup run failed")
		h.sendError(w, http.StatusInternalServerError, "failed to run backup")
		return
	}
	log := zerowrap.FromCtx(ctx)
	log.Info().Str("app", app).Str("service", req.Service).Str("database", req.Database).
		Str("job_id", result.Job.ID).Msg("backup completed via admin API")

	job := toBackupJobResponse(result.Job)
	h.sendJSON(w, http.StatusOK, dto.BackupRunResponse{
		Status: "completed",
		Backup: &job,
	})
}

func mapBackupJobsResponse(jobs []domain.BackupJob) []dto.BackupJob {
	response := make([]dto.BackupJob, 0, len(jobs))
	for _, job := range jobs {
		response = append(response, toBackupJobResponse(job))
	}
	return response
}

func mapVolumeBackupJobsResponse(jobs []domain.VolumeBackupJob) []dto.VolumeBackupJob {
	response := make([]dto.VolumeBackupJob, 0, len(jobs))
	for _, job := range jobs {
		response = append(response, toVolumeBackupJobResponse(job))
	}
	return response
}

// handleReload handles /admin/reload endpoint.
// This reloads configuration from file into memory and triggers container sync.
func (h *Handler) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check write permission (reload modifies state)
	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:write")
		return
	}

	if h.reloadTrigger == nil {
		log.Error().Msg("reload trigger unavailable")
		h.sendError(w, http.StatusInternalServerError, "reload trigger unavailable")
		return
	}

	reloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.reloadTrigger.Trigger(reloadCtx); err != nil {
		log.Error().Err(err).Msg("failed to reload config")
		h.sendError(w, http.StatusInternalServerError, "failed to reload config")
		return
	}

	log.Info().Msg("config reloaded via admin API")
	h.sendJSON(w, http.StatusOK, dto.ReloadResponse{Status: "reloaded"})
}

// handleConfig handles /admin/config endpoint.
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceConfig, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for config:read")
		return
	}

	externalRoutes := h.configSvc.GetExternalRoutes()
	externalResponses := make([]dto.ExternalRoute, 0, len(externalRoutes))
	for domain := range externalRoutes {
		externalResponses = append(externalResponses, dto.ExternalRoute{
			Domain: domain,
		})
	}

	config := dto.ConfigResponse{
		Server: dto.ServerConfig{
			Port:           h.configSvc.GetServerPort(),
			RegistryPort:   h.configSvc.GetRegistryPort(),
			RegistryDomain: h.configSvc.GetRegistryDomain(),
		},
		NetworkIsolation: dto.NetworkIsolationConfig{
			Enabled: h.configSvc.IsNetworkIsolationEnabled(),
			Prefix:  h.configSvc.GetNetworkPrefix(),
		},
		ExternalRoutes: externalResponses,
	}
	if volumeCfg, ok := any(h.configSvc).(interface{ GetVolumeConfig() (bool, string, bool) }); ok {
		config.Volumes.AutoCreate, config.Volumes.Prefix, config.Volumes.Preserve = volumeCfg.GetVolumeConfig()
	}

	h.sendJSON(w, http.StatusOK, config)
}

// handleDeploy handles /admin/deploy/:domain endpoint.
// POST triggers a deployment for the specified domain.
func (h *Handler) handleTags(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	rawRepository := strings.TrimPrefix(path, "/tags/")
	if rawRepository == "" || rawRepository == "/tags" {
		h.sendError(w, http.StatusBadRequest, "repository name required in path")
		return
	}
	repository, err := url.PathUnescape(rawRepository)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, "invalid repository name encoding")
		return
	}
	if err := validation.ValidateRepositoryName(repository); err != nil {
		h.sendError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	if h.registrySvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "registry service not available")
		return
	}

	tags, err := h.registrySvc.ListTags(ctx, repository)
	if err != nil {
		if errors.Is(err, registry.ErrRepositoryNotFound) {
			h.sendError(w, http.StatusNotFound, "repository not found")
			return
		}
		h.sendError(w, http.StatusInternalServerError, "failed to list tags")
		return
	}

	h.sendJSON(w, http.StatusOK, dto.RepositoryTagsResponse{
		Repository: repository,
		Tags:       tags,
	})
}

// handleLogs handles /admin/logs endpoints.
// GET /admin/logs - Gordon process logs
// GET /admin/logs/:domain - Container logs for a specific domain
func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check read permission
	if !HasAccess(ctx, domain.AdminResourceLogs, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for logs:read")
		return
	}

	// Check if LogService is available
	if h.logSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "log service not available")
		return
	}

	// Parse query parameters
	lines := 50 // default
	if linesStr := r.URL.Query().Get("lines"); linesStr != "" {
		if n, err := strconv.Atoi(linesStr); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}
	follow := r.URL.Query().Get("follow") == "true"

	// Parse domain from path
	logDomain := strings.TrimPrefix(path, "/logs/")
	if logDomain == "/logs" {
		logDomain = ""
	}

	if logDomain != "" {
		parts := strings.Split(logDomain, "/")
		if len(parts) != 2 || validation.ValidateDomainParam(parts[0]) != nil || validation.ValidateDomainParam(parts[1]) != nil {
			h.sendError(w, http.StatusBadRequest, "invalid app/service reference")
			return
		}
	}

	if logDomain == "" {
		// Gordon process logs
		h.handleProcessLogs(w, r, lines, follow)
	} else {
		// Container logs
		h.handleContainerLogs(w, r, logDomain, lines, follow)
	}

	// Prevent unused variable warning when follow is implemented
	_ = log
}

// handleProcessLogs handles Gordon process logs.
func (h *Handler) handleProcessLogs(w http.ResponseWriter, r *http.Request, lines int, follow bool) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	if follow {
		// SSE streaming
		h.streamProcessLogs(w, r, lines)
		return
	}

	// Return last N lines as JSON
	logLines, err := h.logSvc.GetProcessLogs(ctx, lines)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get process logs")
		h.sendError(w, http.StatusInternalServerError, "failed to get logs")
		return
	}

	h.sendJSON(w, http.StatusOK, dto.ProcessLogsResponse{Lines: domain.RedactSecretLines(logLines)})
}

// handleContainerLogs handles container logs for a specific domain.
func (h *Handler) handleContainerLogs(w http.ResponseWriter, r *http.Request, logDomain string, lines int, follow bool) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	if follow {
		// SSE streaming
		h.streamContainerLogs(w, r, logDomain, lines)
		return
	}

	// Return last N lines as JSON
	logLines, err := h.logSvc.GetContainerLogs(ctx, logDomain, lines)
	if err != nil {
		log.Warn().Err(err).Str("domain", logDomain).Msg("failed to get container logs")
		h.sendError(w, http.StatusInternalServerError, "failed to get container logs")
		return
	}

	h.sendJSON(w, http.StatusOK, dto.ContainerLogsResponse{
		Domain: logDomain,
		Lines:  domain.RedactSecretLines(logLines),
	})
}

// streamProcessLogs streams Gordon process logs via SSE.
func (h *Handler) streamProcessLogs(w http.ResponseWriter, r *http.Request, lines int) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check for flusher support before setting up SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.sendError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, err := h.logSvc.FollowProcessLogs(ctx, lines)
	if err != nil {
		log.Warn().Err(err).Msg("failed to follow process logs")
		_, _ = fmt.Fprintf(w, "event: error\ndata: failed to stream logs\n\n")
		flusher.Flush()
		return
	}

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			line = domain.RedactSecrets(line)
			// SECURITY: Normalize line endings and escape newlines in log lines to prevent SSE event injection.
			// First normalize CRLF (\r\n) and CR (\r) to LF (\n), then escape newlines.
			// Per the SSE spec, multi-line data must use separate "data:" prefixes.
			normalized := strings.ReplaceAll(line, "\r\n", "\n")
			normalized = strings.ReplaceAll(normalized, "\r", "\n")
			escaped := strings.ReplaceAll(normalized, "\n", "\ndata: ")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", escaped)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// streamContainerLogs streams container logs via SSE.
func (h *Handler) streamContainerLogs(w http.ResponseWriter, r *http.Request, logDomain string, lines int) {
	ctx := r.Context()
	log := zerowrap.FromCtx(ctx)

	// Check for flusher support before setting up SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.sendError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, err := h.logSvc.FollowContainerLogs(ctx, logDomain, lines)
	if err != nil {
		log.Warn().Err(err).Str("domain", logDomain).Msg("failed to follow container logs")
		_, _ = fmt.Fprintf(w, "event: error\ndata: failed to stream container logs\n\n")
		flusher.Flush()
		return
	}

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			line = domain.RedactSecrets(line)
			// SECURITY: Normalize line endings and escape newlines in log lines to prevent SSE event injection.
			// First normalize CRLF (\r\n) and CR (\r) to LF (\n), then escape newlines.
			// Per the SSE spec, multi-line data must use separate "data:" prefixes.
			normalized := strings.ReplaceAll(line, "\r\n", "\n")
			normalized = strings.ReplaceAll(normalized, "\r", "\n")
			escaped := strings.ReplaceAll(normalized, "\n", "\ndata: ")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", escaped)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// handleAuthVerify handles /admin/auth/verify.
func (h *Handler) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Only allow GET method
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Call use case to get auth status
	status, err := h.authSvc.GetAuthStatus(ctx)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "failed to get auth status")
		return
	}

	// Convert domain.AuthStatus to DTO
	response := dto.AuthVerifyResponse{
		Valid:     status.Valid,
		Subject:   status.Subject,
		Scopes:    status.Scopes,
		ExpiresAt: status.ExpiresAt,
		IssuedAt:  status.IssuedAt,
	}

	h.sendJSON(w, http.StatusOK, response)
}

// handleTLSStatus handles GET /admin/tls/status.
func (h *Handler) handleTLSStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	if !HasAccess(ctx, domain.AdminResourceStatus, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for status:read")
		return
	}

	if h.publicTLSSvc == nil {
		h.sendJSON(w, http.StatusOK, dto.TLSStatusResponse{
			ACMEEnabled:     false,
			SelectionReason: "public TLS service not configured",
		})
		return
	}

	status := h.publicTLSSvc.Status(ctx)
	h.sendJSON(w, http.StatusOK, dto.TLSStatusFromDomain(status))
}
