package domain

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/pkg/validation"
)

// DigestImage is a parsed set-image reference: an installation registry
// repository pinned to one exact sha256 digest.
type DigestImage struct {
	Repository string
	Digest     string
}

// Canonical renders the reference on the given installation registry host.
func (i DigestImage) Canonical(host string) string {
	return host + "/" + i.Repository + "@" + i.Digest
}

// ParseDigestImage accepts only `[<host>/]<repo>@sha256:<64 lowercase hex>`
// where host, when present, is one of the accepted installation registry
// hosts. Tags, other digest algorithms, and foreign hosts are refused with
// ErrInvalidImageReference.
func ParseDigestImage(ref string, acceptedHosts []string) (DigestImage, error) {
	name, digest, ok := strings.Cut(ref, "@")
	if !ok || strings.ContainsAny(ref, " \t\r\n") || strings.Contains(digest, "@") {
		return DigestImage{}, fmt.Errorf("%w: expected REPOSITORY@sha256:DIGEST", ErrInvalidImageReference)
	}
	if err := validation.ValidateImageDigest(digest); err != nil {
		return DigestImage{}, fmt.Errorf("%w: digest must be sha256:<64 lowercase hex>", ErrInvalidImageReference)
	}
	if first, rest, explicit := strings.Cut(name, "/"); explicit && (strings.ContainsAny(first, ".:") || strings.EqualFold(first, "localhost")) {
		if !setImageHostAccepted(first, acceptedHosts) {
			return DigestImage{}, fmt.Errorf("%w: registry host is not this installation", ErrInvalidImageReference)
		}
		name = rest
	}
	if err := validation.ValidateRepositoryName(name); err != nil {
		return DigestImage{}, fmt.Errorf("%w: invalid repository", ErrInvalidImageReference)
	}
	return DigestImage{Repository: name, Digest: digest}, nil
}

func setImageHostAccepted(host string, accepted []string) bool {
	canonical, err := canonicalRegistryHost(host)
	if err != nil {
		return false
	}
	for _, candidate := range accepted {
		if candidate == "" {
			continue
		}
		if allowed, err := canonicalRegistryHost(candidate); err == nil && allowed == canonical {
			return true
		}
	}
	return false
}
