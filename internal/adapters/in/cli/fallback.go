package cli

import "strings"

func shouldFallbackToLocal(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())

	// Retry local signal mode when remote API is unavailable or misrouted.
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "timeout") {
		return true
	}

	if strings.Contains(msg, "500") ||
		strings.Contains(msg, "501") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") ||
		strings.Contains(msg, "505") {
		return true
	}

	if strings.Contains(msg, "internal server error") ||
		strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "gateway timeout") {
		return true
	}

	return false
}
