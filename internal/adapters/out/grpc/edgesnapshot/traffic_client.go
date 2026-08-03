package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	edgev1 "github.com/bnema/gordon/api/gordon/edge/v1"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
)

// ErrTrafficGraphUnavailable is returned until a valid graph has arrived.
var ErrTrafficGraphUnavailable = errors.New("traffic graph snapshot is unavailable")

// TrafficGraphHealth is the sanitized status of the independent graph stream.
type TrafficGraphHealth struct {
	Healthy                bool
	Connected              bool
	LastAcceptedGeneration domain.TrafficGraphGeneration
	LastUpdate             time.Time
	ErrorCategory          ErrorCategory
}

// TrafficGraphClient consumes the authenticated control graph stream. It is
// deliberately separate from route snapshots so an edge never reads control config.
type TrafficGraphClient struct {
	client edgev1.EdgeServiceClient

	mu       sync.RWMutex
	snapshot domain.TrafficGraphSnapshot
	hasData  bool
	health   TrafficGraphHealth
	running  bool
	runID    uint64
	cancel   context.CancelFunc
	done     chan struct{}

	initialBackoff time.Duration
	maxBackoff     time.Duration
	retryWait      func(context.Context, time.Duration) bool
	observer       func(domain.TrafficGraphSnapshot)
}

func NewTrafficGraphClient(conn grpc.ClientConnInterface, options ...Option) *TrafficGraphClient {
	return NewTrafficGraphClientWithEdgeService(edgev1.NewEdgeServiceClient(conn), options...)
}

func NewTrafficGraphClientWithEdgeService(service edgev1.EdgeServiceClient, options ...Option) *TrafficGraphClient {
	client := &TrafficGraphClient{client: service, initialBackoff: defaultInitialBackoff, maxBackoff: defaultMaxBackoff, retryWait: waitForRetry}
	for _, option := range options {
		if option != nil {
			// Reuse the public backoff option without sharing state.
			probe := &Client{initialBackoff: client.initialBackoff, maxBackoff: client.maxBackoff}
			option(probe)
			client.initialBackoff, client.maxBackoff = probe.initialBackoff, probe.maxBackoff
		}
	}
	return client
}

func (c *TrafficGraphClient) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("traffic graph watch context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.client == nil {
		return errors.New("traffic graph service is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return errors.New("traffic graph watch is already running")
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.running, c.cancel, c.done = true, cancel, make(chan struct{})
	c.runID++
	go c.watch(runCtx, c.runID, c.done)
	return nil
}

func (c *TrafficGraphClient) Stop() {
	c.mu.RLock()
	cancel, done := c.cancel, c.done
	c.mu.RUnlock()
	if cancel != nil && done != nil {
		cancel()
		<-done
	}
}

// CurrentTrafficGraph returns a caller-owned clone.
func (c *TrafficGraphClient) CurrentTrafficGraph(ctx context.Context) (domain.TrafficGraphSnapshot, error) {
	if ctx == nil {
		return domain.TrafficGraphSnapshot{}, errors.New("traffic graph context is required")
	}
	if err := ctx.Err(); err != nil {
		return domain.TrafficGraphSnapshot{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasData {
		return domain.TrafficGraphSnapshot{}, ErrTrafficGraphUnavailable
	}
	return c.snapshot.Clone(), nil
}

func (c *TrafficGraphClient) TrafficGraphHealth() TrafficGraphHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

// SetTrafficGraphAcceptanceObserver installs a callback for validated graph
// snapshots. The callback is invoked after the client lock is released so a
// slow listener application can never block graph reads or shutdown.
func (c *TrafficGraphClient) SetTrafficGraphAcceptanceObserver(observer func(domain.TrafficGraphSnapshot)) {
	c.mu.Lock()
	c.observer = observer
	c.mu.Unlock()
}

func (c *TrafficGraphClient) watch(ctx context.Context, runID uint64, done chan struct{}) {
	defer func() {
		c.mu.Lock()
		if c.runID == runID {
			c.running, c.cancel = false, nil
			c.health.Connected, c.health.Healthy = false, false
		}
		c.mu.Unlock()
		close(done)
	}()
	backoff := c.initialBackoff
	for ctx.Err() == nil {
		stream, err := c.client.WatchTrafficGraphs(ctx, &edgev1.WatchTrafficGraphsRequest{})
		if err != nil {
			if c.handleError(ctx, err) || !c.retryWait(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, c.maxBackoff)
			continue
		}
		c.setConnected()
		for {
			message, err := stream.Recv()
			if err != nil {
				if c.handleError(ctx, err) || !c.retryWait(ctx, backoff) {
					return
				}
				backoff = nextBackoff(backoff, c.maxBackoff)
				break
			}
			accepted, err := c.accept(message)
			if err != nil {
				c.setError(ErrorInvalid, false)
				return
			}
			if accepted {
				backoff = c.initialBackoff
			}
		}
	}
}

func (c *TrafficGraphClient) handleError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if isAuthenticationError(err) {
		c.setError(ErrorAuthentication, false)
		return true
	}
	c.setError(ErrorTransport, false)
	return false
}

func (c *TrafficGraphClient) setConnected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.Connected, c.health.Healthy, c.health.ErrorCategory = true, false, ErrorNone
}

func (c *TrafficGraphClient) setError(category ErrorCategory, connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.Connected, c.health.Healthy, c.health.ErrorCategory = connected, false, category
}

func (c *TrafficGraphClient) accept(message *edgev1.WatchTrafficGraphsResponse) (bool, error) {
	snapshot, err := edgeTrafficGraphFromProto(message)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	if c.hasData {
		if snapshot.Generation < c.snapshot.Generation {
			c.mu.Unlock()
			return false, nil
		}
		if snapshot.Generation == c.snapshot.Generation {
			if !reflect.DeepEqual(snapshot, c.snapshot) {
				c.mu.Unlock()
				return false, fmt.Errorf("conflicting traffic graph generation %d", snapshot.Generation)
			}
			c.health.Healthy, c.health.Connected, c.health.ErrorCategory = true, true, ErrorNone
			c.mu.Unlock()
			return true, nil
		}
	}
	c.snapshot, c.hasData = snapshot.Clone(), true
	c.health = TrafficGraphHealth{Healthy: true, Connected: true, LastAcceptedGeneration: snapshot.Generation, LastUpdate: time.Now().UTC()}
	observer := c.observer
	accepted := snapshot.Clone()
	c.mu.Unlock()
	if observer != nil {
		observer(accepted)
	}
	return true, nil
}

func edgeTrafficGraphFromProto(message *edgev1.WatchTrafficGraphsResponse) (domain.TrafficGraphSnapshot, error) {
	// Keep the edge conversion independent of the server adapter; this adapter
	// must not import an inbound transport package.
	if message == nil || message.Options == nil || message.Options.Tcp == nil || message.Options.Udp == nil {
		return domain.TrafficGraphSnapshot{}, errors.New("traffic graph options are required")
	}
	if message.Options.Tcp.MaxConnections < 0 || message.Options.Udp.MaxSessions < 0 {
		return domain.TrafficGraphSnapshot{}, errors.New("traffic graph limit is invalid")
	}
	graph, err := edgeTrafficGraphFields(message)
	if err != nil {
		return domain.TrafficGraphSnapshot{}, err
	}
	snapshot := domain.TrafficGraphSnapshot{Generation: domain.TrafficGraphGeneration(message.Generation), Graph: graph}
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return domain.TrafficGraphSnapshot{}, fmt.Errorf("validate traffic graph: %w", err)
	}
	return snapshot, nil
}

func edgeTrafficGraphFields(message *edgev1.WatchTrafficGraphsResponse) (domain.TrafficGraph, error) {
	graph := domain.TrafficGraph{Options: domain.TrafficOptions{TCP: domain.TCPOptions{DialTimeout: time.Duration(message.Options.Tcp.DialTimeoutNanos), IdleTimeout: time.Duration(message.Options.Tcp.IdleTimeoutNanos), DrainTimeout: time.Duration(message.Options.Tcp.DrainTimeoutNanos), MaxConnections: int(message.Options.Tcp.MaxConnections)}, UDP: domain.UDPOptions{IdleTimeout: time.Duration(message.Options.Udp.IdleTimeoutNanos), DrainTimeout: time.Duration(message.Options.Udp.DrainTimeoutNanos), MaxSessions: int(message.Options.Udp.MaxSessions)}}}
	var err error
	if graph.EntryPoints, err = edgeTrafficEntryPoints(message.EntryPoints); err != nil {
		return domain.TrafficGraph{}, err
	}
	if graph.Routers, err = edgeTrafficRouters(message.Routers); err != nil {
		return domain.TrafficGraph{}, err
	}
	if graph.Services, err = edgeTrafficServices(message.Services); err != nil {
		return domain.TrafficGraph{}, err
	}
	return graph, nil
}

func edgeTrafficEntryPoints(values []*edgev1.TrafficEntryPoint) ([]domain.EntryPoint, error) {
	converted := make([]domain.EntryPoint, 0, len(values))
	for _, entry := range values {
		if entry == nil {
			return nil, errors.New("traffic graph entrypoint is required")
		}
		converted = append(converted, domain.EntryPoint{Name: entry.Name, Address: entry.Address, Protocol: domain.EntryPointProtocol(entry.Protocol), TrustedCIDRs: append([]string(nil), entry.TrustedCidrs...), RawFallback: entry.RawFallback, RawFallbackTrustedCIDRs: append([]string(nil), entry.RawFallbackTrustedCidrs...), AllowPublicRawFallback: entry.AllowPublicRawFallback})
	}
	return converted, nil
}

func edgeTrafficRouters(values []*edgev1.TrafficRouter) ([]domain.TrafficRouter, error) {
	converted := make([]domain.TrafficRouter, 0, len(values))
	for _, router := range values {
		if router == nil || router.Rule == nil {
			return nil, errors.New("traffic graph router is required")
		}
		converted = append(converted, domain.TrafficRouter{Name: router.Name, EntryPoint: router.EntryPoint, Protocol: domain.RouterProtocol(router.Protocol), Rule: domain.TrafficRule{Host: router.Rule.Host, SNI: router.Rule.Sni}, Service: router.Service})
	}
	return converted, nil
}

func edgeTrafficServices(values []*edgev1.TrafficService) ([]domain.TrafficService, error) {
	converted := make([]domain.TrafficService, 0, len(values))
	for _, service := range values {
		if service == nil {
			return nil, errors.New("traffic graph service is required")
		}
		result := domain.TrafficService{Name: service.Name}
		for _, backend := range service.Backends {
			if backend == nil || backend.Port < 0 {
				return nil, errors.New("traffic graph backend is invalid")
			}
			result.Backends = append(result.Backends, domain.TrafficBackend{Name: backend.Name, Host: backend.Host, Port: int(backend.Port), Protocol: domain.NetworkProtocol(backend.Protocol)})
		}
		converted = append(converted, result)
	}
	return converted, nil
}
