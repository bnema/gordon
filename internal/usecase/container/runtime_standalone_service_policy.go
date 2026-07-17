package container

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const runtimeStandaloneServicePolicyDeniedEventLimit = 256

// RuntimeStandaloneServicePolicyManager enforces runtime policy immediately before standalone
// service operations cross to the runtime manager.
type RuntimeStandaloneServicePolicyManager struct {
	inner  out.RuntimeStandaloneServiceManager
	policy RuntimePolicy
	now    func() time.Time

	mu                 sync.Mutex
	policyDeniedEvents []domain.RuntimePolicyDeniedEvent
}

// NewRuntimeStandaloneServicePolicyManager wraps a standalone-service runtime manager with policy checks.
func NewRuntimeStandaloneServicePolicyManager(inner out.RuntimeStandaloneServiceManager, policy RuntimePolicy) *RuntimeStandaloneServicePolicyManager {
	return &RuntimeStandaloneServicePolicyManager{
		inner:  inner,
		policy: policy.normalize(),
		now:    time.Now,
	}
}

// PolicyDeniedEvents returns policy findings recorded in observe and enforce modes.
func (m *RuntimeStandaloneServicePolicyManager) PolicyDeniedEvents() []domain.RuntimePolicyDeniedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()

	events := make([]domain.RuntimePolicyDeniedEvent, len(m.policyDeniedEvents))
	copy(events, m.policyDeniedEvents)
	return events
}

// ApplyStandaloneService checks the image policy before passing the command to the runtime manager.
func (m *RuntimeStandaloneServicePolicyManager) ApplyStandaloneService(ctx context.Context, command domain.ApplyStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return m.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	if result, denied := m.policyResult(command.RuntimeCommandIdentity, m.policy.checkImage(command.Service.Image, command.RuntimeCommandIdentity, "")); denied {
		return result, nil
	}
	if m.inner == nil {
		return m.failedResult(command.RuntimeCommandIdentity, errors.New("runtime standalone service manager unavailable")), nil
	}
	return m.inner.ApplyStandaloneService(ctx, command)
}

// RemoveStandaloneService permits mutations only for a canonical, non-empty managed service name.
func (m *RuntimeStandaloneServicePolicyManager) RemoveStandaloneService(ctx context.Context, command domain.RemoveStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	if err := command.Validate(); err != nil {
		return m.failedResult(command.RuntimeCommandIdentity, err), nil
	}
	if result, denied := m.policyResult(command.RuntimeCommandIdentity, m.checkRemove(command)); denied {
		return result, nil
	}
	if m.inner == nil {
		return m.failedResult(command.RuntimeCommandIdentity, errors.New("runtime standalone service manager unavailable")), nil
	}
	return m.inner.RemoveStandaloneService(ctx, command)
}

// ListStandaloneServiceState is read-only and delegates directly to the runtime manager.
func (m *RuntimeStandaloneServicePolicyManager) ListStandaloneServiceState(ctx context.Context) ([]domain.RuntimeStandaloneServiceState, error) {
	if m.inner == nil {
		return nil, errors.New("runtime standalone service manager unavailable")
	}
	return m.inner.ListStandaloneServiceState(ctx)
}

func (m *RuntimeStandaloneServicePolicyManager) checkRemove(command domain.RemoveStandaloneServiceCommand) error {
	name := strings.TrimSpace(command.Name)
	if name == "" || name != command.Name {
		return m.policy.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "runtime command must target a managed standalone service")
	}
	return nil
}

func (m *RuntimeStandaloneServicePolicyManager) policyResult(identity domain.RuntimeCommandIdentity, policyErr error) (domain.RuntimeCommandResult, bool) {
	if policyErr == nil {
		return domain.RuntimeCommandResult{}, false
	}
	m.recordPolicyDenied(policyErr)
	if !m.policy.Enforced() {
		return domain.RuntimeCommandResult{}, false
	}
	return m.deniedResult(identity, policyErr), true
}

func (m *RuntimeStandaloneServicePolicyManager) recordPolicyDenied(err error) {
	event, ok := RuntimePolicyDeniedEventFromError(err, "")
	if !ok {
		return
	}
	event.OccurredAt = m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.policyDeniedEvents = append(m.policyDeniedEvents, event)
	if len(m.policyDeniedEvents) > runtimeStandaloneServicePolicyDeniedEventLimit {
		copy(m.policyDeniedEvents, m.policyDeniedEvents[len(m.policyDeniedEvents)-runtimeStandaloneServicePolicyDeniedEventLimit:])
		m.policyDeniedEvents = m.policyDeniedEvents[:runtimeStandaloneServicePolicyDeniedEventLimit]
	}
}

func (m *RuntimeStandaloneServicePolicyManager) deniedResult(identity domain.RuntimeCommandIdentity, err error) domain.RuntimeCommandResult {
	result := m.baseResult(identity)
	result.Status = domain.RuntimeCommandStatusDenied
	result.CompletedAt = result.StartedAt

	var denied RuntimePolicyDeniedError
	code := "runtime_policy_denied"
	if errors.As(err, &denied) {
		code = formatPolicyReason(denied.Reason)
	}
	result.Error = &domain.RuntimeCommandError{Code: code, Message: "runtime policy denied"}
	return result
}

func (m *RuntimeStandaloneServicePolicyManager) failedResult(identity domain.RuntimeCommandIdentity, err error) domain.RuntimeCommandResult {
	result := m.baseResult(identity)
	result.Status = domain.RuntimeCommandStatusFailed
	result.CompletedAt = result.StartedAt
	result.Error = sanitizeRuntimeCommandError(err)
	return result
}

func (m *RuntimeStandaloneServicePolicyManager) baseResult(identity domain.RuntimeCommandIdentity) domain.RuntimeCommandResult {
	return domain.RuntimeCommandResult{
		CommandID:      identity.ID,
		IdempotencyKey: identity.IdempotencyKey,
		Generation:     identity.Generation,
		Status:         domain.RuntimeCommandStatusRunning,
		StartedAt:      m.now(),
	}
}

var _ out.RuntimeStandaloneServiceManager = (*RuntimeStandaloneServicePolicyManager)(nil)
