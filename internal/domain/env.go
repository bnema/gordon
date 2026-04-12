package domain

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

var (
	envKeyRegex        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	containerNameRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
	secretRefRegex     = regexp.MustCompile(`\$\{(pass|sops):[^}]+\}`)
)

// SanitizeDomainForEnvFile validates and sanitizes a domain name for env file storage.
// Returns the collision-resistant storage key used for filenames.
func SanitizeDomainForEnvFile(domainName string) (string, error) {
	safeDomain, err := NewEnvStorageKey(domainName)
	if err != nil {
		return "", err
	}

	return string(safeDomain), nil
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

// ContainsSecretReference detects if a string contains a secret provider reference
// like ${pass:path} or ${sops:path}. This is used to prevent attacker-controlled
// env files from persisting secret references that would later resolve against
// host secret providers.
func ContainsSecretReference(value string) bool {
	return secretRefRegex.MatchString(value)
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
