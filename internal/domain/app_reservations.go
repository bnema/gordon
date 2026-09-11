package domain

import (
	"fmt"
	"sort"
)

// ReservationsFor computes the global listener claims for a normalized
// spec. It is the single owner of reservation derivation; the apps and
// deployment use cases delegate to it.
func ReservationsFor(spec AppSpec) []AppListenerReservation {
	var reservations []AppListenerReservation
	for _, svc := range spec.Services {
		for _, h := range svc.HTTP {
			reservations = append(reservations, AppListenerReservation{
				Proto:   "http",
				Host:    h.Host,
				Service: svc.Name,
				App:     spec.Name,
			})
		}
		for _, t := range svc.TCP {
			host, port, err := ParsePublish(t.Publish)
			if err != nil {
				continue
			}
			reservations = append(reservations, AppListenerReservation{
				Proto:   "tcp",
				IP:      reservationIP(host),
				Port:    port,
				Service: svc.Name,
				App:     spec.Name,
			})
		}
		for _, u := range svc.UDP {
			host, port, err := ParsePublish(u.Publish)
			if err != nil {
				continue
			}
			reservations = append(reservations, AppListenerReservation{
				Proto:   "udp",
				IP:      reservationIP(host),
				Port:    port,
				Service: svc.Name,
				App:     spec.Name,
			})
		}
	}
	sort.Slice(reservations, func(i, j int) bool {
		if reservations[i].Proto != reservations[j].Proto {
			return reservations[i].Proto < reservations[j].Proto
		}
		if reservations[i].Host != reservations[j].Host {
			return reservations[i].Host < reservations[j].Host
		}
		if reservations[i].Port != reservations[j].Port {
			return reservations[i].Port < reservations[j].Port
		}
		return reservations[i].IP < reservations[j].IP
	})
	return reservations
}

// reservationIP maps an empty publish host to the wildcard entry.
func reservationIP(host string) string {
	if host == "" {
		return "dual"
	}
	return host
}

// CheckReservations validates a candidate reservation set against the
// global checkpoint plus wildcard-aware overlay rules. Same-app entries
// in existing are retained until withdrawal, so the candidate's own app
// entries never self-conflict. Intra-candidate claims overlap like
// cross-app claims: a wildcard L4 bind collides with a specific bind on
// the same proto and port.
func CheckReservations(existing []AppListenerReservation, candidate []AppListenerReservation, app string) error {
	// Duplicate and overlapping claims within the candidate itself.
	for i, res := range candidate {
		for _, other := range candidate[:i] {
			if ReservationsOverlap(res, other) {
				return fmt.Errorf(
					"%w: %s claims %s twice (service %s vs %s)",
					ErrAppReservationConflict,
					app, describeReservation(res), other.Service, res.Service,
				)
			}
		}
	}
	for _, res := range candidate {
		for _, other := range existing {
			if other.App == app {
				continue
			}
			if ReservationsOverlap(res, other) {
				return fmt.Errorf(
					"%w: %s/%s conflicts with %s/%s on %s",
					ErrAppReservationConflict,
					app, res.Service, other.App, other.Service, describeReservation(res),
				)
			}
		}
	}
	return nil
}

// ReservationsOverlap reports wildcard/specific conflicts within one
// proto namespace.
func ReservationsOverlap(a, b AppListenerReservation) bool {
	if a.Proto != b.Proto {
		return false
	}
	if a.Proto == "http" {
		return a.Host == b.Host
	}
	if a.Port != b.Port {
		return false
	}
	if IsReservationWildcard(a.IP) || IsReservationWildcard(b.IP) {
		return true
	}
	return a.IP == b.IP
}

// IsReservationWildcard matches wildcard and dual-family entries.
func IsReservationWildcard(ip string) bool {
	return ip == "" || ip == "0.0.0.0" || ip == "::" || ip == "dual"
}

// describeReservation renders a human-readable claim.
func describeReservation(res AppListenerReservation) string {
	if res.Proto == "http" {
		return "http://" + res.Host
	}
	return fmt.Sprintf("%s://%s:%d", res.Proto, res.IP, res.Port)
}
