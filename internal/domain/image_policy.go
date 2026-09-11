package domain

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/bnema/gordon/pkg/validation"
)

var defaultImageRegistries = []string{"docker.io", "ghcr.io", "quay.io"}

// ImageSourcePolicy is the installation policy for every image reference
// the daemon validates, resolves, pulls, or runs.
type ImageSourcePolicy struct {
	// AllowedRegistries adds explicit hostname+port entries to the defaults.
	AllowedRegistries []string
	// RequireDigest rejects mutable tag references.
	RequireDigest bool
	// InstallationRegistry is Gordon's own registry host, always allowed.
	InstallationRegistry string
}

// Validate checks every configured registry authority.
func (p ImageSourcePolicy) Validate() error {
	entries := append([]string{}, p.AllowedRegistries...)
	if p.InstallationRegistry != "" {
		entries = append(entries, p.InstallationRegistry)
	}
	for _, entry := range entries {
		if _, err := canonicalRegistryHost(entry); err != nil {
			return fmt.Errorf("%w: %s", ErrAppImageNotAllowed, err)
		}
	}
	return nil
}

// ValidateImageSource checks one reference against the installation registry
// allowlist. The allowlist controls names the runtime may contact; it does not
// constrain where DNS resolves or provide runtime network egress enforcement.
func (p ImageSourcePolicy) ValidateImageSource(ref string) error {
	host, rest, err := parseImageRegistry(strings.TrimSpace(ref))
	if err != nil {
		return fmt.Errorf("%w: %s", ErrAppImageNotAllowed, err)
	}
	repository, _ := splitRepositoryReference(rest)
	if err := validation.ValidateRepositoryName(repository); err != nil {
		return fmt.Errorf("%w: %s", ErrAppImageNotAllowed, err)
	}
	if p.RequireDigest && !strings.Contains(rest, "@") {
		return fmt.Errorf("%w: registry policy requires an immutable digest", ErrAppImageNotAllowed)
	}
	if p.registryAllowed(host) {
		return nil
	}
	return fmt.Errorf("%w: registry %q is not allowlisted", ErrAppImageNotAllowed, host)
}

func (p ImageSourcePolicy) registryAllowed(host string) bool {
	entries := append(append([]string{}, defaultImageRegistries...), p.AllowedRegistries...)
	if p.InstallationRegistry != "" {
		entries = append(entries, p.InstallationRegistry)
	}
	for _, entry := range entries {
		allowed, err := canonicalRegistryHost(entry)
		if err == nil && dockerRegistryAlias(allowed) == dockerRegistryAlias(host) {
			return true
		}
	}
	return false
}

func parseImageRegistry(ref string) (string, string, error) {
	if ref == "" || strings.Contains(ref, "://") || strings.Contains(ref, "\\") {
		return "", "", fmt.Errorf("malformed image reference")
	}
	first, rest, explicit := strings.Cut(ref, "/")
	if !explicit || (!strings.ContainsAny(first, ".:") && !strings.EqualFold(first, "localhost")) {
		first, rest = "docker.io", ref
	}
	if strings.Contains(first, "@") || rest == "" {
		return "", "", fmt.Errorf("malformed registry host")
	}
	host, err := canonicalRegistryHost(first)
	if err != nil {
		return "", "", err
	}
	return host, rest, nil
}

// canonicalRegistryHost safely normalizes a hostname or IP plus optional port.
// HTTPS's default port is omitted so host and host:443 are equivalent.
func canonicalRegistryHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "/@?#") {
		return "", fmt.Errorf("malformed registry host %q", value)
	}
	host, port, err := splitRegistryAuthority(value)
	if err != nil {
		return "", err
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || strings.Contains(host, "..") || (net.ParseIP(host) == nil && !validRegistryHostname(host)) {
		return "", fmt.Errorf("malformed registry host %q", value)
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", fmt.Errorf("malformed registry port %q", port)
		}
		if n != 443 {
			return net.JoinHostPort(host, port), nil
		}
	}
	return host, nil
}

func splitRegistryAuthority(value string) (string, string, error) {
	if strings.HasPrefix(value, "[") {
		if strings.HasSuffix(value, "]") {
			return strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"), "", nil
		}
		host, port, err := net.SplitHostPort(value)
		if err != nil {
			return "", "", fmt.Errorf("malformed registry host %q", value)
		}
		return host, port, nil
	}
	if strings.Count(value, ":") == 1 {
		host, port, _ := strings.Cut(value, ":")
		if host == "" || port == "" {
			return "", "", fmt.Errorf("malformed registry host %q", value)
		}
		return host, port, nil
	}
	if strings.Contains(value, ":") && net.ParseIP(value) == nil {
		return "", "", fmt.Errorf("ambiguous registry host %q", value)
	}
	return value, "", nil
}

func validRegistryHostname(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return len(host) <= 253
}

func dockerRegistryAlias(host string) string {
	if host == "registry-1.docker.io" {
		return "docker.io"
	}
	return host
}

func splitRepositoryReference(rest string) (string, string) {
	if base, digest, ok := strings.Cut(rest, "@"); ok {
		return base, digest
	}
	if idx := strings.LastIndex(rest, ":"); idx >= 0 {
		return rest[:idx], rest[idx+1:]
	}
	return rest, ""
}

// IsLocalOrPrivateHost reports whether a literal registry host is local or
// non-public. It does not resolve DNS names and must not be treated as an
// egress guarantee.
func IsLocalOrPrivateHost(host string) bool {
	hostname, err := canonicalRegistryHost(host)
	if err != nil {
		return true
	}
	if h, _, splitErr := net.SplitHostPort(hostname); splitErr == nil {
		hostname = h
	}
	hostname = strings.Trim(hostname, "[]")
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast())
}
