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
var containerNameRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// SanitizeDomainForEnvFile validates and sanitizes a domain name for env file storage.
// Returns a safe domain string suitable for filenames.
// This function is idempotent: calling it on already-sanitized input returns the same value.
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
	// Allow large env values (certs/keys) without hitting the default ~64KB limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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
		rawValue := parts[1]

		trimmedValue := strings.TrimSpace(rawValue)
		value := trimmedValue

		if len(trimmedValue) >= 2 {
			if (strings.HasPrefix(trimmedValue, "\"") && strings.HasSuffix(trimmedValue, "\"")) ||
				(strings.HasPrefix(trimmedValue, "'") && strings.HasSuffix(trimmedValue, "'")) {
				value = trimmedValue[1 : len(trimmedValue)-1]
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
		return ErrInvalidEnvKey
	}
	if strings.Contains(key, "..") || strings.ContainsAny(key, "/\\") {
		return ErrPathTraversal
	}
	if !envKeyRegex.MatchString(key) {
		return ErrInvalidEnvKey
	}
	return nil
}

// ValidateContainerName validates a container name for attachment storage.
// Container names can contain alphanumeric characters, hyphens, and underscores,
// but must start with a letter.
func ValidateContainerName(name string) error {
	if name == "" {
		return ErrInvalidContainerName
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return ErrPathTraversal
	}
	if !containerNameRegex.MatchString(name) {
		return ErrInvalidContainerName
	}
	return nil
}
