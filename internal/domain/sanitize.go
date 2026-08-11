package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// SanitizeDomainForContainer makes a domain safe for container naming.
// It uses distinct replacements to avoid collisions between domains like
// "git.example.com" and "git-example.com" (which would both become "git-example-com"
// if we naively replaced all separators with hyphens).
//
// Replacements:
//
//	. → __ (double underscore, distinct from single _ in some conventions)
//	: → -_ (hyphen-underscore combo, used for ports)
//	/ → -- (double hyphen, distinct from colon to avoid :/ collisions)
func SanitizeDomainForContainer(domain string) string {
	result := strings.ReplaceAll(domain, ".", "__")
	result = strings.ReplaceAll(result, ":", "-_")
	result = strings.ReplaceAll(result, "/", "--")
	return result
}

// StableResourceName returns a Docker-safe, collision-resistant name fragment.
func StableResourceName(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s-%x", SanitizeDomainForContainer(value), digest[:6])
}

// SanitizeDomainForContainerLegacy uses the OLD (buggy) sanitization that replaces
// all separators with hyphens, for backwards compatibility.
//
// IMPORTANT: This is maintained ONLY for finding old containers during upgrades.
// New containers always use the collision-resistant SanitizeDomainForContainer().
//
// Migration strategy:
// - During binary upgrades, legacy containers are found using this function
// - Old containers are stopped and removed, then replaced with new containers
// - New containers use the collision-resistant SanitizeDomainForContainer() naming
// - After all deployments are upgraded, this function can be removed in a major version
func SanitizeDomainForContainerLegacy(domain string) string {
	result := strings.ReplaceAll(domain, ".", "-")
	result = strings.ReplaceAll(result, ":", "-")
	result = strings.ReplaceAll(result, "/", "-")
	return result
}
