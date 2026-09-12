package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/appmanifest"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

// handleApps dispatches /admin/apps/* reads and lifecycle mutations.
func (h *Handler) handleApps(w http.ResponseWriter, r *http.Request, path string) {
	if path == "/apps/apply" {
		h.handleAppApply(w, r)
		return
	}
	if app, key, ok := splitOpLookup(path); ok {
		h.handleAppOpLookup(w, r, app, key)
		return
	}
	rest := strings.TrimPrefix(path, "/apps")
	if rest == "" || rest == "/" {
		if r.Method != http.MethodGet {
			h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleAppList(w, r)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if parts[0] == "" {
		h.sendError(w, http.StatusBadRequest, "app required in path")
		return
	}
	h.dispatchAppSubroute(w, r, parts)
}

// splitOpLookup parses /apps/{app}/operations/by-key/{key}.
func splitOpLookup(path string) (string, string, bool) {
	rest, ok := strings.CutPrefix(path, "/apps/")
	if !ok || !strings.Contains(rest, "/operations/by-key/") {
		return "", "", false
	}
	app, key, _ := strings.Cut(rest, "/")
	key, ok = strings.CutPrefix(key, "operations/by-key/")
	if !ok || app == "" || key == "" || strings.Contains(key, "/") {
		return "", "", false
	}
	return app, key, true
}

// dispatchAppSubroute routes one app's subpaths by method + shape.
func (h *Handler) dispatchAppSubroute(w http.ResponseWriter, r *http.Request, parts []string) {
	app := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleAppShow(w, r, app)
		return
	}
	if len(parts) == 2 {
		h.dispatchAppVerb(w, r, app, parts[1])
		return
	}
	if len(parts) == 3 && parts[1] == "secrets" {
		h.dispatchAppSecrets(w, r, app, parts[2])
		return
	}
	h.sendError(w, http.StatusNotFound, "route not found")
}

// dispatchAppVerb routes single-segment verbs (diff/deploy/stop/...).
func (h *Handler) dispatchAppVerb(w http.ResponseWriter, r *http.Request, app, verb string) {
	switch {
	case verb == "diff" && r.Method == http.MethodGet:
		h.handleAppDiff(w, r, app)
	case verb == "deploy" && r.Method == http.MethodPost:
		h.handleAppDeploy(w, r, app)
	case verb == "restart" && r.Method == http.MethodPost:
		h.handleAppRestart(w, r, app)
	case (verb == "stop" || verb == "start" || verb == "remove") && r.Method == http.MethodPost:
		h.handleAppLifecycle(w, r, app, verb)
	default:
		h.sendError(w, http.StatusNotFound, "route not found")
	}
}

// dispatchAppSecrets routes the secrets set/delete pair.
func (h *Handler) dispatchAppSecrets(w http.ResponseWriter, r *http.Request, app, action string) {
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch action {
	case "set":
		h.handleAppSecretsSet(w, r, app)
	case "delete":
		h.handleAppSecretsDelete(w, r, app)
	default:
		h.sendError(w, http.StatusNotFound, "route not found")
	}
}

// appService returns the port or 503 when the daemon engine is unwired.
func (h *Handler) appService(w http.ResponseWriter) (in.AppService, bool) {
	if h.appSvc == nil {
		h.sendError(w, http.StatusServiceUnavailable, "app engine not available")
		return nil, false
	}
	return h.appSvc, true
}

// handleAppApply validates a manifest and persists desired state.
func (h *Handler) handleAppApply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:write")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)
	var req dto.AppApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendAppError(w, http.StatusBadRequest, "invalid-request", "invalid JSON", "", "")
		return
	}
	if strings.TrimSpace(req.ManifestTOML) == "" {
		h.sendAppError(w, http.StatusBadRequest, "invalid-manifest", "manifest_toml is required", "", "")
		return
	}
	spec, _, err := appmanifest.Parse([]byte(req.ManifestTOML), "")
	if err != nil {
		h.sendAppError(w, http.StatusBadRequest, "invalid-manifest", err.Error(), "", "")
		return
	}
	applyResult, dryResult, err := svc.Apply(ctx, spec, []byte(req.ManifestTOML), req.DryRun)
	if err != nil {
		h.sendAppOpError(w, err)
		return
	}
	if dryResult != nil {
		h.sendJSON(w, http.StatusOK, dto.AppApplyResponse{
			App:  spec.Name,
			Diff: toAppDiffSection(dryResult.Diff),
		})
		return
	}
	h.sendJSON(w, http.StatusOK, dto.AppApplyResponse{
		App:               applyResult.App,
		FormerRevision:    applyResult.FormerRevision,
		ResultingRevision: applyResult.ResultingRevision,
		Pending:           applyResult.Pending,
		Noop:              applyResult.Noop,
		Diff:              toAppDiffSection(applyResult.Diff),
		Intent:            applyResult.IntentID,
	})
}

// handleAppList returns one summary per known app.
func (h *Handler) handleAppList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:read")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	summaries, err := svc.List(ctx)
	if err != nil {
		h.sendAppOpError(w, err)
		return
	}
	items := make([]dto.AppSummaryDTO, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, dto.AppSummaryDTO{
			App: s.App, Desired: s.Desired, DesiredStatus: s.DesiredStatus,
			Active: s.Active, Converged: s.Converged, Pending: s.Pending,
			Stopped: s.Stopped, LastOutcome: s.LastOutcome,
		})
	}
	if items == nil {
		items = []dto.AppSummaryDTO{}
	}
	h.sendJSON(w, http.StatusOK, map[string]any{"apps": items})
}

// handleAppShow inspects desired + active + intent.
func (h *Handler) handleAppShow(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:read")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	detail, err := svc.Show(ctx, app)
	if err != nil {
		h.sendAppOpError(w, err)
		return
	}
	services := map[string]dto.AppActiveServiceDTO{}
	for name, view := range detail.Services {
		services[name] = dto.AppActiveServiceDTO{
			EffectiveRevision: view.EffectiveRevision,
			Digest:            view.Digest,
			Container:         view.Container,
			RestartUnsafe:     view.RestartUnsafe,
		}
	}
	resp := dto.AppShowResponse{
		App:     detail.App,
		Desired: dto.AppDesiredDTO{Revision: detail.DesiredRevision, Status: detail.DesiredStatus, Pending: detail.Pending},
		Active: dto.AppActiveDTO{
			Converged:         detail.Converged,
			ConvergedRevision: detail.ConvergedRevision,
			Services:          services,
		},
		Intent: dto.AppIntentDTO{Stopped: detail.Stopped},
		Retained: dto.AppRetainedDTO{
			Volumes: detail.Retained.Volumes,
			Secrets: detail.Retained.Secrets,
			Images:  detail.Retained.Images,
		},
	}
	if detail.LastOp != "" {
		resp.LastOp = &dto.AppLastOpDTO{
			Op: detail.LastOp, Kind: detail.LastOpKind,
			Outcome: detail.LastOutcome, StartedAt: detail.LastOpStartedAt,
		}
	}
	h.sendJSON(w, http.StatusOK, resp)
}

// handleAppDiff returns the normalized desired-vs-active diff.
func (h *Handler) handleAppDiff(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:read")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	diff, err := svc.Diff(ctx, app)
	if err != nil {
		h.sendAppOpError(w, err)
		return
	}
	h.sendJSON(w, http.StatusOK, dto.AppDiffResponse{App: app, Diff: toAppDiffSection(diff)})
}

// handleAppDeploy activates a captured revision.
func (h *Handler) handleAppDeploy(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:write")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)
	var req dto.AppDeployRequest
	if r.Body != nil {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			h.sendAppError(w, http.StatusBadRequest, "invalid-request", "unreadable body", "", "")
			return
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req); err != nil {
				h.sendAppError(w, http.StatusBadRequest, "invalid-request", "invalid JSON", "", "")
				return
			}
		}
	}
	key, ok := appIdempotencyKey(w, r)
	if !ok {
		return
	}
	op, err := svc.Deploy(ctx, app, req.Revision, req.Service, key)
	if err != nil && (op == nil || isMappedPreflightError(err, op)) {
		h.sendAppOpError(w, err)
		return
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusConflict
	}
	h.sendJSON(w, status, h.appMutationResponse(ctx, svc, app, op, false))
}

// handleAppLifecycle runs stop/start/remove verbs.
func (h *Handler) handleAppLifecycle(w http.ResponseWriter, r *http.Request, app, verb string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:write")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	key, ok := appIdempotencyKey(w, r)
	if !ok {
		return
	}
	var domainOp *domain.AppOperation
	var err error
	switch verb {
	case "stop":
		domainOp, err = svc.Stop(ctx, app, key)
	case "start":
		domainOp, err = svc.Start(ctx, app, key)
	case "remove":
		domainOp, err = svc.Remove(ctx, app, key)
	default:
		h.sendError(w, http.StatusNotFound, "route not found")
		return
	}
	if err != nil && domainOp == nil {
		h.sendAppOpError(w, err)
		return
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusConflict
	}
	h.sendJSON(w, status, h.appMutationResponse(ctx, svc, app, domainOp, false))
}

// handleAppRestart restarts from pinned digests.
func (h *Handler) handleAppRestart(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:write")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	key, ok := appIdempotencyKey(w, r)
	if !ok {
		return
	}
	service := r.URL.Query().Get("service")
	op, err := svc.Restart(ctx, app, service, key)
	if err != nil && op == nil {
		h.sendAppOpError(w, err)
		return
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusConflict
	}
	h.sendJSON(w, status, h.appMutationResponse(ctx, svc, app, op, false))
}

func appIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		http.Error(w, "valid Idempotency-Key header required", http.StatusBadRequest)
		return "", false
	}
	for _, char := range key {
		if char < '!' || char > '~' {
			http.Error(w, "valid Idempotency-Key header required", http.StatusBadRequest)
			return "", false
		}
	}
	return key, true
}

// handleAppOpLookup serves GET /apps/{app}/operations/{key}.
func (h *Handler) handleAppOpLookup(w http.ResponseWriter, r *http.Request, app, key string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:read")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	op, err := svc.OperationByKey(ctx, app, key)
	if err != nil {
		h.sendAppOpError(w, err)
		return
	}
	// Application diagnostics require the logs read scope; an apps-read
	// actor receives the stable error only. The lookup answers with the
	// journal alone: no app read is added to a journal query.
	includeDiagnostics := HasAccess(ctx, domain.AdminResourceLogs, domain.AdminActionRead)
	h.sendJSON(w, http.StatusOK, toAppDeployResponse(app, op, includeDiagnostics))
}

// handleAppSecretsSet writes secret values for pre-registered names.
func (h *Handler) handleAppSecretsSet(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:write")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)
	var req dto.AppSecretSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendAppError(w, http.StatusBadRequest, "invalid-request", "invalid JSON", "", "")
		return
	}
	if err := svc.SetSecrets(ctx, app, req.Service, req.Secrets); err != nil {
		h.sendAppOpError(w, err)
		return
	}
	h.sendJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleAppSecretsDelete removes one secret value (refused when referenced).
func (h *Handler) handleAppSecretsDelete(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:write")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestSize)
	var req dto.AppSecretDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendAppError(w, http.StatusBadRequest, "invalid-request", "invalid JSON", "", "")
		return
	}
	if err := svc.DeleteSecret(ctx, app, req.Service, req.Key); err != nil {
		h.sendAppOpError(w, err)
		return
	}
	h.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// sendAppError writes the v2.50 error envelope (never carries logs).
func (h *Handler) sendAppError(w http.ResponseWriter, status int, code, message, cause, hint string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dto.AppError{Error: code, Message: message, Cause: cause, Hint: hint})
}

func isMappedPreflightError(err error, op *domain.AppOperation) bool {
	for _, step := range op.Steps {
		if strings.HasPrefix(step.ID, "service.") {
			return false
		}
	}
	return errors.Is(err, domain.ErrInvalidAppSpec) ||
		errors.Is(err, domain.ErrAppNotFound) ||
		errors.Is(err, domain.ErrAppReservationConflict) ||
		errors.Is(err, domain.ErrAppImageUnresolvable) ||
		errors.Is(err, domain.ErrAppSecretMissing) ||
		errors.Is(err, domain.ErrAppUnmanagedImageVolume) ||
		errors.Is(err, domain.ErrAppRevisionNotFound) ||
		errors.Is(err, domain.ErrAppIntentNotFound) ||
		errors.Is(err, domain.ErrAppOperationNotFound) ||
		errors.Is(err, domain.ErrAppStateConflict)
}

// sendAppOpError maps domain errors to status + envelope.
func (h *Handler) sendAppOpError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidAppSpec):
		h.sendAppError(w, http.StatusBadRequest, "invalid-manifest", err.Error(), "", "")
	case errors.Is(err, domain.ErrAppReservationConflict):
		h.sendAppError(w, http.StatusConflict, "reservation-conflict", err.Error(), "", "")
	case errors.Is(err, domain.ErrAppImageUnresolvable):
		h.sendAppError(w, http.StatusBadRequest, "image-unresolvable", err.Error(), "", "")
	case errors.Is(err, domain.ErrAppSecretMissing):
		h.sendAppError(w, http.StatusBadRequest, "secret-missing", err.Error(), "", "set the secret, then redeploy")
	case errors.Is(err, domain.ErrAppUnmanagedImageVolume):
		h.sendAppError(w, http.StatusBadRequest, "unmanaged-image-volume", err.Error(), "", "")
	case errors.Is(err, domain.ErrAppNotFound):
		h.sendAppError(w, http.StatusNotFound, "app-not-found", err.Error(), "", "check the app name")
	case errors.Is(err, domain.ErrAppRevisionNotFound),
		errors.Is(err, domain.ErrAppIntentNotFound),
		errors.Is(err, domain.ErrAppOperationNotFound):
		h.sendAppError(w, http.StatusNotFound, "not-found", err.Error(), "", "")
	case errors.Is(err, domain.ErrAppStateConflict):
		h.sendAppError(w, http.StatusConflict, "state-conflict", err.Error(), "", "")
	default:
		h.sendAppError(w, http.StatusInternalServerError, "internal", "app operation failed", "", "")
	}
}

// toAppDiffSection maps the domain diff to its DTO shape.
func toAppDiffSection(diff domain.AppDiff) dto.AppDiffSection {
	return dto.AppDiffSection{Added: diff.Added, Removed: diff.Removed, Changed: diff.Changed}
}

// toAppDeployResponse maps a journaled operation to its DTO shape.
// toAppDeployResponse maps an operation journal record to the wire DTO.
// includeDiagnostics must be true only for callers holding the logs read
// scope: application output is never returned on a mutation response.
func toAppDeployResponse(app string, op *domain.AppOperation, includeDiagnostics bool) dto.AppDeployResponse {
	resp := dto.AppDeployResponse{App: app}
	if op == nil {
		return resp
	}
	resp.Op = op.Op
	resp.Revision = op.InputRevision
	resp.Outcome = op.Outcome
	resp.Services = map[string]dto.AppServiceResultDTO{}
	for _, step := range op.Steps {
		service, ok := strings.CutPrefix(step.ID, "service.")
		if !ok {
			continue
		}
		service, _, _ = strings.Cut(service, ".")
		entry := resp.Services[service]
		switch step.State {
		case domain.AppStepSucceeded:
			entry.Result = "deployed"
		case domain.AppStepFailed:
			entry.Result = "failed"
			entry.Error = step.Error
		default:
			entry.Result = "pending"
		}
		entry.Before = step.Before
		entry.After = step.After
		if entry.EffectiveRevision == "" {
			entry.EffectiveRevision = op.InputRevision
		}
		resp.Services[service] = entry
	}
	for _, warning := range op.Warnings {
		resp.CleanupWarnings = append(resp.CleanupWarnings, dto.AppCleanupWarningDTO{
			Service: warning.Service, Leftover: warning.Leftover, Detail: warning.Detail,
		})
	}
	resp.Steps = make([]dto.AppStepDTO, 0, len(op.Steps))
	for _, step := range op.Steps {
		entry := dto.AppStepDTO{
			ID: step.ID, State: step.State, Detail: step.Detail,
			Error: step.Error, Before: step.Before, After: step.After,
		}
		if includeDiagnostics {
			entry.Diagnostics = step.Diagnostics
		}
		resp.Steps = append(resp.Steps, entry)
	}
	return resp
}

// appMutationResponse maps one operation journal to the wire DTO and
// attaches current app state. Effective revisions, convergence, and owned
// resources come from desired + ACTIVE + ownership, never from the
// journal. An app that no longer exists (removal) omits both sections.
func (h *Handler) appMutationResponse(ctx context.Context, svc in.AppService, app string, op *domain.AppOperation, includeDiagnostics bool) dto.AppDeployResponse {
	resp := toAppDeployResponse(app, op, includeDiagnostics)
	detail, err := svc.Show(ctx, app)
	if err != nil {
		return resp
	}
	effective := &dto.AppEffectiveDTO{
		Converged:         detail.Converged,
		ConvergedRevision: detail.ConvergedRevision,
		Services:          map[string]string{},
	}
	for name, view := range detail.Services {
		effective.Services[name] = view.EffectiveRevision
	}
	resp.Effective = effective
	resp.Retained = &dto.AppRetainedDTO{
		Volumes: detail.Retained.Volumes,
		Secrets: detail.Retained.Secrets,
		Images:  detail.Retained.Images,
	}
	return resp
}
