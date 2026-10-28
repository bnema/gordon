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
	"github.com/bnema/gordon/internal/adapters/in/http/appapi"
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
		if parts[1] == "secrets" && r.Method == http.MethodGet {
			h.handleAppSecretsList(w, r, app)
		} else {
			h.dispatchAppVerb(w, r, app, parts[1])
		}
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
	case verb == "set-image" && r.Method == http.MethodPost:
		h.handleAppSetImage(w, r, app)
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
	applyResult, dryResult, err := svc.Apply(ctx, in.AppApplyInput{
		Spec:                spec,
		Source:              []byte(req.ManifestTOML),
		DryRun:              req.DryRun,
		AllowSetImageRevert: req.AllowSetImageRevert,
		ExpectedRevision:    req.ExpectedRevision,
	})
	if err != nil {
		h.sendAppApplyError(w, err)
		return
	}
	if dryResult != nil {
		h.sendJSON(w, http.StatusOK, dto.AppApplyResponse{
			App:             spec.Name,
			Diff:            toAppDiffSection(dryResult.Diff),
			SetImageReverts: toSetImageRevertDTOs(dryResult.SetImageReverts),
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
		SetImageReverts:   toSetImageRevertDTOs(applyResult.SetImageReverts),
	})
}

// sendAppApplyError answers a set-image revert refusal with 409 and the
// reverts plus the checked desired revision, so the client can confirm and
// retry against exactly that revision. Other errors map as usual.
func (h *Handler) sendAppApplyError(w http.ResponseWriter, err error) {
	var revert *domain.SetImageRevertError
	if !errors.As(err, &revert) {
		h.sendAppOpError(w, err)
		return
	}
	h.sendJSON(w, http.StatusConflict, dto.AppSetImageRevertConflict{
		Error:           dto.AppSetImageRevertCode,
		Message:         err.Error(),
		Hint:            "confirm with allow_set_image_revert, or update the manifest to the server image",
		DesiredRevision: revert.DesiredRevision,
		SetImageReverts: toSetImageRevertDTOs(revert.Reverts),
	})
}

// toSetImageRevertDTOs maps set-image reverts to their wire shape.
func toSetImageRevertDTOs(reverts []domain.AppSetImageRevert) []dto.AppSetImageRevertDTO {
	if len(reverts) == 0 {
		return nil
	}
	out := make([]dto.AppSetImageRevertDTO, 0, len(reverts))
	for _, r := range reverts {
		out = append(out, dto.AppSetImageRevertDTO{
			Service: r.Service, ServerImage: r.ServerImage, FileImage: r.FileImage,
			Revision: r.Revision, Principal: r.Principal, ChangedAt: r.ChangedAt,
		})
	}
	return out
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
			Resources:         view.Resources,
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
	op, err := svc.Deploy(ctx, app, req.Revision, req.Service, req.All, key)
	if err != nil && (op == nil || isMappedPreflightError(err, op)) {
		h.sendAppOpError(w, err)
		return
	}
	status := http.StatusOK
	switch {
	case err != nil:
		status = http.StatusConflict
	case op != nil && !op.Terminal():
		// Newly owned: the claim is durable and its effects run on the
		// daemon context, so answer 202 with the running operation and let
		// the client observe completion through operations/by-key. A
		// terminal replay instead answers 200 with the stored outcome.
		status = http.StatusAccepted
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
	query := r.URL.Query()
	all := query.Get("all") == "true"
	op, err := svc.Restart(ctx, app, query.Get("service"), all, key)
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

// handleAppSetImage serves POST /apps/{app}/set-image: run exactly one registry
// digest in one service. A stored-token caller sets the image as the gordon
// principal of its token subject and needs a set-image policy; the owner-only
// local socket sets the image as the local owner without one.
func (h *Handler) handleAppSetImage(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionWrite) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:write")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	req, err := appapi.DecodeSetImageRequest(w, r)
	if err != nil {
		h.sendAppOpError(w, err)
		return
	}
	key, ok := appIdempotencyKey(w, r)
	if !ok {
		return
	}
	principal, localOwner := adminPrincipal(ctx)
	op, err := svc.SetImage(ctx, appapi.SetImageInput(app, key, req, principal, localOwner))
	status, respond := appapi.SetImageOutcome(op, err)
	if !respond {
		h.sendAppOpError(w, err)
		return
	}
	h.sendJSON(w, status, h.appMutationResponse(ctx, svc, app, op, false))
}

// adminPrincipal returns the set-image identity of an admin caller and
// whether it is the local owner. Only LocalAuthority sets the local-owner
// marker; a token whose subject happens to equal LocalAuthoritySubject is
// still a token caller.
func adminPrincipal(ctx context.Context) (domain.WorkloadPrincipal, bool) {
	principal := domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: GetSubject(ctx)}
	return principal, isLocalOwner(ctx)
}

func appIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !appapi.ValidIdempotencyKey(key) {
		http.Error(w, "valid Idempotency-Key header required", http.StatusBadRequest)
		return "", false
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

func (h *Handler) handleAppSecretsList(w http.ResponseWriter, r *http.Request, app string) {
	ctx := r.Context()
	if !HasAccess(ctx, domain.AdminResourceApps, domain.AdminActionRead) {
		h.sendError(w, http.StatusForbidden, "insufficient permissions for apps:read")
		return
	}
	svc, ok := h.appService(w)
	if !ok {
		return
	}
	entries, err := svc.ListSecrets(ctx, app, r.URL.Query().Get("service"))
	if err != nil {
		h.sendAppOpError(w, err)
		return
	}
	result := make([]dto.AppSecretMetadataDTO, 0, len(entries))
	for _, entry := range entries {
		result = append(result, dto.AppSecretMetadataDTO{Service: entry.Service, Key: entry.Key, Name: entry.Name, Source: entry.Source, Presence: entry.Presence})
	}
	h.sendJSON(w, http.StatusOK, result)
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
		h.sendAppSecretWriteError(w, err)
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
		h.sendAppSecretWriteError(w, err)
		return
	}
	h.sendJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// sendAppSecretWriteError maps secret set/delete errors. A missing
// registration is not fixed by setting the secret, so the deploy-oriented
// hint of sendAppOpError is omitted; the message carries the next step.
func (h *Handler) sendAppSecretWriteError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrAppSecretMissing) {
		h.sendAppError(w, http.StatusBadRequest, "secret-missing", err.Error(), "", "")
		return
	}
	h.sendAppOpError(w, err)
}

// sendAppError writes the v3 error envelope (never carries logs).
func (h *Handler) sendAppError(w http.ResponseWriter, status int, code, message, cause, hint string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dto.AppError{Error: code, Message: message, Cause: cause, Hint: hint})
}

func isMappedPreflightError(err error, op *domain.AppOperation) bool {
	// A non-terminal journal is a running replay, never a preflight
	// failure: its stored operation is the response regardless of which
	// steps have started, so the mapped error envelope must not replace
	// it. Only an absent or terminal journal can map a preflight error.
	if op != nil && !op.Terminal() {
		return false
	}
	for _, step := range op.Steps {
		if strings.HasPrefix(step.ID, "service.") {
			return false
		}
	}
	return errors.Is(err, domain.ErrInvalidAppSpec) ||
		errors.Is(err, domain.ErrBindPolicy) ||
		errors.Is(err, domain.ErrDevicePolicy) ||
		errors.Is(err, domain.ErrCapabilityPolicy) ||
		isPreflightErrorRest(err)
}

// isPreflightErrorRest covers the remaining preflight-mappable sentinels.
// Split from isMappedPreflightError to keep its complexity within budget.
func isPreflightErrorRest(err error) bool {
	return errors.Is(err, domain.ErrRuntimeUnsupported) ||
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
	appapi.WriteError(w, err)
}

// toAppDiffSection maps the domain diff to its DTO shape.
func toAppDiffSection(diff domain.AppDiff) dto.AppDiffSection {
	return dto.AppDiffSection{Added: diff.Added, Removed: diff.Removed, Changed: diff.Changed}
}

// toAppDeployResponse maps an operation journal record to the wire DTO.
// includeDiagnostics must be true only for callers holding the logs read
// scope: application output is never returned on a mutation response.
func toAppDeployResponse(app string, op *domain.AppOperation, includeDiagnostics bool) dto.AppDeployResponse {
	return appapi.OperationResponse(app, op, includeDiagnostics)
}

// appMutationResponse maps one operation journal to the wire DTO and
// attaches current app state (see appapi.MutationResponse).
func (h *Handler) appMutationResponse(ctx context.Context, svc in.AppService, app string, op *domain.AppOperation, includeDiagnostics bool) dto.AppDeployResponse {
	return appapi.MutationResponse(ctx, svc, app, op, includeDiagnostics)
}
