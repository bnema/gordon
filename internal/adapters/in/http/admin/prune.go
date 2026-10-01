package admin

import (
	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

// toPruneSummary maps the domain prune report to its wire form.
func toPruneSummary(report domain.PruneReport) dto.PruneSummary {
	return dto.PruneSummaryFromDomain(report)
}
