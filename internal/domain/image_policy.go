package domain

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/bnema/gordon/pkg/validation"
)

var defaultImageRegistries = []string{"docker.io", "ghcr.io", "quay.io"}

type ImagePullTransport string

const (
	ImagePullTransportVerifiedTLS ImagePullTransport = "verified-tls"
	ImagePullTransportHTTP        ImagePullTransport = "http"
)

type ImagePullRequest struct {
	Reference string
	Username  string
	Password  string
	Transport ImagePullTransport
}

// IsInstallationImage reports whether ref belongs to the configured
// installation registry, under its own host or one of its aliases.
func (p ImageSourcePolicy) IsInstallationImage(ref string) bool {
	host, _, err := parseImageRegistry(strings.TrimSpace(ref))
	if err != nil || p.InstallationRegistry == "" {
		return false
	}
	for _, entry := range p.installationHosts() {
		installation, err := canonicalRegistryHost(entry)
		if err == nil && dockerRegistryAlias(host) == dockerRegistryAlias(installation) {
			return true
		}
	}
	return false
}

// installationHosts lists the installation registry and its non-empty aliases.
func (p ImageSourcePolicy) installationHosts() []string {
	if p.InstallationRegistry == "" {
		return nil
	}
	hosts := []string{p.InstallationRegistry}
	for _, alias := range p.InstallationAliases {
		if strings.TrimSpace(alias) != "" {
			hosts = append(hosts, alias)
		}
	}
	return hosts
}

// ImageSourcePolicy is the installation policy for every image reference
// the daemon validates, resolves, pulls, or runs.
type ImageSourcePolicy struct {
	// AllowedRegistries adds explicit hostname+port entries to the defaults.
	AllowedRegistries []string
	// RequireDigest rejects mutable tag references.
	RequireDigest bool
	// InstallationRegistry is Gordon's own registry host, always allowed.
	InstallationRegistry string
	// InstallationAliases are other hosts that serve the same registry
	// (the CI host). They are allowed and treated as the installation.
	InstallationAliases []string
}

// Clone returns a deep copy, so a published policy snapshot never shares
// slices with the config it was built from.
func (p ImageSourcePolicy) Clone() ImageSourcePolicy {
	clone := p
	clone.AllowedRegistries = cloneStrings(p.AllowedRegistries)
	clone.InstallationAliases = cloneStrings(p.InstallationAliases)
	return clone
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

// ImageReference is a parsed image reference: the canonical registry host,
// the registry-relative repository, and the tag or digest (either may be
// empty).
type ImageReference struct {
	Host       string
	Repository string
	Tag        string
	Digest     string
}

// ParseImageReference splits ref into host, repository, tag, and digest
// following Docker's host rule, so callers that already classified the host
// (for example as an installation host) can address the repository
// independently of which host name the reference used.
func ParseImageReference(ref string) (ImageReference, error) {
	host, rest, err := parseImageRegistry(strings.TrimSpace(ref))
	if err != nil {
		return ImageReference{}, err
	}
	parsed := ImageReference{Host: host}
	if base, digest, ok := strings.Cut(rest, "@"); ok {
		if err := validation.ValidateImageDigest(digest); err != nil {
			return ImageReference{}, err
		}
		parsed.Digest = digest
		rest = base
	}
	parsed.Repository, parsed.Tag = splitRepositoryReference(rest)
	if err := validation.ValidateRepositoryName(parsed.Repository); err != nil {
		return ImageReference{}, err
	}
	// A tag separator must be followed by a valid tag ("app:" is malformed).
	if strings.Contains(rest, ":") {
		if err := validation.ValidateReference(parsed.Tag); err != nil {
			return ImageReference{}, err
		}
	}
	return parsed, nil
}

// Validate checks every configured registry authority.
func (p ImageSourcePolicy) Validate() error {
	entries := append(append([]string{}, p.AllowedRegistries...), p.installationHosts()...)
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
	parsed, err := ParseImageReference(ref)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrAppImageNotAllowed, err)
	}
	if parsed.Digest == "" && p.RequireDigest {
		return fmt.Errorf("%w: registry policy requires an immutable digest", ErrAppImageNotAllowed)
	}
	if p.registryAllowed(parsed.Host) {
		return nil
	}
	return fmt.Errorf("%w: registry %q is not allowlisted", ErrAppImageNotAllowed, parsed.Host)
}

func (p ImageSourcePolicy) registryAllowed(host string) bool {
	entries := append(append([]string{}, defaultImageRegistries...), p.AllowedRegistries...)
	entries = append(entries, p.installationHosts()...)
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
