package in

import "github.com/bnema/gordon/internal/domain"

// TrafficStatusService exposes runtime traffic-plane status.
type TrafficStatusService interface {
	Status() domain.TrafficStatus
}
