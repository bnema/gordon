package appapi

import (
	"context"
	"strings"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

// OperationResponse maps an operation journal record to the wire DTO.
// includeDiagnostics must be true only for callers allowed to read
// application output: it is never returned on a mutation response.
func OperationResponse(app string, op *domain.AppOperation, includeDiagnostics bool) dto.AppDeployResponse {
	resp := dto.AppDeployResponse{App: app}
	if op == nil {
		return resp
	}
	resp.Op = op.Op
	resp.Revision = op.InputRevision
	resp.Outcome = op.Outcome
	// A non-terminal operation is still running; a terminal one reports
	// its persisted outcome as the stable status.
	resp.Status = dto.AppStatusRunning
	if op.Terminal() {
		resp.Status = op.Outcome
	}
	resp.Services = serviceResults(op)
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

// serviceResults folds the per-service steps of a journal into results.
func serviceResults(op *domain.AppOperation) map[string]dto.AppServiceResultDTO {
	results := map[string]dto.AppServiceResultDTO{}
	for _, step := range op.Steps {
		service, ok := strings.CutPrefix(step.ID, "service.")
		if !ok {
			continue
		}
		service, action, _ := strings.Cut(service, ".")
		entry := results[service]
		switch step.State {
		case domain.AppStepSucceeded:
			entry.Result = "deployed"
			if action == "remove" {
				entry.Result = domain.AppServiceRemoved
			}
			if strings.HasPrefix(step.Detail, domain.AppServiceUnchanged+":") {
				entry.Result = domain.AppServiceUnchanged
			}
		case domain.AppStepFailed:
			entry.Result = "failed"
			entry.Error = step.Error
		default:
			entry.Result = "pending"
		}
		entry.Before = step.Before
		entry.After = step.After
		// A removed service has no effective revision: the input revision
		// no longer declares it.
		if entry.EffectiveRevision == "" && entry.Result != domain.AppServiceRemoved {
			entry.EffectiveRevision = op.InputRevision
		}
		results[service] = entry
	}
	return results
}

// MutationResponse maps one operation journal to the wire DTO and attaches
// current app state. Effective revisions, convergence, and owned resources
// come from desired + ACTIVE + ownership, never from the journal. An app
// that no longer exists (removal) omits both sections.
func MutationResponse(ctx context.Context, svc in.AppService, app string, op *domain.AppOperation, includeDiagnostics bool) dto.AppDeployResponse {
	resp := OperationResponse(app, op, includeDiagnostics)
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
