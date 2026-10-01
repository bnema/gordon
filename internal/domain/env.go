package domain

import (
	"regexp"
	"strings"
)

var (
	envKeyRegex    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretRefRegex = regexp.MustCompile(`\$\{(pass|sops):[^}]+\}`)
)

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
