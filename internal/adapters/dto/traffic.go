package dto

import "github.com/bnema/gordon/internal/domain"

type TrafficStatusResponse struct {
	LastReloadStatus string                    `json:"last_reload_status"`
	LastReloadError  string                    `json:"last_reload_error,omitempty"`
	EntryPoints      []TrafficEntryPointStatus `json:"entrypoints"`
	Routers          []TrafficRouterStatus     `json:"routers"`
	Services         []TrafficServiceStatus    `json:"services"`
	Counters         TrafficCounters           `json:"counters"`
}

type TrafficEntryPointStatus struct {
	Name                 string                    `json:"name"`
	Address              string                    `json:"address"`
	Protocol             domain.EntryPointProtocol `json:"protocol"`
	Active               bool                      `json:"active"`
	ActiveTCPConnections int64                     `json:"active_tcp_connections"`
	ActiveUDPSessions    int64                     `json:"active_udp_sessions"`
	TotalAccepted        int64                     `json:"total_accepted"`
	TotalRefused         int64                     `json:"total_refused"`
	TotalErrors          int64                     `json:"total_errors"`
	BytesIn              int64                     `json:"bytes_in"`
	BytesOut             int64                     `json:"bytes_out"`
}

type TrafficRouterStatus struct {
	Name                 string                `json:"name"`
	EntryPoint           string                `json:"entrypoint"`
	Protocol             domain.RouterProtocol `json:"protocol"`
	Rule                 TrafficRule           `json:"rule"`
	Service              string                `json:"service"`
	Active               bool                  `json:"active"`
	ActiveTCPConnections int64                 `json:"active_tcp_connections"`
	ActiveUDPSessions    int64                 `json:"active_udp_sessions"`
	TotalAccepted        int64                 `json:"total_accepted"`
	TotalRefused         int64                 `json:"total_refused"`
	TotalErrors          int64                 `json:"total_errors"`
	BytesIn              int64                 `json:"bytes_in"`
	BytesOut             int64                 `json:"bytes_out"`
}

type TrafficServiceStatus struct {
	Name     string                 `json:"name"`
	Active   bool                   `json:"active"`
	Backends []TrafficBackendStatus `json:"backends"`
}

type TrafficRule struct {
	Host string `json:"host,omitempty"`
	SNI  string `json:"sni,omitempty"`
}

type TrafficBackendStatus struct {
	Name                 string                 `json:"name"`
	Host                 string                 `json:"host"`
	Port                 int                    `json:"port"`
	Protocol             domain.NetworkProtocol `json:"protocol"`
	Active               bool                   `json:"active"`
	ActiveTCPConnections int64                  `json:"active_tcp_connections"`
	ActiveUDPSessions    int64                  `json:"active_udp_sessions"`
	TotalAccepted        int64                  `json:"total_accepted"`
	TotalRefused         int64                  `json:"total_refused"`
	TotalErrors          int64                  `json:"total_errors"`
	BytesIn              int64                  `json:"bytes_in"`
	BytesOut             int64                  `json:"bytes_out"`
}

type TrafficCounters struct {
	ActiveTCPConnections int64 `json:"active_tcp_connections"`
	ActiveUDPSessions    int64 `json:"active_udp_sessions"`
	TotalAccepted        int64 `json:"total_accepted"`
	TotalRefused         int64 `json:"total_refused"`
	TotalErrors          int64 `json:"total_errors"`
	BytesIn              int64 `json:"bytes_in"`
	BytesOut             int64 `json:"bytes_out"`
}

func TrafficStatusFromDomain(status domain.TrafficStatus) TrafficStatusResponse {
	return TrafficStatusResponse{
		LastReloadStatus: status.LastReloadStatus,
		LastReloadError:  status.LastReloadError,
		EntryPoints:      trafficEntryPointsFromDomain(status.EntryPoints),
		Routers:          trafficRoutersFromDomain(status.Routers),
		Services:         trafficServicesFromDomain(status.Services),
		Counters:         trafficCountersFromDomain(status.Counters),
	}
}

func trafficEntryPointsFromDomain(values []domain.EntryPointStatus) []TrafficEntryPointStatus {
	out := make([]TrafficEntryPointStatus, 0, len(values))
	for _, value := range values {
		out = append(out, TrafficEntryPointStatus{
			Name: value.Name, Address: value.Address, Protocol: value.Protocol, Active: value.Active,
			ActiveTCPConnections: value.ActiveTCPConnections, ActiveUDPSessions: value.ActiveUDPSessions,
			TotalAccepted: value.TotalAccepted, TotalRefused: value.TotalRefused, TotalErrors: value.TotalErrors,
			BytesIn: value.BytesIn, BytesOut: value.BytesOut,
		})
	}
	return out
}

func trafficRoutersFromDomain(values []domain.TrafficRouterStatus) []TrafficRouterStatus {
	out := make([]TrafficRouterStatus, 0, len(values))
	for _, value := range values {
		out = append(out, TrafficRouterStatus{
			Name: value.Name, EntryPoint: value.EntryPoint, Protocol: value.Protocol, Rule: TrafficRule{Host: value.Rule.Host, SNI: value.Rule.SNI}, Service: value.Service, Active: value.Active,
			ActiveTCPConnections: value.ActiveTCPConnections, ActiveUDPSessions: value.ActiveUDPSessions,
			TotalAccepted: value.TotalAccepted, TotalRefused: value.TotalRefused, TotalErrors: value.TotalErrors,
			BytesIn: value.BytesIn, BytesOut: value.BytesOut,
		})
	}
	return out
}

func trafficServicesFromDomain(values []domain.TrafficServiceStatus) []TrafficServiceStatus {
	out := make([]TrafficServiceStatus, 0, len(values))
	for _, value := range values {
		out = append(out, TrafficServiceStatus{Name: value.Name, Active: value.Active, Backends: trafficBackendsFromDomain(value.Backends)})
	}
	return out
}

func trafficBackendsFromDomain(values []domain.TrafficBackendStatus) []TrafficBackendStatus {
	out := make([]TrafficBackendStatus, 0, len(values))
	for _, value := range values {
		out = append(out, TrafficBackendStatus{
			Name: value.Name, Host: value.Host, Port: value.Port, Protocol: value.Protocol, Active: value.Active,
			ActiveTCPConnections: value.ActiveTCPConnections, ActiveUDPSessions: value.ActiveUDPSessions,
			TotalAccepted: value.TotalAccepted, TotalRefused: value.TotalRefused, TotalErrors: value.TotalErrors,
			BytesIn: value.BytesIn, BytesOut: value.BytesOut,
		})
	}
	return out
}

func trafficCountersFromDomain(value domain.TrafficCounters) TrafficCounters {
	return TrafficCounters{
		ActiveTCPConnections: value.ActiveTCPConnections,
		ActiveUDPSessions:    value.ActiveUDPSessions,
		TotalAccepted:        value.TotalAccepted,
		TotalRefused:         value.TotalRefused,
		TotalErrors:          value.TotalErrors,
		BytesIn:              value.BytesIn,
		BytesOut:             value.BytesOut,
	}
}
