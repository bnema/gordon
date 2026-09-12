package apptraffic_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
)

// l4 builds installation entrypoint policies. An empty address omits the
// entrypoint, so tests exercise only the transports they configure.
func l4(tcpAddress, udpAddress string) map[string]apptraffic.EntrypointPolicy {
	policies := map[string]apptraffic.EntrypointPolicy{}
	if tcpAddress != "" {
		policies["tcp"] = apptraffic.EntrypointPolicy{
			Name: "tcp", Address: tcpAddress, Protocol: domain.EntryPointProtocolTCP,
			TrustedCIDRs: []string{"10.0.0.0/8"},
		}
	}
	if udpAddress != "" {
		policies["udp"] = apptraffic.EntrypointPolicy{
			Name: "udp", Address: udpAddress, Protocol: domain.EntryPointProtocolUDP,
		}
	}
	return policies
}

func webActive() domain.AppActive {
	return domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Container:         "c-web",
				BackendBinds:      map[int]int{8080: 18080},
				Spec: domain.AppService{
					HTTP: []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
				},
			},
		},
	}
}

func TestProject_HTTP(t *testing.T) {
	entries, err := apptraffic.Project("blog", webActive(), nil)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "http", entries[0].Kind)
	assert.Equal(t, "blog.example.com", entries[0].Host)
	assert.Equal(t, "auto", entries[0].TLSMode)
	assert.Equal(t, 8080, entries[0].Backend.ContainerPort)
	assert.True(t, entries[0].Backend.Resolved())
	assert.Equal(t, "127.0.0.1", entries[0].Backend.Host)
	assert.Equal(t, 18080, entries[0].Backend.Port)
	assert.Equal(t, "c-web", entries[0].Backend.ContainerID)
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
	entries, err := apptraffic.Project("game", active, l4("0.0.0.0:25565", "0.0.0.0:28015"))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "tcp", entries[0].Kind)
	assert.Equal(t, "udp", entries[1].Kind)
	assert.Equal(t, 25565, entries[0].BindPort)
	assert.Equal(t, "tcp", entries[0].Entrypoint)
}

// TestProject_SamePortTCPUDPResolvesDistinctBackends proves the same
// container port number on TCP and UDP resolves to two distinct loopback
// backends from their respective bind maps.
func TestProject_SamePortTCPUDPResolvesDistinctBackends(t *testing.T) {
	active := domain.AppActive{
		App: "game",
		Services: map[string]domain.AppEffectiveService{
			"server": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Container:         "c-game",
				BackendBinds:      map[int]int{9000: 19000},
				UDPBackendBinds:   map[int]int{9000: 19001},
				Spec: domain.AppService{
					TCP: []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "0.0.0.0:9000"}},
					UDP: []domain.AppUDPInterface{{Entrypoint: "udp", Port: 9000, Publish: "0.0.0.0:9000"}},
				},
			},
		},
	}
	entries, err := apptraffic.Project("game", active, l4("0.0.0.0:9000", "0.0.0.0:9000"))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	byKind := map[string]apptraffic.RouteEntry{}
	for _, entry := range entries {
		byKind[entry.Kind] = entry
	}
	require.Contains(t, byKind, "tcp")
	require.Contains(t, byKind, "udp")
	assert.True(t, byKind["tcp"].Backend.Resolved())
	assert.True(t, byKind["udp"].Backend.Resolved())
	assert.Equal(t, 9000, byKind["tcp"].Backend.ContainerPort)
	assert.Equal(t, 9000, byKind["udp"].Backend.ContainerPort)
	assert.Equal(t, 19000, byKind["tcp"].Backend.Port)
	assert.Equal(t, 19001, byKind["udp"].Backend.Port)
	assert.NotEqual(t, byKind["tcp"].Backend.Port, byKind["udp"].Backend.Port)
}

func TestProject_TCPAsOrdinaryRCON(t *testing.T) {
	// RCON is ordinary TCP: no special kind or policy tag.
	active := domain.AppActive{
		App: "rust",
		Services: map[string]domain.AppEffectiveService{
			"server": {
				EffectiveRevision: "rev-1",
				Image:             "img:1",
				Spec: domain.AppService{
					TCP: []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 28016, Publish: "0.0.0.0:28016"}},
				},
			},
		},
	}
	entries, err := apptraffic.Project("rust", active, l4("0.0.0.0:28016", ""))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "tcp", entries[0].Kind)
	assert.Equal(t, 28016, entries[0].Backend.ContainerPort)
	assert.False(t, entries[0].Backend.Resolved(), "no recorded bind: fail closed")
}

// TestProject_RejectsPublishMismatch proves a declared bind that differs
// from the entrypoint listener is refused rather than silently widened to
// the entrypoint address.
func TestProject_RejectsPublishMismatch(t *testing.T) {
	cases := []struct {
		name       string
		entrypoint apptraffic.EntrypointPolicy
		publish    string
	}{
		{
			name:       "loopback-vs-wildcard",
			entrypoint: apptraffic.EntrypointPolicy{Name: "tcp", Address: "0.0.0.0:25432", Protocol: domain.EntryPointProtocolTCP},
			publish:    "127.0.0.1:15432",
		},
		{
			name:       "same-host-different-port",
			entrypoint: apptraffic.EntrypointPolicy{Name: "tcp", Address: "0.0.0.0:25432", Protocol: domain.EntryPointProtocolTCP},
			publish:    "0.0.0.0:15432",
		},
		{
			name:       "protocol-mismatch",
			entrypoint: apptraffic.EntrypointPolicy{Name: "tcp", Address: "0.0.0.0:25432", Protocol: domain.EntryPointProtocolUDP},
			publish:    "0.0.0.0:25432",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			active := domain.AppActive{
				App: "game",
				Services: map[string]domain.AppEffectiveService{
					"server": {
						EffectiveRevision: "rev-1", Image: "img:1",
						Spec: domain.AppService{
							TCP: []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 25432, Publish: tc.publish}},
						},
					},
				},
			}
			_, err := apptraffic.Project("game", active, map[string]apptraffic.EntrypointPolicy{"tcp": tc.entrypoint})
			require.ErrorIs(t, err, domain.ErrAppTrafficProjection)
		})
	}
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
	_, err := apptraffic.Project("game", active, l4("0.0.0.0:25565", ""))
	require.ErrorIs(t, err, domain.ErrAppTrafficProjection)
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
					TCP:       []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 28016, Publish: "0.0.0.0:28016"}},
				},
			},
		},
	}
	entries, err := apptraffic.Project("rust", active, l4("0.0.0.0:28016", "0.0.0.0:28015"))
	require.NoError(t, err)
	require.Len(t, entries, 2)
}
