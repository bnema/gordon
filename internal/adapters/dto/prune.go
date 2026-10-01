package dto

import "github.com/bnema/gordon/internal/domain"

// PruneCandidate is one planned prune candidate with its verdict and the
// stable reason codes behind it.
type PruneCandidate struct {
	Kind    string   `json:"kind"`
	Ref     string   `json:"ref"`
	Verdict string   `json:"verdict"`
	Reasons []string `json:"reasons"`
}

// PruneGap is one inventory that could not be read in full. A gap never
// fails the operation; it makes the dependent candidates unknown.
type PruneGap struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// PruneFailure is one deletion that failed without aborting the run.
type PruneFailure struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Error string `json:"error"`
}

// PruneSummary is the shared prune report shape: dry-run and execution
// return the same structure, distinguished by Applied.
type PruneSummary struct {
	// Applied is false for a dry run, where nothing was deleted.
	Applied bool `json:"applied"`
	// Eligible, Protected, and Unknown count the planned candidates.
	Eligible  int `json:"eligible"`
	Protected int `json:"protected"`
	Unknown   int `json:"unknown"`
	// Candidates lists every planned candidate with its verdict, so an
	// operator can see why each resource was kept or skipped.
	Candidates []PruneCandidate `json:"candidates"`
	// Deleted lists exactly the identities that were removed.
	Deleted []PruneCandidate `json:"deleted"`
	// Failures lists deletions that failed without aborting the run.
	Failures []PruneFailure `json:"failures,omitempty"`
	// Gaps lists every inventory that could not be read in full.
	Gaps []PruneGap `json:"gaps,omitempty"`
	// ReclaimedBytes is set only when the adapter can attribute exact
	// bytes to the deletions it performed.
	ReclaimedBytes int64 `json:"reclaimed_bytes"`
	ReclaimedKnown bool  `json:"reclaimed_known"`
}

// CountByKind counts the eligible candidates of one resource kind.
func (s PruneSummary) CountByKind(kind domain.PruneResourceKind) int {
	count := 0
	for _, candidate := range s.Candidates {
		if candidate.Kind == string(kind) && candidate.Verdict == string(domain.PruneVerdictEligible) {
			count++
		}
	}
	return count
}

// PruneSummaryFromDomain maps the domain prune report to its wire form.
// Dry-run and execution share the shape, distinguished by Applied.
func PruneSummaryFromDomain(report domain.PruneReport) PruneSummary {
	summary := PruneSummary{
		Applied:        report.Applied,
		Eligible:       report.CountByVerdict(domain.PruneVerdictEligible),
		Protected:      report.CountByVerdict(domain.PruneVerdictProtected),
		Unknown:        report.CountByVerdict(domain.PruneVerdictUnknown),
		ReclaimedBytes: report.ReclaimedBytes,
		ReclaimedKnown: report.ReclaimedKnown,
	}
	for _, candidate := range report.Candidates {
		summary.Candidates = append(summary.Candidates, pruneCandidateFromDomain(candidate))
	}
	for _, candidate := range report.Deleted {
		summary.Deleted = append(summary.Deleted, pruneCandidateFromDomain(candidate))
	}
	for _, failure := range report.Failures {
		summary.Failures = append(summary.Failures, PruneFailure{
			Kind: string(failure.Kind), Ref: failure.Ref, Error: failure.Err,
		})
	}
	for _, gap := range report.Gaps {
		summary.Gaps = append(summary.Gaps, PruneGap{
			Source: string(gap.Source), Reason: string(gap.Reason), Detail: gap.Detail,
		})
	}
	return summary
}

func pruneCandidateFromDomain(candidate domain.PruneCandidateReport) PruneCandidate {
	reasons := make([]string, 0, len(candidate.Reasons))
	for _, reason := range candidate.Reasons {
		reasons = append(reasons, string(reason))
	}
	return PruneCandidate{
		Kind: string(candidate.Kind), Ref: candidate.Ref, Verdict: string(candidate.Verdict), Reasons: reasons,
	}
}
