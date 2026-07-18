package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// MigrationOrchestrator performs the non-public prepare sequence. Bootstrap is
// intentionally explicit: the existing monolith/runtime authority receives the
// authenticated command to create the new runtime, then control uses that new
// authenticated channel. No control/edge/registry process is given socket
// authority at any point.
type MigrationOrchestrator struct {
	preflight   *MigrationPreflight
	store       *MigrationCheckpointStore
	launcher    ComponentLauncher
	now         func() time.Time
	appNetworks func(context.Context) ([]string, error)
}

func NewMigrationOrchestrator(preflight *MigrationPreflight, store *MigrationCheckpointStore, launcher ComponentLauncher) (*MigrationOrchestrator, error) {
	if preflight == nil || store == nil || launcher == nil {
		return nil, fmt.Errorf("migration preflight, checkpoint store, and component launcher are required")
	}
	return &MigrationOrchestrator{preflight: preflight, store: store, launcher: launcher, now: time.Now}, nil
}

// DryRun invokes only read-only preflight probes; it cannot write a checkpoint,
// credentials, env file, network, or component.
// WithRuntimeSnapshotAppNetworks obtains only managed app-network names from
// the authenticated runtime actual-state stream. No raw inspect data crosses
// into control and a snapshot failure stops preparation before launch.
func (o *MigrationOrchestrator) WithRuntimeSnapshotAppNetworks(subscriber out.RuntimeStateSubscriber) *MigrationOrchestrator {
	if o != nil && subscriber != nil {
		o.appNetworks = func(ctx context.Context) ([]string, error) {
			updates, err := subscriber.SubscribeRuntimeState(ctx)
			if err != nil {
				return nil, err
			}
			select {
			case snapshot, ok := <-updates:
				if !ok {
					return nil, fmt.Errorf("runtime state stream closed")
				}
				networks := make([]string, 0, len(snapshot.EdgeAttachments))
				for _, attachment := range snapshot.EdgeAttachments {
					if attachment.Attached {
						networks = append(networks, attachment.NetworkName)
					}
				}
				return safeAppNetworks(networks), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return o
}

func (o *MigrationOrchestrator) DryRun(ctx context.Context) (MigrationPreflightReport, error) {
	if o == nil || o.preflight == nil {
		return MigrationPreflightReport{}, fmt.Errorf("migration orchestrator is not configured")
	}
	return o.preflight.Check(ctx), nil
}

func (o *MigrationOrchestrator) Prepare(ctx context.Context, checkpoint MigrationCheckpoint) (*MigrationCheckpoint, error) {
	if o == nil || o.launcher == nil || o.store == nil {
		return nil, fmt.Errorf("migration orchestrator is not configured")
	}
	if err := o.normalizePrepareCheckpoint(&checkpoint); err != nil {
		return nil, err
	}
	if err := o.resolveAppNetworks(ctx, &checkpoint); err != nil {
		return nil, err
	}
	plan, err := NewComponentLaunchPlan(checkpoint)
	if err != nil {
		return nil, err
	}
	if err := o.startPlan(ctx, plan, &checkpoint); err != nil {
		return nil, err
	}
	if err := o.checkPlanHealth(ctx, plan); err != nil {
		return nil, err
	}
	if err := o.connectEdgeAppNetworks(ctx, plan, &checkpoint); err != nil {
		return nil, err
	}
	if err := o.store.Save(checkpoint); err != nil {
		return nil, fmt.Errorf("checkpoint prepared components: %w", err)
	}
	return o.store.Load()
}

func (o *MigrationOrchestrator) normalizePrepareCheckpoint(checkpoint *MigrationCheckpoint) error {
	if checkpoint.StartedAt.IsZero() {
		checkpoint.StartedAt = o.now().UTC()
	}
	if checkpoint.Phase == "" || checkpoint.Phase == MigrationPhasePlanned {
		checkpoint.Phase = MigrationPhasePrepared
	}
	if checkpoint.Phase != MigrationPhasePrepared {
		return fmt.Errorf("prepare requires prepared phase")
	}
	return nil
}

func (o *MigrationOrchestrator) resolveAppNetworks(ctx context.Context, checkpoint *MigrationCheckpoint) error {
	if len(checkpoint.EdgeAppNetworks) != 0 || o.appNetworks == nil {
		return nil
	}
	networks, err := o.appNetworks(ctx)
	if err != nil {
		return fmt.Errorf("read runtime route snapshot: %w", err)
	}
	checkpoint.EdgeAppNetworks = networks
	if err := o.store.Save(*checkpoint); err != nil {
		return fmt.Errorf("checkpoint app networks: %w", err)
	}
	return nil
}

func (o *MigrationOrchestrator) startPlan(ctx context.Context, plan ComponentLaunchPlan, checkpoint *MigrationCheckpoint) error {
	if len(checkpoint.PreparedComponents) == 0 {
		if err := o.launcher.CreateInternalNetwork(ctx, plan); err != nil {
			return fmt.Errorf("prepare internal network: %w", err)
		}
	}
	for _, component := range plan.Components {
		if slices.Contains(checkpoint.PreparedComponents, component.ComponentID) {
			continue
		}
		if err := o.launcher.StartComponent(ctx, component); err != nil {
			return fmt.Errorf("start %s: %w", component.Role, err)
		}
		checkpoint.PreparedComponents = append(checkpoint.PreparedComponents, component.ComponentID)
		if err := o.store.Save(*checkpoint); err != nil {
			return fmt.Errorf("checkpoint %s start: %w", component.Role, err)
		}
	}
	return o.transferRuntimeCommandChannel(ctx, plan, checkpoint)
}

func (o *MigrationOrchestrator) transferRuntimeCommandChannel(ctx context.Context, plan ComponentLaunchPlan, checkpoint *MigrationCheckpoint) error {
	transfer, ok := o.launcher.(RuntimeCommandChannelTransfer)
	if !ok || checkpoint.RuntimeChannelTransferred {
		return nil
	}
	for _, component := range plan.Components {
		if component.Role != "runtime" {
			continue
		}
		if err := transfer.TransferRuntimeCommandChannel(ctx, component); err != nil {
			return fmt.Errorf("transfer runtime command channel: %w", err)
		}
		checkpoint.RuntimeChannelTransferred = true
		if err := o.store.Save(*checkpoint); err != nil {
			return fmt.Errorf("checkpoint runtime command channel: %w", err)
		}
		return nil
	}
	return fmt.Errorf("runtime component missing from launch plan")
}

func (o *MigrationOrchestrator) checkPlanHealth(ctx context.Context, plan ComponentLaunchPlan) error {
	for _, component := range plan.Components {
		if err := o.launcher.CheckComponentHealth(ctx, component); err != nil {
			return fmt.Errorf("health check %s: %w", component.Role, err)
		}
	}
	return nil
}

// connectEdgeAppNetworks is deliberately after all health/auth checks and
// connects only checkpointed managed app networks; it never changes traffic.
func (o *MigrationOrchestrator) connectEdgeAppNetworks(ctx context.Context, plan ComponentLaunchPlan, checkpoint *MigrationCheckpoint) error {
	for _, component := range plan.Components {
		if component.Role != "edge" {
			continue
		}
		for _, network := range plan.AppNetworks {
			if slices.Contains(checkpoint.ConnectedEdgeNetworks, network) {
				continue
			}
			if err := o.launcher.ConnectEdgeToAppNetwork(ctx, component, network); err != nil {
				return fmt.Errorf("connect edge to app network: %w", err)
			}
			checkpoint.ConnectedEdgeNetworks = append(checkpoint.ConnectedEdgeNetworks, network)
			if err := o.store.Save(*checkpoint); err != nil {
				return fmt.Errorf("checkpoint edge network connection: %w", err)
			}
		}
	}
	return nil
}

// CleanupPrepared is safe after a failed prepare: it asks runtime to remove
// only labeled prepared component containers, in reverse order. The lifecycle
// command always carries PreserveVolumes=true; volumes are never a cleanup target.
func (o *MigrationOrchestrator) CleanupPrepared(ctx context.Context, plan ComponentLaunchPlan) error {
	if o == nil || o.launcher == nil {
		return fmt.Errorf("migration orchestrator is not configured")
	}
	for index := len(plan.Components) - 1; index >= 0; index-- {
		if err := o.launcher.RemovePreparedComponent(ctx, plan.Components[index]); err != nil {
			return fmt.Errorf("remove prepared %s: %w", plan.Components[index].Role, err)
		}
	}
	return nil
}
