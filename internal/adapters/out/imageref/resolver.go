// Package imageref resolves image references to pinned digests without
// pulling or running anything. Installation-registry refs resolve locally
// through manifest storage; external refs resolve through the registry
// transfer path (allowlist + require-digest policy enforced by callers).
package imageref

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// ManifestReader is the manifest-storage subset the resolver needs.
type ManifestReader interface {
	GetManifest(name, reference string) ([]byte, string, error)
}

// RemoteResolver resolves external refs (allowlisted registries).
type RemoteResolver interface {
	ResolveDigest(ctx context.Context, ref string) (string, error)
}

// Resolver implements out.ImageResolver.
type Resolver struct {
	registryDomain string
	manifests      ManifestReader
	remote         RemoteResolver
	policy         domain.ImageSourcePolicy
}

// NewResolver wires local manifest resolution with an optional remote.
func NewResolver(registryDomain string, manifests ManifestReader, remote RemoteResolver) *Resolver {
	return &Resolver{
		registryDomain: registryDomain,
		manifests:      manifests,
		remote:         remote,
		policy:         domain.ImageSourcePolicy{InstallationRegistry: registryDomain},
	}
}

// WithPolicy installs the installation image-source policy. Every
// reference, including an already-pinned digest, is validated against it
// before resolution.
func (r *Resolver) WithPolicy(policy domain.ImageSourcePolicy) *Resolver {
	r.policy = policy
	return r
}

// ResolveDigest implements out.ImageResolver.
func (r *Resolver) ResolveDigest(ctx context.Context, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", fmt.Errorf("imageref: empty reference: %w", domain.ErrAppImageUnresolvable)
	}
	// Already pinned: digest refs pass through after validation.
	if _, digest, ok := strings.Cut(trimmed, "@"); ok {
		if !strings.HasPrefix(digest, "sha256:") || len(digest) != 7+64 {
			return "", fmt.Errorf("imageref: reference %q has invalid digest: %w", ref, domain.ErrAppImageUnresolvable)
		}
	}
	// Every reference, pinned or not, is validated against installation
	// policy before any resolution or pull.
	if err := r.policy.ValidateImageSource(trimmed); err != nil {
		return "", fmt.Errorf("imageref: %w", err)
	}
	if _, digest, ok := strings.Cut(trimmed, "@"); ok {
		return digest, nil
	}
	// Installation-registry refs resolve locally.
	if name, tag, ok := splitLocalRef(r.registryDomain, trimmed); ok {
		raw, _, err := r.manifests.GetManifest(name, tag)
		if err != nil {
			return "", fmt.Errorf("imageref: local manifest %s:%s: %w", name, tag, domain.ErrAppImageUnresolvable)
		}
		sum := sha256.Sum256(raw)
		return "sha256:" + hex.EncodeToString(sum[:]), nil
	}
	// External refs need the remote resolver.
	if r.remote == nil {
		return "", fmt.Errorf("imageref: external reference %q needs remote resolution: %w", ref, domain.ErrAppImageUnresolvable)
	}
	digest, err := r.remote.ResolveDigest(ctx, trimmed)
	if err != nil {
		return "", fmt.Errorf("imageref: remote %q: %w", ref, domain.ErrAppImageUnresolvable)
	}
	return digest, nil
}

// splitLocalRef splits registry-domain refs into name + tag.
func splitLocalRef(registryDomain, ref string) (string, string, bool) {
	host, rest, ok := strings.Cut(ref, "/")
	if !ok || host != registryDomain {
		return "", "", false
	}
	name, tag, _ := strings.Cut(rest, ":")
	if name == "" {
		return "", "", false
	}
	if tag == "" {
		tag = "latest"
	}
	return name, tag, true
}
