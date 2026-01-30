package domain

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

// envDomainRegex validates domain names used for env secrets.
// Allows: alphanumeric, dots, hyphens, colons (for ports), and forward slashes (for paths).
var envDomainRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*[a-zA-Z0-9]$|^[a-zA-Z0-9]$`)
var envKeyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SanitizeDomainForEnvFile validates and sanitizes a domain name for env file storage.
// Returns a safe domain string suitable for filenames.
func SanitizeDomainForEnvFile(domainName string) (string, error) {
	if domainName == "" {
		return "", ErrPathTraversal
	}

	if strings.Contains(domainName, "..") {
		return "", ErrPathTraversal
	}

	if !envDomainRegex.MatchString(domainName) {
		return "", ErrPathTraversal
	}

	safeDomain := strings.ReplaceAll(domainName, ".", "_")
	safeDomain = strings.ReplaceAll(safeDomain, ":", "_")
	safeDomain = strings.ReplaceAll(safeDomain, "/", "_")
	return safeDomain, nil
}

// ParseEnvData parses env data into a key-value map.
func ParseEnvData(data []byte) (map[string]string, error) {
	secrets := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if len(value) >= 2 {
			if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) ||
				(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
				value = value[1 : len(value)-1]
			}
		}

		if key != "" {
			secrets[key] = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return secrets, nil
}

// ValidateEnvKey validates an env key for storage.
func ValidateEnvKey(key string) error {
	if key == "" {
		return ErrPathTraversal
	}
	if strings.Contains(key, "..") || strings.ContainsAny(key, "/\\") {
		return ErrPathTraversal
	}
	if !envKeyRegex.MatchString(key) {
		return ErrPathTraversal
	}
	return nil
}
