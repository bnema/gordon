package domain

import "regexp"

type secretRedactionPattern struct {
	re   *regexp.Regexp
	repl string
}

const secretKeyPattern = `[A-Z0-9_]*(?:PASSWORD|TOKEN|SECRET|API[_-]?KEY|DATABASE[_-]?URL)[A-Z0-9_]*` //nolint:gosec // Regex pattern for secret-like key names, not a credential.

var secretValuePatterns = []secretRedactionPattern{
	{
		re:   regexp.MustCompile(`(?i)(\b` + secretKeyPattern + `\b\s*=\s*)(?:"(?:\\.|[^"])*"|'(?:\\.|[^'])*'|[^\s]+)`),
		repl: `${1}[REDACTED]`,
	},
	{
		re:   regexp.MustCompile(`(?i)("` + secretKeyPattern + `"\s*:\s*")((?:\\.|[^"])*)(")`),
		repl: `${1}[REDACTED]${3}`,
	},
	{
		re:   regexp.MustCompile(`(?i)(\bAuthorization\s*:\s*Bearer\s+)([^\s]+)`),
		repl: `${1}[REDACTED]`,
	},
}

// RedactSecrets redacts common secret values from a single log or diagnostic line.
func RedactSecrets(line string) string {
	redacted := line
	for _, pattern := range secretValuePatterns {
		redacted = pattern.re.ReplaceAllString(redacted, pattern.repl)
	}
	return redacted
}

// RedactSecretLines redacts common secret values from log or diagnostic lines.
func RedactSecretLines(lines []string) []string {
	if lines == nil {
		return nil
	}
	redacted := make([]string, len(lines))
	for i, line := range lines {
		redacted[i] = RedactSecrets(line)
	}
	return redacted
}
