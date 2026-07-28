package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
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
	switcher    TrafficSwitcher
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
				networks = safeAppNetworks(networks)
				// A route in authenticated actual state is managed runtime
				// inventory. Never launch an edge that cannot be attached to any
				// unambiguous managed backend network.
				if len(snapshot.Routes) != 0 && len(networks) == 0 {
					return nil, fmt.Errorf("managed runtime routes have no unambiguous app networks")
				}
				return networks, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return o
}

// WithTrafficSwitcher enables the separately gated public cutover operation.
// Without it, Switch fails closed rather than making a best-effort change.
func (o *MigrationOrchestrator) WithTrafficSwitcher(switcher TrafficSwitcher) *MigrationOrchestrator {
	if o != nil {
		o.switcher = switcher
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
	// This is the durable prepare barrier. In particular, recovery after a
	// runtime-channel handoff must finish registry/edge creation and health
	// before it may offer public listener activation.
	checkpoint.PrepareComplete = true
	// Save atomically merges an authenticated edge attestation that may have
	// arrived while preparation was completing.
	if err := o.store.Save(checkpoint); err != nil {
		return nil, fmt.Errorf("checkpoint prepared components: %w", err)
	}
	return o.store.Load()
}

// Switch advances only a prepared checkpoint after TrafficSwitcher has
// verified every prerequisite and activated the edge through RuntimeSelfUpdate.
// A failed switch persists retry metadata after the runtime has compensated
// the managed inventory. Cold cutover restores the probe-only prepared edge;
// compatibility cutover also restores a managed old container when present.
func (o *MigrationOrchestrator) Switch(ctx context.Context, checkpoint MigrationCheckpoint) (*MigrationCheckpoint, error) {
	if o == nil || o.store == nil || o.switcher == nil {
		return nil, fmt.Errorf("migration traffic switch is not configured")
	}
	if checkpoint.Phase == MigrationPhaseSwitched {
		return &checkpoint, nil
	}
	if checkpoint.Phase != MigrationPhasePrepared || !checkpoint.PrepareComplete {
		return nil, fmt.Errorf("traffic switch requires completed prepare phase")
	}
	checkpoint, err := o.waitForEdgeAppliedCheckpoint(ctx, checkpoint)
	if err != nil {
		return nil, err
	}
	plan, err := NewComponentLaunchPlan(checkpoint)
	if err != nil {
		return nil, err
	}
	if err := o.switcher.Switch(ctx, checkpoint, plan); err != nil {
		checkpoint.SwitchAttempts++
		checkpoint.LastRetryPhase = "switch"
		if saveErr := o.store.Save(checkpoint); saveErr != nil {
			return nil, fmt.Errorf("checkpoint failed traffic switch: %w", saveErr)
		}
		return nil, fmt.Errorf("traffic switch failed with compensated inventory: %w", err)
	}
	checkpoint.Phase = MigrationPhaseSwitched
	checkpoint.LastRetryPhase = ""
	if err := o.store.Save(checkpoint); err != nil {
		return nil, fmt.Errorf("checkpoint traffic switch: %w", err)
	}
	return o.store.Load()
}

const migrationEdgeAppliedWait = 10 * time.Second

// waitForEdgeAppliedCheckpoint reloads the durable control attestation rather
// than trusting the CLI process's stale checkpoint copy. A newly started edge
// reports asynchronously; switch waits only for its non-zero persisted fact.
func (o *MigrationOrchestrator) waitForEdgeAppliedCheckpoint(ctx context.Context, checkpoint MigrationCheckpoint) (MigrationCheckpoint, error) {
	if checkpoint.RouteSnapshotGeneration != 0 {
		return checkpoint, nil
	}
	deadline := time.NewTimer(migrationEdgeAppliedWait)
	defer deadline.Stop()
	for {
		persisted, err := o.store.Load()
		if err != nil {
			return MigrationCheckpoint{}, fmt.Errorf("load edge applied checkpoint: %w", err)
		}
		if persisted.MigrationID != checkpoint.MigrationID || persisted.ComponentGeneration != checkpoint.ComponentGeneration || persisted.Phase != MigrationPhasePrepared {
			return MigrationCheckpoint{}, fmt.Errorf("edge applied checkpoint no longer matches prepared migration")
		}
		if persisted.RouteSnapshotGeneration != 0 {
			return *persisted, nil
		}
		select {
		case <-ctx.Done():
			return MigrationCheckpoint{}, fmt.Errorf("wait for edge applied generation: %w", ctx.Err())
		case <-deadline.C:
			return MigrationCheckpoint{}, fmt.Errorf("traffic switch requires an authenticated edge applied generation")
		case <-time.After(100 * time.Millisecond):
		}
	}
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
		if !slices.Contains(checkpoint.PreparedComponents, component.ComponentID) {
			if err := o.launcher.StartComponent(ctx, component); err != nil {
				return fmt.Errorf("start %s: %w", component.Role, err)
			}
			checkpoint.PreparedComponents = append(checkpoint.PreparedComponents, component.ComponentID)
			if err := o.store.Save(*checkpoint); err != nil {
				return fmt.Errorf("checkpoint %s start: %w", component.Role, err)
			}
		}
		// Control and runtime are the bootstrap pair. The replacement runtime
		// must prove authenticated health and state before registry or edge can
		// receive a command from it.
		if component.Role == domain.ComponentRoleRuntime {
			if err := o.transferRuntimeCommandChannel(ctx, plan, checkpoint); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *MigrationOrchestrator) transferRuntimeCommandChannel(ctx context.Context, plan ComponentLaunchPlan, checkpoint *MigrationCheckpoint) error {
	transfer, ok := o.launcher.(RuntimeCommandChannelTransfer)
	if !ok || checkpoint.RuntimeChannelTransferred {
		return nil
	}
	for _, component := range plan.Components {
		if component.Role != domain.ComponentRoleRuntime {
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
		if component.Role != domain.ComponentRoleEdge {
			continue
		}
		connected := make([]string, 0, len(plan.AppNetworks))
		for _, network := range plan.AppNetworks {
			if err := o.launcher.ConnectEdgeToAppNetwork(ctx, component, network); err != nil {
				return fmt.Errorf("connect edge to app network: %w", err)
			}
			connected = append(connected, network)
			checkpoint.ConnectedEdgeNetworks = append([]string(nil), connected...)
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
