package container

import (
	"context"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

const DefaultSecretsDebounce = 60 * time.Second

// SecretsChangedHandler handles secrets.changed events with per-domain
// debouncing. When secrets are modified rapidly (e.g. multiple
// "gordon secrets set" calls), the handler resets a timer on each
// event and only triggers a deploy once the domain is quiet for
// the configured delay.
type SecretsChangedHandler struct {
	containerSvc  in.ContainerService
	configSvc     in.ConfigService
	ctx           context.Context
	cancel        context.CancelFunc
	debounceDelay time.Duration

	mu          sync.Mutex
	timers      map[string]*time.Timer
	generations map[string]uint64
}

// NewSecretsChangedHandler creates a new SecretsChangedHandler.
func NewSecretsChangedHandler(
	ctx context.Context,
	containerSvc in.ContainerService,
	configSvc in.ConfigService,
	debounceDelay time.Duration,
) *SecretsChangedHandler {
	ctx, cancel := context.WithCancel(ctx)
	return &SecretsChangedHandler{
		containerSvc:  containerSvc,
		configSvc:     configSvc,
		ctx:           ctx,
		cancel:        cancel,
		debounceDelay: debounceDelay,
		timers:        make(map[string]*time.Timer),
		generations:   make(map[string]uint64),
	}
}

// Handle processes a secrets.changed event by resetting the debounce
// timer for the affected domain. Returns immediately - the deploy
// runs asynchronously when the timer fires.
//
// Uses the handler's long-lived context (h.ctx) rather than the event
// context because the debounced deploy fires after this method returns;
// using the event context would cause premature cancellation.
func (h *SecretsChangedHandler) Handle(_ context.Context, event domain.Event) error {
	payload, ok := event.Data.(domain.SecretsChangedPayload)
	if !ok {
		return nil
	}
	if payload.Domain == "" {
		return nil
	}

	ctx := zerowrap.CtxWithFields(h.ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldHandler: "SecretsChangedHandler",
		"domain":              payload.Domain,
		"operation":           payload.Operation,
	})
	log := zerowrap.FromCtx(ctx)

	log.Debug().
		Strs("keys", payload.Keys).
		Dur("debounce", h.debounceDelay).
		Msg("secrets changed, resetting debounce timer")

	h.resetTimer(ctx, payload.Domain)
	return nil
}

// CanHandle returns true for secrets.changed events.
func (h *SecretsChangedHandler) CanHandle(eventType domain.EventType) bool {
	return eventType == domain.EventSecretsChanged
}

// Stop cancels the handler context and all pending timers.
// Call before graceful shutdown to prevent deploys during drain.
func (h *SecretsChangedHandler) Stop() {
	h.cancel()

	h.mu.Lock()
	defer h.mu.Unlock()

	for domainName, timer := range h.timers {
		timer.Stop()
		delete(h.timers, domainName)
		delete(h.generations, domainName)
	}
}

// resetTimer replaces any existing timer for the domain with a new one.
func (h *SecretsChangedHandler) resetTimer(ctx context.Context, domainName string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing, ok := h.timers[domainName]; ok {
		existing.Stop()
	}

	h.generations[domainName]++
	gen := h.generations[domainName]

	h.timers[domainName] = time.AfterFunc(h.debounceDelay, func() {
		h.fireDeploy(ctx, domainName, gen)
	})
}

// fireDeploy looks up the route and triggers a deploy.
func (h *SecretsChangedHandler) fireDeploy(ctx context.Context, domainName string, generation uint64) {
	// Bail if the handler has been stopped (shutdown in progress).
	if h.ctx.Err() != nil {
		return
	}

	log := zerowrap.FromCtx(ctx)

	h.mu.Lock()
	if h.generations[domainName] != generation {
		h.mu.Unlock()
		return
	}
	delete(h.timers, domainName)
	delete(h.generations, domainName)
	h.mu.Unlock()

	route, err := h.configSvc.GetRoute(ctx, domainName)
	if err != nil {
		log.WrapErr(err, "failed to get route for secrets-changed deploy")
		return
	}
	if route == nil {
		log.Debug().Msg("no route configured for domain, skipping secrets-changed deploy")
		return
	}

	log.Info().Str("image", route.Image).Msg("debounce fired, deploying after secrets change")

	if _, err := h.containerSvc.Deploy(ctx, *route); err != nil {
		log.WrapErr(err, "secrets-changed deploy failed")
	}
}
