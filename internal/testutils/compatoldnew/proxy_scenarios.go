package compatoldnew

// ProxyScenarios returns Phase 5 proxy compatibility scenario shells.
func ProxyScenarios() []Scenario {
	return []Scenario{
		proxyScenario("proxy/managed-http-route"),
		proxyScenario("proxy/unknown-host"),
		proxyScenario("proxy/external-route"),
		proxyScenario("proxy/h2c-backend"),
		proxyScenario("proxy/registry-domain-routing"),
		proxyScenario("proxy/body-size-limit"),
		proxyScenario("proxy/zero-downtime-drain"),
		proxyScenario("proxy/access-log-emitted"),
	}
}

func proxyScenario(name string) Scenario {
	return pendingScenario(name, SurfaceProxy, "6.5 Proxy and traffic compatibility", true, "old/new proxy compatibility scenario execution requires real Podman-backed Gordon instances")
}
