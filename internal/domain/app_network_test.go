package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
)

func TestAppPrivateNetworkName_DerivesFromUUIDNotName(t *testing.T) {
	first := domain.AppPrivateNetworkName("gordon", "app-1")
	second := domain.AppPrivateNetworkName("gordon", "app-2")
	assert.NotEqual(t, first, second, "distinct incarnations must not share a network")
	assert.Equal(t, first, domain.AppPrivateNetworkName("gordon", "app-1"), "derivation is deterministic for recovery")
	assert.NotEqual(t, "default", first)
	assert.NotEqual(t, "bridge", first)

	// Same public name, new incarnation UUID: a different network.
	reused := domain.AppPrivateNetworkName("gordon", "app-9")
	assert.NotEqual(t, first, reused, "name reuse must not reattach the old network")
}

func TestAppPrivateNetworkName_AppliesDefaultPrefix(t *testing.T) {
	assert.Equal(t,
		domain.AppPrivateNetworkName("gordon", "app-1"),
		domain.AppPrivateNetworkName("", "app-1"),
	)
}

func TestAppSharedNetworkName_StablePerDeclaration(t *testing.T) {
	assert.Equal(t, domain.AppSharedNetworkName("gordon", "db"), domain.AppSharedNetworkName("gordon", "db"))
	assert.NotEqual(t, domain.AppSharedNetworkName("gordon", "db"), domain.AppSharedNetworkName("gordon", "cache"))
}

func TestNetworkOwnedBy_RequiresEveryExpectedLabel(t *testing.T) {
	expected := domain.AppPrivateNetworkLabels("blog", "app-1")
	assert.True(t, domain.NetworkOwnedBy(expected, expected))
	assert.True(t, domain.NetworkOwnedBy(map[string]string{
		domain.LabelManaged:        "true",
		domain.LabelApp:            "blog",
		domain.LabelAppNetworkRole: domain.AppNetworkRolePrivate,
		domain.LabelAppID:          "app-1",
		"extra":                    "tolerated",
	}, expected))
	assert.False(t, domain.NetworkOwnedBy(map[string]string{domain.LabelManaged: "true"}, expected))
	assert.False(t, domain.NetworkOwnedBy(nil, expected))
	assert.False(t, domain.NetworkOwnedBy(expected, nil), "empty expectation is never proof of ownership")
	assert.False(t, domain.NetworkOwnedBy(map[string]string{
		domain.LabelManaged:        "true",
		domain.LabelApp:            "blog",
		domain.LabelAppNetworkRole: domain.AppNetworkRolePrivate,
		domain.LabelAppID:          "app-2",
	}, expected), "a different incarnation is foreign")
}

func TestAppServiceSharedNetworks_FiltersAndDeduplicates(t *testing.T) {
	spec := domain.AppSpec{
		Name: "blog",
		Networks: []domain.AppSharedNetwork{
			{Network: "db", Services: []string{"web", "worker"}},
			{Network: "cache", Services: []string{"worker"}},
			{Network: "db", Services: []string{"web"}},
		},
	}
	web := domain.AppServiceSharedNetworks(spec, "web")
	assert.Equal(t, []domain.AppSharedNetwork{{Network: "db", Services: []string{"web", "worker"}}}, web)

	worker := domain.AppServiceSharedNetworks(spec, "worker")
	assert.Len(t, worker, 2)
	assert.Equal(t, "db", worker[0].Network)
	assert.Equal(t, "cache", worker[1].Network)

	assert.Empty(t, domain.AppServiceSharedNetworks(spec, "absent"))
}
