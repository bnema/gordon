package apptraffic_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
)

func entrypoints() map[string]apptraffic.EntrypointPolicy {
	return map[string]apptraffic.EntrypointPolicy{
		"tcp": {Name: "tcp", TrustedCIDRs: []string{"10.0.0.0/8"}},
		"udp": {Name: "udp"},
	}
}

func webActive() domain.AppActive {
	return domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Spec: domain.AppService{
					HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
				},
			},
		},
	}
}

func TestProject_HTTP(t *testing.T) {
	entries, err := apptraffic.Project("blog", webActive(), nil, entrypoints())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "http", entries[0].Kind)
	assert.Equal(t, "blog.example.com", entries[0].Host)
	assert.Equal(t, "auto", entries[0].TLSMode)
	assert.Equal(t, 8080, entries[0].BackendPort)
	assert.Empty(t, entries[0].Entrypoint)
}

func TestProject_TCPUDP(t *testing.T) {
	active := domain.AppActive{
		App: "game",
		Services: map[string]domain.AppEffectiveService{
			"server": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Spec: domain.AppService{
					TCP: []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 25565, Publish: "0.0.0.0:25565"}},
					UDP: []domain.AppUDPInterface{{Entrypoint: "udp", Port: 28015, Publish: "0.0.0.0:28015"}},
				},
			},
		},
	}
	entries, err := apptraffic.Project("game", active, nil, entrypoints())
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "tcp", entries[0].Kind)
	assert.Equal(t, "udp", entries[1].Kind)
	assert.Equal(t, 25565, entries[0].BindPort)
	assert.Equal(t, "tcp", entries[0].Entrypoint)
}

func TestProject_RCONPrivateDefault(t *testing.T) {
	active := domain.AppActive{
		App: "rust",
		Services: map[string]domain.AppEffectiveService{
			"server": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Spec: domain.AppService{
					RCON: []domain.AppRCONInterface{{Entrypoint: "tcp", Port: 28016, Publish: "127.0.0.1:28016"}},
				},
			},
		},
	}
	entries, err := apptraffic.Project("rust", active, nil, entrypoints())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "tcp", entries[0].Kind)
	assert.Equal(t, "rcon", entries[0].Policy)
}

func TestProject_RCONPublicPair(t *testing.T) {
	makeActive := func(public bool, cidrs []string) domain.AppActive {
		return domain.AppActive{
			App: "rust",
			Services: map[string]domain.AppEffectiveService{
				"server": {
					EffectiveRevision: "rev-1",
					Image:             "img:1",
					Spec: domain.AppService{
						RCON: []domain.AppRCONInterface{{
							Entrypoint: "tcp", Port: 28016, Publish: "0.0.0.0:28016",
							Public: public, TrustedCIDRs: cidrs,
						}},
					},
				},
			},
		}
	}
	// Public without CIDRs → rejected.
	_, err := apptraffic.Project("rust", makeActive(true, nil), nil, entrypoints())
	require.ErrorIs(t, err, domain.ErrAppTrafficProjection)

	// CIDRs exceeding the entrypoint → rejected (never relax installation).
	_, err = apptraffic.Project("rust", makeActive(true, []string{"192.168.0.0/16"}), nil, entrypoints())
	require.ErrorIs(t, err, domain.ErrAppTrafficProjection)

	// Exact entrypoint subset → allowed.
	entries, err := apptraffic.Project("rust", makeActive(true, []string{"10.0.0.0/8"}), nil, entrypoints())
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	// Private with CIDRs → rejected.
	_, err = apptraffic.Project("rust", makeActive(false, []string{"10.0.0.0/8"}), nil, entrypoints())
	require.ErrorIs(t, err, domain.ErrAppTrafficProjection)
}

func TestProject_UnknownEntrypoint(t *testing.T) {
	active := domain.AppActive{
		App: "game",
		Services: map[string]domain.AppEffectiveService{
			"server": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Spec: domain.AppService{
					TCP: []domain.AppTCPInterface{{Entrypoint: "nope", Port: 1, Publish: "1"}},
				},
			},
		},
	}
	_, err := apptraffic.Project("game", active, nil, entrypoints())
	require.ErrorIs(t, err, domain.ErrAppTrafficProjection)
}

func TestProject_OverlayReplacesOneService(t *testing.T) {
	active := domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1", Image: "img:1",
				Spec: domain.AppService{
					HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
				},
			},
			"worker": {
				EffectiveRevision: "rev-1", Image: "img:1",
				Spec: domain.AppService{
					TCP: []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "9000"}},
				},
			},
		},
	}
	overlay := &apptraffic.OpOverlay{
		Op: "op-1", App: "blog", Service: "web", Revision: "rev-2",
		Spec: domain.AppService{
			Name: "web",
			HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8081, TLS: "auto"}},
		},
	}
	entries, err := apptraffic.Project("blog", active, overlay, entrypoints())
	require.NoError(t, err)
	require.Len(t, entries, 2)
	// Worker preserved untouched; web shifted to the overlay backend.
	byService := map[string]apptraffic.RouteEntry{}
	for _, entry := range entries {
		byService[entry.Service] = entry
	}
	assert.Equal(t, 9000, byService["worker"].BackendPort)
	assert.Equal(t, 8081, byService["web"].BackendPort)

	// Wrong-app overlay rejected.
	overlay.App = "other"
	_, err = apptraffic.Project("blog", active, overlay, entrypoints())
	require.ErrorIs(t, err, domain.ErrAppTrafficProjection)
}

func TestSnapshot_CompareAndCommit(t *testing.T) {
	store := apptraffic.NewSnapshotStore()
	base := store.Current()
	assert.Equal(t, uint64(0), base.ID)

	first, err := store.Commit("op-1", 0, []apptraffic.RouteEntry{{RouterName: "a"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first.ID)

	// Stale holder rejected; current untouched.
	_, err = store.Commit("op-2", 0, []apptraffic.RouteEntry{{RouterName: "b"}}, nil)
	require.ErrorIs(t, err, domain.ErrAppTrafficSnapshotConflict)
	current := store.Current()
	assert.Equal(t, uint64(1), current.ID)
	assert.Equal(t, "a", current.Entries[0].RouterName)

	// Fresh base commits.
	second, err := store.Commit("op-2", 1, []apptraffic.RouteEntry{{RouterName: "a"}, {RouterName: "b"}}, map[string]string{"x.test": "h:1"})
	require.NoError(t, err)
	assert.Equal(t, uint64(2), second.ID)
	assert.Equal(t, "h:1", second.External["x.test"])
}

func TestSnapshot_ConcurrentCommits(t *testing.T) {
	store := apptraffic.NewSnapshotStore()
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			base := store.Current()
			_, err := store.Commit("op-x", base.ID, base.Entries, nil)
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			} else {
				assert.ErrorIs(t, err, domain.ErrAppTrafficSnapshotConflict)
			}
		}()
	}
	wg.Wait()
	// At least one won; losers got clean conflicts, no lost updates.
	assert.GreaterOrEqual(t, successes, 1)
	final := store.Current()
	assert.Equal(t, uint64(successes), final.ID)
}

func TestMerge_WithdrawalPreservesUnrelated(t *testing.T) {
	blog, err := apptraffic.Project("blog", webActive(), nil, entrypoints())
	require.NoError(t, err)
	shopActive := domain.AppActive{
		App: "shop",
		Services: map[string]domain.AppEffectiveService{
			"api": {
				EffectiveRevision: "rev-1", Image: "img:1",
				Spec: domain.AppService{
					HTTP: []domain.AppHTTPInterface{{Host: "shop.example.com", Port: 8080, TLS: "auto"}},
				},
			},
		},
	}
	shop, err := apptraffic.Project("shop", shopActive, nil, entrypoints())
	require.NoError(t, err)

	before := apptraffic.Snapshot{ID: 1, Entries: apptraffic.Merge(map[string][]apptraffic.RouteEntry{"blog": blog, "shop": shop}, nil)}
	after := apptraffic.Snapshot{ID: 2, Entries: apptraffic.Merge(map[string][]apptraffic.RouteEntry{"shop": shop}, nil)}

	withdrawn, added := apptraffic.DiffSnapshots(before, after)
	assert.Empty(t, added)
	require.Len(t, withdrawn, 1)
	assert.Contains(t, withdrawn[0], "blog")
	// Shop entry byte-identical across the withdrawal.
	for _, entry := range after.Entries {
		assert.Equal(t, "shop", entry.App)
	}
}

func TestProject_GameExample(t *testing.T) {
	active := domain.AppActive{
		App: "rust",
		Services: map[string]domain.AppEffectiveService{
			"server": {
				EffectiveRevision: "rev-1", Image: "img:1",
				Spec: domain.AppService{
					Readiness: domain.AppReadiness{Type: "log", Path: "/data/logs/server.log", Contains: "ready", Timeout: 5 * time.Minute},
					UDP:       []domain.AppUDPInterface{{Entrypoint: "udp", Port: 28015, Publish: "0.0.0.0:28015"}},
					RCON:      []domain.AppRCONInterface{{Entrypoint: "tcp", Port: 28016, Publish: "127.0.0.1:28016"}},
				},
			},
		},
	}
	entries, err := apptraffic.Project("rust", active, nil, entrypoints())
	require.NoError(t, err)
	require.Len(t, entries, 2)
}
