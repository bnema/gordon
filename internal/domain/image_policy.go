package domain

import (
	"fmt"
	"net"
	"strings"

	"github.com/bnema/gordon/pkg/validation"
)

// ImageSourcePolicy is the installation policy for every image reference
// the daemon resolves, pulls, or runs. It is validated before resolution
// and again before any pull, including boot, restart, and recovery, so a
// digest-pinned reference can never bypass the registry allowlist or the
// local/private address restriction.
type ImageSourcePolicy struct {
	// AllowedRegistries restricts which registry hosts may be contacted.
	// An empty list allows any public registry; the entry "*" allows any
	// host. Hosts compare case-insensitively.
	AllowedRegistries []string
	// RequireDigest rejects mutable tag references.
	RequireDigest bool
	// InstallationRegistry is Gordon's own registry host, always allowed.
	InstallationRegistry string
}

// ValidateImageSource checks one reference against the policy. Both the
// repository grammar and the registry host are validated; local and
// private addresses are always rejected, even when allowlisted, because
// the runtime would otherwise be coerced into contacting daemon-local or
// internal services.
func (p ImageSourcePolicy) ValidateImageSource(ref string) error {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return fmt.Errorf("%w: empty image reference", ErrAppImageNotAllowed)
	}

	host, rest := splitRegistryHost(trimmed)
	host = normalizeRegistryHost(host)
	if host == "" {
		host = "docker.io"
	}

	repository, _ := splitRepositoryReference(rest)
	if err := validation.ValidateRepositoryName(repository); err != nil {
		return fmt.Errorf("%w: %s", ErrAppImageNotAllowed, err)
	}
	if p.RequireDigest && !strings.Contains(rest, "@") {
		return fmt.Errorf("%w: registry policy requires an immutable digest", ErrAppImageNotAllowed)
	}

	if installation := normalizeRegistryHost(p.InstallationRegistry); installation != "" && host == installation {
		return nil
	}
	if IsLocalOrPrivateHost(host) {
		return fmt.Errorf("%w: registry %q resolves to a local or private address", ErrAppImageNotAllowed, host)
	}
	if !p.registryAllowed(host) {
		return fmt.Errorf("%w: registry %q is not allowlisted", ErrAppImageNotAllowed, host)
	}
	return nil
}

// registryAllowed reports whether the host is permitted by the allowlist.
func (p ImageSourcePolicy) registryAllowed(host string) bool {
	if len(p.AllowedRegistries) == 0 {
		return true
	}
	for _, allowed := range p.AllowedRegistries {
		allowed = normalizeRegistryHost(allowed)
		if allowed == "*" || allowed == host {
			return true
		}
	}
	return false
}

// splitRegistryHost splits an image reference into its registry host and
// the remainder. Following Docker's rule, the first path component is a
// registry only when it contains a dot or a colon, or is "localhost".
func splitRegistryHost(ref string) (string, string) {
	first, rest, ok := strings.Cut(ref, "/")
	if !ok {
		return "", ref
	}
	if strings.ContainsAny(first, ".:") || strings.EqualFold(first, "localhost") {
		return first, rest
	}
	return "", ref
}

// splitRepositoryReference splits a repository path from its tag or
// digest reference (the reference is empty when the path is untagged).
func splitRepositoryReference(rest string) (string, string) {
	if base, digest, ok := strings.Cut(rest, "@"); ok {
		return base, digest
	}
	if idx := strings.LastIndex(rest, ":"); idx >= 0 {
		return rest[:idx], rest[idx+1:]
	}
	return rest, ""
}

// normalizeRegistryHost lowercases a registry host and strips a trailing
// dot so comparisons are stable.
func normalizeRegistryHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.TrimSuffix(host, ".")
}

// IsLocalOrPrivateHost reports whether a registry host names a loopback,
// private, link-local, unspecified, or multicast address (or localhost).
func IsLocalOrPrivateHost(host string) bool {
	hostname := normalizeRegistryHost(host)
	if h, _, err := net.SplitHostPort(hostname); err == nil {
		hostname = h
	}
	hostname = strings.Trim(hostname, "[]")
	if hostname == "" {
		return true
	}
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}
