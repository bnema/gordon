package domain

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/pkg/validation"
)

// AppOperationKindDeploy is the operation kind of a revision deploy.
const AppOperationKindDeploy = "deploy"

// PromotionImage is a parsed promotion reference: an installation registry
// repository pinned to one exact sha256 digest.
type PromotionImage struct {
	Repository string
	Digest     string
}

// Canonical renders the reference on the given installation registry host.
func (i PromotionImage) Canonical(host string) string {
	return host + "/" + i.Repository + "@" + i.Digest
}

// ParsePromotionImage accepts only `[<host>/]<repo>@sha256:<64 lowercase hex>`
// where host, when present, is one of the accepted installation registry
// hosts. Tags, other digest algorithms, and foreign hosts are refused with
// ErrInvalidPromotionImage.
func ParsePromotionImage(ref string, acceptedHosts []string) (PromotionImage, error) {
	name, digest, ok := strings.Cut(ref, "@")
	if !ok || strings.ContainsAny(ref, " \t\r\n") || strings.Contains(digest, "@") {
		return PromotionImage{}, fmt.Errorf("%w: expected REPOSITORY@sha256:DIGEST", ErrInvalidPromotionImage)
	}
	if err := validation.ValidateImageDigest(digest); err != nil {
		return PromotionImage{}, fmt.Errorf("%w: digest must be sha256:<64 lowercase hex>", ErrInvalidPromotionImage)
	}
	if first, rest, explicit := strings.Cut(name, "/"); explicit && (strings.ContainsAny(first, ".:") || strings.EqualFold(first, "localhost")) {
		if !promotionHostAccepted(first, acceptedHosts) {
			return PromotionImage{}, fmt.Errorf("%w: registry host is not this installation", ErrInvalidPromotionImage)
		}
		name = rest
	}
	if err := validation.ValidateRepositoryName(name); err != nil {
		return PromotionImage{}, fmt.Errorf("%w: invalid repository", ErrInvalidPromotionImage)
	}
	return PromotionImage{Repository: name, Digest: digest}, nil
}

func promotionHostAccepted(host string, accepted []string) bool {
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
