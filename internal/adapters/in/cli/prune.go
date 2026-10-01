package cli

import (
	"fmt"
	"io"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/bytesize"
)

// cliRenderPruneSummary writes the shared prune verdict summary: counts
// per verdict, what was removed, and the reasons protected or unknown
// candidates were skipped. Dry-run and execution use the same renderer.
func cliRenderPruneSummary(out io.Writer, summary dto.PruneSummary) error {
	action := "Deleted"
	if !summary.Applied {
		action = "Would delete"
	}
	// The wire summary carries the authoritative counts, so the human
	// output can never disagree with the JSON body.
	actionCount := len(summary.Deleted)
	if !summary.Applied {
		actionCount = summary.Eligible
	}
	if err := cliWritef(out, "%s: %d (eligible=%d protected=%d unknown=%d)\n",
		action, actionCount, summary.Eligible, summary.Protected, summary.Unknown); err != nil {
		return err
	}
	if summary.ReclaimedKnown {
		if err := cliWritef(out, "Space reclaimed: %s\n", bytesize.Format(summary.ReclaimedBytes)); err != nil {
			return err
		}
	}
	for _, failure := range summary.Failures {
		line := fmt.Sprintf("failed to delete %s %s: %s", failure.Kind, failure.Ref, failure.Error)
		if err := cliWriteLine(out, cliRenderWarning(line)); err != nil {
			return err
		}
	}
	for _, gap := range summary.Gaps {
		line := fmt.Sprintf("inventory incomplete (%s: %s); dependent candidates were left untouched", gap.Source, gap.Reason)
		if err := cliWriteLine(out, cliRenderWarning(line)); err != nil {
			return err
		}
	}
	return cliRenderSkippedCandidates(out, summary.Candidates)
}

// cliRenderSkippedCandidates lists the not-deleted candidates with their
// first reason, so an operator can see why each resource survived.
func cliRenderSkippedCandidates(out io.Writer, candidates []dto.PruneCandidate) error {
	skipped := make([]dto.PruneCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Verdict == string(domain.PruneVerdictEligible) {
			continue
		}
		skipped = append(skipped, candidate)
	}
	if len(skipped) == 0 {
		return nil
	}
	if err := cliWriteLine(out, cliRenderMuted("Skipped:")); err != nil {
		return err
	}
	for _, candidate := range skipped {
		reason := ""
		if len(candidate.Reasons) > 0 {
			reason = candidate.Reasons[0]
		}
		if err := cliWritef(out, "  %s %s (%s) %s\n", candidate.Kind, candidate.Ref, candidate.Verdict, reason); err != nil {
			return err
		}
	}
	return nil
}
