package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// DefaultNetworkPrefix applies when no installation prefix is configured.
const DefaultNetworkPrefix = "gordon"

// Network roles identifying what a Gordon-managed network is for.
const (
	// AppNetworkRolePrivate marks the incarnation-owned network of one app.
	// Containers of different apps never share it.
	AppNetworkRolePrivate = "private"
	// AppNetworkRoleShared marks a declared cross-app shared network.
	AppNetworkRoleShared = "shared"
)

// Network ownership label keys. Management follows the same fail-closed
// rule as volumes: an existing network is reused only when its labels
// prove Gordon ownership; anything else is refused, never adopted.
const (
	// LabelAppNetworkRole carries AppNetworkRolePrivate/AppNetworkRoleShared.
	LabelAppNetworkRole = "gordon.app.network.role"
	// LabelAppNetworkName carries the declared shared-network name, so a
	// derived runtime name can be tied back to exactly one declaration.
	LabelAppNetworkName = "gordon.app.network.name"
)

// NormalizeNetworkPrefix returns the configured prefix or the default.
func NormalizeNetworkPrefix(prefix string) string {
	if strings.TrimSpace(prefix) == "" {
		return DefaultNetworkPrefix
	}
	return prefix
}

// AppPrivateNetworkName derives the incarnation-owned network name from
// the app's stable internal UUID. The name is deterministic so recovery
// reattaches the same network, and it never derives from the public name:
// removing and re-adding an app under the same name yields a different
// incarnation UUID and therefore a different network.
func AppPrivateNetworkName(prefix, appID string) string {
	sum := sha256.Sum256([]byte(appID))
	return fmt.Sprintf("%s-app-%x", NormalizeNetworkPrefix(prefix), sum[:6])
}

// AppSharedNetworkName derives the Gordon-managed name of a declared
// shared network. Deriving from the declaration means two apps that
// declare the same name join the same network, while a foreign network
// that happens to use the name is never mistaken for it.
func AppSharedNetworkName(prefix, declared string) string {
	sum := sha256.Sum256([]byte(declared))
	return fmt.Sprintf("%s-shared-%x", NormalizeNetworkPrefix(prefix), sum[:6])
}

// AppPrivateNetworkLabels is the ownership label set stamped on an
// incarnation-owned network.
func AppPrivateNetworkLabels(app, appID string) map[string]string {
	labels := map[string]string{
		LabelManaged:        "true",
		LabelApp:            app,
		LabelAppNetworkRole: AppNetworkRolePrivate,
	}
	if appID != "" {
		labels[LabelAppID] = appID
	}
	return labels
}

// AppSharedNetworkLabels is the ownership label set stamped on a declared
// shared network. A shared network belongs to the installation, not to a
// single app, so it carries no app identity.
func AppSharedNetworkLabels(declared string) map[string]string {
	return map[string]string{
		LabelManaged:        "true",
		LabelAppNetworkRole: AppNetworkRoleShared,
		LabelAppNetworkName: declared,
	}
}

// NetworkOwnedBy reports whether an observed network's labels prove the
// exact expected Gordon ownership. Extra labels are tolerated; every
// expected key must be present and equal, so a network created with
// different ownership (or none) is never adopted.
func NetworkOwnedBy(observed, expected map[string]string) bool {
	if len(expected) == 0 {
		return false
	}
	for key, want := range expected {
		if observed[key] != want {
			return false
		}
	}
	return true
}

// AppServiceSharedNetworks returns the declared shared-network memberships
// that the named service joins, preserving declaration order and
// de-duplicating by network name.
func AppServiceSharedNetworks(spec AppSpec, service string) []AppSharedNetwork {
	var memberships []AppSharedNetwork
	seen := map[string]struct{}{}
	for _, network := range spec.Networks {
		if _, ok := seen[network.Network]; ok {
			continue
		}
		for _, member := range network.Services {
			if member != service {
				continue
			}
			seen[network.Network] = struct{}{}
			memberships = append(memberships, network)
			break
		}
	}
	return memberships
}
