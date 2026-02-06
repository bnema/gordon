package cli

import "strings"

var (
	fallbackNetworkErrors = []string{
		"connection refused",
		"no such host",
		"i/o timeout",
		"timeout",
	}

	fallbackStatusCodes = []string{
		"500", "501", "502", "503", "504", "505",
	}

	fallbackGatewayErrors = []string{
		"internal server error",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
	}
)

func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func shouldFallbackToLocal(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())

	return containsAny(msg, fallbackNetworkErrors) ||
		containsAny(msg, fallbackStatusCodes) ||
		containsAny(msg, fallbackGatewayErrors)
}
