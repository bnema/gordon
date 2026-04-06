package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func formatDeployFailure(err error) error {
	var deployErr *domain.DeployFailureError
	if errors.As(err, &deployErr) {
		return fmt.Errorf("%s: %w", renderDeployFailure(deployErr.Summary, deployErr.Cause, deployErr.Hint, deployErr.Logs), deployErr)
	}

	var httpErr *remote.HTTPError
	if errors.As(err, &httpErr) && (httpErr.Structured || httpErr.Cause != "" || httpErr.Hint != "" || len(httpErr.Logs) > 0) {
		return fmt.Errorf("%s: %w", renderDeployFailure(httpErr.Body, httpErr.Cause, httpErr.Hint, httpErr.Logs), httpErr)
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
	cleanedLogs := make([]string, 0, len(logs))
	for _, line := range logs {
		if cleanedLine := sanitizeDeployLogLine(line); cleanedLine != "" {
			cleanedLogs = append(cleanedLogs, cleanedLine)
		}
	}
	if len(cleanedLogs) > 0 {
		b.WriteString("\n\nRecent container logs:")
		for _, cleanedLine := range cleanedLogs {
			b.WriteString("\n  ")
			b.WriteString(cleanedLine)
		}
	}

	return b.String()
}

func sanitizeDeployLogLine(line string) string {
	line = ansiEscapePattern.ReplaceAllString(line, "")
	line = strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return r
		case !unicode.IsPrint(r):
			return -1
		default:
			return r
		}
	}, line)
	return strings.TrimSpace(line)
}
