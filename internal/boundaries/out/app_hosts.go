package out

// AppHostSource is the shared ACTIVE-derived HTTP host projection for
// traffic, PKI and public-TLS consumers. Implemented by the apptraffic
// host index; read-only, never container IPs.
//
// Fail-closed contract: no apps, no ACTIVE record, stopped intent, or
// unresolved backends yield zero hosts. Installation external and
// management hosts are authorized independently, never through here.
// TLS-mode filtering (auto/always/never) is the consumer's decision:
// the projection preserves TLSMode per host.
type AppHostSource interface {
	// AppHosts returns the sorted served hosts with resolved backends.
	AppHosts() []AppHost
}

// AppHost is one served HTTP host with its owning workload and TLS mode.
type AppHost struct {
	Host    string
	App     string
	Service string
	TLSMode string
}
