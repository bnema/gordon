package edgesnapshot

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/bnema/gordon/internal/domain"
)

// DrainState is the intentionally narrow edge drain report. It has no route,
// container, socket, or component identity fields; identity comes from RPC auth.
type DrainState struct {
	Generation     domain.RouteTargetGeneration
	TargetKey      domain.RouteTargetKey
	InFlight       uint64
	AcknowledgedAt time.Time
	TimeoutReason  string
}

// Validate ensures a drain report is structurally safe to relay. It does not
// assign drain semantics or inspect the authenticated edge identity.
func (s DrainState) Validate() error {
	if s.Generation == 0 {
		return fmt.Errorf("generation is required")
	}
	if !s.TargetKey.Valid() {
		return fmt.Errorf("target key is invalid")
	}
	if len(s.TimeoutReason) > 256 || strings.IndexFunc(s.TimeoutReason, func(r rune) bool {
		return unicode.IsControl(r)
	}) >= 0 {
		return fmt.Errorf("timeout reason is invalid")
	}
	return nil
}

// DrainStateReceiver receives validated drain reports for later orchestration.
type DrainStateReceiver interface {
	ReportDrainState(context.Context, DrainState) error
}
