package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

func formatDeployFailure(err error) error {
	var deployErr *domain.DeployFailureError
	if errors.As(err, &deployErr) {
		return errors.New(renderDeployFailure(deployErr.Summary, deployErr.Cause, deployErr.Hint, deployErr.Logs))
	}

	var httpErr *remote.HTTPError
	if errors.As(err, &httpErr) && (httpErr.Structured || httpErr.Cause != "" || httpErr.Hint != "" || len(httpErr.Logs) > 0) {
		return errors.New(renderDeployFailure(httpErr.Body, httpErr.Cause, httpErr.Hint, httpErr.Logs))
	}

	return fmt.Errorf("failed to deploy: %w", err)
}

func renderDeployFailure(summary, cause, hint string, logs []string) string {
	if summary == "" {
		summary = "failed to deploy"
	}

	var b strings.Builder
	if summary == "failed to deploy" {
		b.WriteString(summary)
	} else {
		b.WriteString("failed to deploy: ")
		b.WriteString(summary)
	}

	if cause != "" {
		b.WriteString("\nCause: ")
		b.WriteString(cause)
	}
	if hint != "" {
		b.WriteString("\nHint: ")
		b.WriteString(hint)
	}
	if len(logs) > 0 {
		b.WriteString("\n\nRecent container logs:")
		for _, line := range logs {
			b.WriteString("\n  ")
			b.WriteString(line)
		}
	}

	return b.String()
}
