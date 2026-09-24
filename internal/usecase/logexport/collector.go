// Package logexport follows app container output and exports it as
// log records, one source identity per app service.
package logexport

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	defaultReconcileInterval = 10 * time.Second
	defaultRetryDelay        = 2 * time.Second
)

// Collector keeps one follower per exportable ACTIVE container.
//
// A container is exportable when its app is not stopped and its service
// did not opt out (domain.AppService.LogExportDisabled). Reconciliation
// polls ACTIVE state so deploys, stops, and opt-out changes converge
// without coupling the deployment engine to log export.
type Collector struct {
	state    out.AppStateReader
	streamer out.ContainerLogStreamer
	exporter out.LogExporter

	reconcileInterval time.Duration
	retryDelay        time.Duration
	// startedAt bounds replay: a follower exports output emitted since
	// the collector was created, so a container deployed later is
	// exported from its first line, while restarting Gordon never
	// re-exports older output.
	startedAt time.Time

	mu        sync.Mutex
	followers map[string]*follower
	wg        sync.WaitGroup
}

type follower struct {
	source domain.LogSource
	cancel context.CancelFunc
}

// NewCollector creates a collector. Call Run to start it.
func NewCollector(state out.AppStateReader, streamer out.ContainerLogStreamer, exporter out.LogExporter) *Collector {
	return &Collector{
		state:             state,
		streamer:          streamer,
		exporter:          exporter,
		reconcileInterval: defaultReconcileInterval,
		retryDelay:        defaultRetryDelay,
		startedAt:         time.Now(),
		followers:         map[string]*follower{},
	}
}

// Run reconciles until ctx is canceled, then stops every follower and
// waits for them to exit.
func (c *Collector) Run(ctx context.Context) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "LogExport",
	})
	defer c.stopAll()
	log := zerowrap.FromCtx(ctx)

	ticker := time.NewTicker(c.reconcileInterval)
	defer ticker.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil && ctx.Err() == nil {
			log.Warn().Err(err).Msg("log export reconciliation failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Reconcile starts followers for new exportable containers and stops
// followers whose container is gone, stopped, or opted out.
func (c *Collector) Reconcile(ctx context.Context) error {
	desired, err := c.desiredContainers(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for containerID, f := range c.followers {
		if source, ok := desired[containerID]; !ok || source != f.source {
			f.cancel()
			delete(c.followers, containerID)
		}
	}
	for containerID, source := range desired {
		if _, ok := c.followers[containerID]; ok {
			continue
		}
		c.startLocked(ctx, containerID, source)
	}
	return nil
}

// desiredContainers maps exportable ACTIVE container IDs to their source.
func (c *Collector) desiredContainers(ctx context.Context) (map[string]domain.LogSource, error) {
	apps, err := c.state.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	desired := map[string]domain.LogSource{}
	for _, app := range apps {
		active, found, err := c.state.LoadActive(ctx, app)
		if err != nil {
			return nil, err
		}
		if !found || active.StopIntent {
			continue
		}
		for name, svc := range active.Services {
			if svc.Container == "" || svc.Spec.LogExportDisabled {
				continue
			}
			desired[svc.Container] = domain.LogSource{App: app, Service: name}
		}
	}
	return desired, nil
}

func (c *Collector) startLocked(parent context.Context, containerID string, source domain.LogSource) {
	ctx, cancel := context.WithCancel(parent)
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldEntityID: containerID,
		"app":                  source.App,
		"service":              source.Service,
	})
	c.followers[containerID] = &follower{source: source, cancel: cancel}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.follow(ctx, containerID, source)
	}()
}

// follow streams one container until canceled, starting at startedAt.
// A stream that ends (container restarting) resumes after the last
// exported line, so a reconnect neither duplicates nor replays output.
func (c *Collector) follow(ctx context.Context, containerID string, source domain.LogSource) {
	log := zerowrap.FromCtx(ctx)
	since := c.startedAt
	for {
		err := c.streamer.StreamContainerLogs(ctx, containerID, since, func(line domain.ContainerLogLine) {
			c.exporter.Export(ctx, domain.LogRecord{
				Time:   line.Time,
				Source: source,
				Type:   domain.LogTypeContainer,
				Stream: line.Stream,
				Body:   line.Body,
			})
			if line.Time.After(since) {
				since = line.Time.Add(time.Nanosecond)
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, domain.ErrContainerNotFound) {
			log.Debug().Err(err).Msg("container log stream interrupted, retrying")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.retryDelay):
		}
	}
}

func (c *Collector) stopAll() {
	c.mu.Lock()
	for containerID, f := range c.followers {
		f.cancel()
		delete(c.followers, containerID)
	}
	c.mu.Unlock()
	c.wg.Wait()
}
