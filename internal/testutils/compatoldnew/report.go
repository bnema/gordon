package compatoldnew

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Report struct {
	Total           int       `json:"total"`
	Failed          int       `json:"failed"`
	Failures        []Failure `json:"failures"`
	BaselineCommit  string    `json:"baselineCommit,omitempty"`
	CandidateCommit string    `json:"candidateCommit,omitempty"`
	RerunCommand    string    `json:"rerunCommand,omitempty"`
}

// ReportMetadata identifies the exact old/new inputs and focused rerun for a
// scenario report.
type ReportMetadata struct {
	BaselineCommit  string
	CandidateCommit string
	RerunCommand    string
}

// SideResult associates a captured artifact with the target that produced it.
// ValidationError is deliberately separate from the artifact: observations
// must be compared and persisted before a broken contract fails the scenario.
type SideResult struct {
	Side            string
	Artifact        Artifact
	ValidationError error
}

// CompareSideResults routes every comparison through Compare and writes the
// report artifacts even when the values match, so failures are diagnosable.
func CompareSideResults(old, new SideResult, allow *AllowlistedDifference, artifactDir string) (Report, error) {
	return CompareSideResultsWithMetadata(old, new, allow, artifactDir, ReportMetadata{})
}

// CompareSideResultsWithMetadata compares artifacts through Compare and adds
// reproducibility information to the report written for the scenario.
func CompareSideResultsWithMetadata(old, new SideResult, allow *AllowlistedDifference, artifactDir string, metadata ReportMetadata) (Report, error) {
	if old.Side != SideOld || new.Side != SideNew {
		return Report{}, fmt.Errorf("compare sides: expected old and new results")
	}
	if old.Artifact == nil || new.Artifact == nil {
		return Report{}, fmt.Errorf("compare sides: both artifacts are required")
	}
	if artifactDir == "" {
		return Report{}, fmt.Errorf("compare sides: report artifact directory is required")
	}
	report := NewReport(Compare(old.Artifact, new.Artifact, allow), 1)
	report.BaselineCommit = metadata.BaselineCommit
	report.CandidateCommit = metadata.CandidateCommit
	report.RerunCommand = metadata.RerunCommand
	if err := writeSideArtifacts(artifactDir, old, new); err != nil {
		return Report{}, fmt.Errorf("compare sides write side artifacts: %w", err)
	}
	if err := report.WriteArtifactDirectory(artifactDir); err != nil {
		return Report{}, fmt.Errorf("compare sides write report: %w", err)
	}
	if err := validationErrors(old, new); err != nil {
		return report, err
	}
	return report, nil
}

func validationErrors(old, new SideResult) error {
	var errors []string
	for _, result := range []SideResult{old, new} {
		if result.ValidationError != nil {
			errors = append(errors, fmt.Sprintf("%s validation: %v", result.Side, result.ValidationError))
		}
	}
	if len(errors) == 0 {
		return nil
	}
	return fmt.Errorf("compatibility contract failure after report emission: %s", strings.Join(errors, "; "))
}

func NewReport(failures []Failure, total int) Report {
	return Report{Total: total, Failed: len(failures), Failures: failures}
}

func (r Report) ConsoleSummary() string {
	if r.Failed == 0 {
		return fmt.Sprintf("compat: %d compared, 0 failed", r.Total)
	}
	return fmt.Sprintf("compat: %d compared, %d failed", r.Total, r.Failed)
}

func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

func (r Report) WriteArtifactDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(redactArtifactValue(r), "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateFile(filepath.Join(dir, "compat-report.json"), b); err != nil {
		return err
	}
	var diffs []string
	for _, f := range r.Failures {
		diffs = append(diffs, NormalizedDiff(redactArtifactValue(f.OldValue), redactArtifactValue(f.NewValue)))
	}
	return writePrivateFile(filepath.Join(dir, "normalized.diff"), []byte(strings.Join(diffs, "\n")))
}

func writeSideArtifacts(dir string, old, new SideResult) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, side := range []struct {
		name  string
		value any
	}{
		{"old.raw.json", old.Artifact.RawValue()},
		{"new.raw.json", new.Artifact.RawValue()},
		{"old.normalized.json", old.Artifact.NormalizedValue()},
		{"new.normalized.json", new.Artifact.NormalizedValue()},
	} {
		body, err := json.MarshalIndent(redactArtifactValue(side.value), "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", side.name, err)
		}
		if err := writePrivateFile(filepath.Join(dir, side.name), body); err != nil {
			return fmt.Errorf("write %s: %w", side.name, err)
		}
	}
	return nil
}

func writePrivateFile(path string, body []byte) error {
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

var sensitiveArtifactKey = regexp.MustCompile(`(?i)(token|authorization|credential|password|secret)`)
var artifactBearer = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[^\s"']+`)
var artifactSensitiveValue = regexp.MustCompile(`(?i)\b(?:token|authorization|credential|password|secret)=[^\s"']+`)
var artifactJWT = regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\b`)

// redactArtifactValue serializes through JSON so structs and maps receive the
// same recursive secret filtering before any artifact reaches disk.
func redactArtifactValue(value any) any {
	body, err := json.Marshal(value)
	if err != nil {
		return "<unserializable artifact>"
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "<unserializable artifact>"
	}
	return redactArtifactJSON(decoded)
}

func redactArtifactJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveArtifactKey.MatchString(key) {
				out[key] = "<redacted>"
				continue
			}
			out[key] = redactArtifactJSON(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactArtifactJSON(child)
		}
		return out
	case string:
		typed = artifactBearer.ReplaceAllString(typed, "<redacted authorization>")
		typed = artifactSensitiveValue.ReplaceAllString(typed, "<redacted>")
		return artifactJWT.ReplaceAllString(typed, "<redacted token>")
	default:
		return value
	}
}
