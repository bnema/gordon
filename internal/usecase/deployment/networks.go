package deployment

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/domain"
)

// ensureIncarnationID returns the app's stable internal UUID, assigning
// and persisting one on first use. Every incarnation-scoped resource
// (network, volumes, secrets) keys off this ID, never the public name, so
// removing and re-adding an app under the same name can never inherit the
// old incarnation's resources.
func (s *Service) ensureIncarnationID(ctx context.Context, app string) (string, error) {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return "", fmt.Errorf("deployment: load ownership: %w", err)
	}
	if ownership.ID != "" {
		return ownership.ID, nil
	}
	ownership.App = app
	if err := s.deps.State.SaveOwnership(ctx, ownership); err != nil {
		return "", fmt.Errorf("deployment: assign incarnation: %w", err)
	}
	saved, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return "", fmt.Errorf("deployment: reload ownership: %w", err)
	}
	if saved.ID == "" {
		return "", fmt.Errorf("deployment: app %q has no incarnation id: %w", app, domain.ErrAppStateConflict)
	}
	return saved.ID, nil
}

// appNetworks is the runtime name set one service joins.
type appNetworks struct {
	// private is the incarnation-owned network every service of the app
	// joins; no other app shares it.
	private string
	// shared are the declared cross-app memberships resolved to runtime
	// names.
	shared []sharedNetwork
}

// sharedNetwork ties a declared membership to its derived runtime name.
type sharedNetwork struct {
	declared string
	runtime  string
}

// resolveAppNetworks derives runtime network names from the app UUID and
// the service's declared shared memberships. Deriving (never accepting a
// caller-supplied runtime name) keeps recovery and deploy in agreement and
// prevents a manifest from naming a foreign network.
func (s *Service) resolveAppNetworks(appID string, memberships []domain.AppSharedNetwork) appNetworks {
	prefix := s.deps.Networks.Prefix
	resolved := appNetworks{private: domain.AppPrivateNetworkName(prefix, appID)}
	for _, membership := range memberships {
		resolved.shared = append(resolved.shared, sharedNetwork{
			declared: membership.Network,
			runtime:  domain.AppSharedNetworkName(prefix, membership.Network),
		})
	}
	return resolved
}

// ensureAppNetworks verifies or creates the incarnation network and every
// declared shared network BEFORE any container is created. An existing
// network is reused only when its labels prove Gordon ownership; any other
// network using the derived name is refused fail-closed. Networks are
// never removed here: an empty owned network is inert, and during a
// replacement the superseded generation may still be attached to it.
func (s *Service) ensureAppNetworks(ctx context.Context, app, appID string, nets appNetworks) error {
	observed, err := s.deps.Runtime.ListNetworks(ctx)
	if err != nil {
		return fmt.Errorf("deployment: list networks: %w", err)
	}
	byName := make(map[string]*domain.NetworkInfo, len(observed))
	for _, network := range observed {
		if network == nil {
			continue
		}
		byName[network.Name] = network
	}
	if err := s.ensureNetwork(ctx, byName, nets.private, domain.AppPrivateNetworkLabels(app, appID)); err != nil {
		return err
	}
	for _, shared := range nets.shared {
		if err := s.ensureNetwork(ctx, byName, shared.runtime, domain.AppSharedNetworkLabels(shared.declared)); err != nil {
			return err
		}
	}
	return nil
}

// ensureNetwork reuses an owned network or creates it.
func (s *Service) ensureNetwork(ctx context.Context, byName map[string]*domain.NetworkInfo, name string, labels map[string]string) error {
	if existing, ok := byName[name]; ok {
		if !domain.NetworkOwnedBy(existing.Labels, labels) {
			return fmt.Errorf("deployment: network %q exists without Gordon ownership: %w", name, domain.ErrAppStateConflict)
		}
		return nil
	}
	config := domain.NetworkConfig{Driver: "bridge", Internal: s.deps.Networks.Internal, Labels: labels}
	if err := s.deps.Runtime.CreateNetwork(ctx, name, config); err != nil {
		return fmt.Errorf("deployment: create network %q: %w", name, err)
	}
	return nil
}

// connectSharedNetworks attaches a created container to every declared
// shared network. The private network is attached at create through
// NetworkMode; shared memberships are explicit so an app joins only the
// networks its manifest declares.
func (s *Service) connectSharedNetworks(ctx context.Context, containerID string, nets appNetworks) error {
	for _, shared := range nets.shared {
		if err := s.deps.Runtime.ConnectContainerToNetwork(ctx, containerID, shared.runtime); err != nil {
			return fmt.Errorf("deployment: connect %s to network %q: %w", containerID, shared.runtime, err)
		}
	}
	return nil
}

// recordNetworkOwnership stamps the incarnation network and every declared
// shared membership into the ownership record so prune/remove can see
// exactly which networks this app created or joined.
func recordNetworkOwnership(ownership *domain.AppOwnership, prefix string, shared []domain.AppSharedNetwork) {
	if ownership.ID == "" {
		return
	}
	entries := []domain.AppOwnedNetwork{{Name: domain.AppPrivateNetworkName(prefix, ownership.ID), Role: domain.AppNetworkRolePrivate}}
	for _, membership := range shared {
		entries = append(entries, domain.AppOwnedNetwork{
			Name: domain.AppSharedNetworkName(prefix, membership.Network),
			Role: domain.AppNetworkRoleShared,
		})
	}
	seen := make(map[string]struct{}, len(entries))
	merged := make([]domain.AppOwnedNetwork, 0, len(entries))
	for _, entry := range entries {
		if _, ok := seen[entry.Name]; ok {
			continue
		}
		seen[entry.Name] = struct{}{}
		merged = append(merged, entry)
	}
	for _, existing := range ownership.Networks {
		if _, ok := seen[existing.Name]; ok {
			continue
		}
		seen[existing.Name] = struct{}{}
		merged = append(merged, existing)
	}
	ownership.Networks = merged
}

// reclaimPrivateNetworks removes the incarnation-owned private networks of
// one app immediately after every exact container is confirmed gone, while
// the ownership record is still live. The runtime name is re-derived from
// the incarnation UUID and the observed labels must prove that exact
// ownership, so a name reused by another incarnation can never be
// deleted. Shared networks are never removed: they belong to the
// installation, and other apps may still be attached. An unattached,
// ownership-verified network is removed; anything else is left in place
// and reported as a bounded warning, never silently deleted.
func (s *Service) reclaimPrivateNetworks(ctx context.Context, app string, ownership domain.AppOwnership) ([]CleanupWarning, error) {
	if ownership.ID == "" {
		return nil, nil
	}
	expectedName := domain.AppPrivateNetworkName(s.deps.Networks.Prefix, ownership.ID)
	private := make([]string, 0, len(ownership.Networks))
	for _, network := range ownership.Networks {
		if network.Role != domain.AppNetworkRolePrivate {
			continue
		}
		if network.Name != expectedName {
			// A record naming anything but this incarnation's derived
			// network is never deleted: the name may belong to a
			// different incarnation.
			continue
		}
		private = append(private, network.Name)
	}
	if len(private) == 0 {
		return nil, nil
	}
	observed, err := s.deps.Runtime.ListNetworks(ctx)
	if err != nil {
		return nil, fmt.Errorf("deployment: list networks for reclamation: %w", err)
	}
	byName := make(map[string]*domain.NetworkInfo, len(observed))
	for _, network := range observed {
		if network == nil {
			continue
		}
		byName[network.Name] = network
	}
	expected := domain.AppPrivateNetworkLabels(app, ownership.ID)
	var warnings []CleanupWarning
	for _, name := range private {
		network, ok := byName[name]
		if !ok {
			continue // already gone
		}
		if !domain.NetworkOwnedBy(network.Labels, expected) {
			warnings = append(warnings, CleanupWarning{
				Leftover: name,
				Detail:   "private network labels do not prove this incarnation; left in place",
			})
			continue
		}
		if len(network.Containers) > 0 {
			warnings = append(warnings, CleanupWarning{
				Leftover: name,
				Detail:   "private network still has attached containers; left in place",
			})
			continue
		}
		if err := s.deps.Runtime.RemoveNetwork(ctx, name); err != nil {
			return warnings, fmt.Errorf("deployment: remove private network %q: %w", name, err)
		}
	}
	return warnings, nil
}
