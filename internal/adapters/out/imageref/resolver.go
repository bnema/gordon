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
	"github.com/bnema/gordon/pkg/validation"
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
	manifests ManifestReader
	remote    RemoteResolver
	policy    func() domain.ImageSourcePolicy
}

// NewResolver wires local manifest resolution with an optional remote. The
// default policy treats registryDomain as the only installation registry.
func NewResolver(registryDomain string, manifests ManifestReader, remote RemoteResolver) *Resolver {
	fixed := domain.ImageSourcePolicy{InstallationRegistry: registryDomain}
	return &Resolver{
		manifests: manifests,
		remote:    remote,
		policy:    func() domain.ImageSourcePolicy { return fixed },
	}
}

// WithPolicy installs the provider of the current installation image-source
// policy, read once per resolution. Every reference, including an
// already-pinned digest, is validated against it before resolution, and
// references on any installation host (the registry or an alias such as the
// CI host) resolve through local manifest storage.
func (r *Resolver) WithPolicy(policy func() domain.ImageSourcePolicy) *Resolver {
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
		if err := validation.ValidateImageDigest(digest); err != nil {
			return "", fmt.Errorf("imageref: reference %q has invalid digest: %w", ref, domain.ErrAppImageUnresolvable)
		}
	}
	// Every reference, pinned or not, is validated against installation
	// policy before any resolution or pull. One policy value serves the
	// whole resolution.
	policy := r.policy()
	if err := policy.ValidateImageSource(trimmed); err != nil {
		return "", fmt.Errorf("imageref: %w", err)
	}
	if _, digest, ok := strings.Cut(trimmed, "@"); ok {
		return digest, nil
	}
	// Installation-registry refs resolve locally.
	if name, tag, ok := localRef(policy, trimmed); ok {
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
	if err := validation.ValidateImageDigest(digest); err != nil {
		return "", fmt.Errorf("imageref: remote %q returned invalid digest: %w", ref, domain.ErrAppImageUnresolvable)
	}
	return digest, nil
}

// localRef splits a reference on any installation host into repository and
// tag (default latest), independent of which installation host it names.
func localRef(policy domain.ImageSourcePolicy, ref string) (string, string, bool) {
	if !policy.IsInstallationImage(ref) {
		return "", "", false
	}
	parsed, err := domain.ParseImageReference(ref)
	if err != nil || parsed.Digest != "" {
		return "", "", false
	}
	tag := parsed.Tag
	if tag == "" {
		tag = "latest"
	}
	return parsed.Repository, tag, true
}
