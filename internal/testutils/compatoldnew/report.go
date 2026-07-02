package compatoldnew

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Report struct {
	Total    int       `json:"total"`
	Failed   int       `json:"failed"`
	Failures []Failure `json:"failures"`
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
	b, err := r.JSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "compat-report.json"), b, 0o600); err != nil {
		return err
	}
	var diffs []string
	for _, f := range r.Failures {
		diffs = append(diffs, NormalizedDiff(f.OldValue, f.NewValue))
	}
	return os.WriteFile(filepath.Join(dir, "normalized.diff"), []byte(strings.Join(diffs, "\n")), 0o600)
}
