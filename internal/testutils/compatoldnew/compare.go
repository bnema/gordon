package compatoldnew

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// AllowlistedDifference documents an accepted compatibility delta.
type AllowlistedDifference struct {
	Reason      string `json:"reason"`
	SpecSection string `json:"specSection"`
}

// Failure describes a compatibility mismatch.
type Failure struct {
	OldValue         any                    `json:"oldValue"`
	NewValue         any                    `json:"newValue"`
	Source           string                 `json:"source"`
	Level            ComparisonLevel        `json:"level"`
	SuggestedCommand string                 `json:"suggestedNextDebuggingCommand"`
	Allowlist        *AllowlistedDifference `json:"allowlist,omitempty"`
	Problem          string                 `json:"problem,omitempty"`
}

func Compare(oldA, newA Artifact, allow *AllowlistedDifference) []Failure {
	if failure := metadataFailure(oldA, newA); failure != nil {
		return []Failure{*failure}
	}
	if oldA.Level() == LevelAllowlistedDifference {
		if allow != nil && allow.Reason != "" && allow.SpecSection != "" {
			return nil
		}
		return []Failure{{OldValue: oldA.NormalizedValue(), NewValue: newA.NormalizedValue(), Source: oldA.Source(), Level: oldA.Level(), SuggestedCommand: suggest(oldA), Allowlist: allow, Problem: allowlistProblem(allow)}}
	}
	oldV, newV := oldA.NormalizedValue(), newA.NormalizedValue()
	if oldA.Level() == LevelPresence {
		if present(oldV) == present(newV) {
			return nil
		}
	} else if reflect.DeepEqual(oldV, newV) {
		return nil
	}
	return []Failure{{OldValue: oldV, NewValue: newV, Source: oldA.Source(), Level: oldA.Level(), SuggestedCommand: suggest(oldA), Allowlist: allow}}
}

func present(v any) bool { return v != nil && fmt.Sprint(v) != "" }

func metadataFailure(oldA, newA Artifact) *Failure {
	switch {
	case oldA.ArtifactType() != newA.ArtifactType():
		return newMetadataFailure(oldA, "artifact type mismatch: old %q, new %q", oldA.ArtifactType(), newA.ArtifactType())
	case oldA.Source() != newA.Source():
		return newMetadataFailure(oldA, "artifact source mismatch: old %q, new %q", oldA.Source(), newA.Source())
	case oldA.Level() != newA.Level():
		return newMetadataFailure(oldA, "comparison level mismatch: old %q, new %q", oldA.Level(), newA.Level())
	default:
		return nil
	}
}

func newMetadataFailure(oldA Artifact, problem string, args ...any) *Failure {
	return &Failure{
		Source:           oldA.Source(),
		Level:            oldA.Level(),
		SuggestedCommand: suggest(oldA),
		Problem:          fmt.Sprintf(problem, args...),
	}
}

func allowlistProblem(allow *AllowlistedDifference) string {
	if allow == nil {
		return "allowlisted difference requires an allowlist with non-empty reason and spec section"
	}
	if allow.Reason == "" && allow.SpecSection == "" {
		return "allowlisted difference allowlist missing reason and spec section"
	}
	if allow.Reason == "" {
		return "allowlisted difference allowlist missing reason"
	}
	return "allowlisted difference allowlist missing spec section"
}

func suggest(a Artifact) string {
	switch a.ArtifactType() {
	case "cli":
		return a.Source()
	case "http":
		return "curl -i " + a.Source()
	default:
		return "inspect artifact source: " + a.Source()
	}
}

func NormalizedDiff(oldV, newV any) string {
	ob, _ := json.MarshalIndent(oldV, "", "  ")
	nb, _ := json.MarshalIndent(newV, "", "  ")
	if string(ob) == string(nb) {
		return ""
	}
	return "--- old\n" + string(ob) + "\n+++ new\n" + string(nb) + "\n"
}
