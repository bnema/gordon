package publictls

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Config holds the resolved configuration for public ACME TLS.
type Config struct {
	Enabled   bool
	Email     string
	Challenge string
	HTTPPort  int
	TLSPort   int
	DataDir   string
}

// EffectiveChallenge represents the resolved ACME challenge configuration.
type EffectiveChallenge struct {
	ConfiguredMode domain.ACMEChallengeMode
	Mode           domain.ACMEChallengeMode
	Token          string
	TokenSource    domain.ACMETokenSource
	Reason         string
}

// ResolveEffectiveChallenge resolves the effective ACME challenge configuration
// from the given Config and optional token resolver.
func ResolveEffectiveChallenge(ctx context.Context, cfg Config, resolver out.SecretResolver) (EffectiveChallenge, error) {
	if !cfg.Enabled {
		return EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeAuto,
			Mode:           domain.ACMEChallengeAuto,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "ACME disabled",
		}, nil
	}

	if cfg.Email == "" {
		return EffectiveChallenge{}, domain.ErrACMEEmailRequired
	}

	if cfg.TLSPort == 0 {
		return EffectiveChallenge{}, fmt.Errorf("%w: tls_port must be set", domain.ErrACMEChallengeInvalid)
	}

	parsedMode, err := domain.ParseACMEChallengeMode(cfg.Challenge)
	if err != nil {
		return EffectiveChallenge{}, err
	}

	switch parsedMode {
	case domain.ACMEChallengeHTTP01:
		if cfg.HTTPPort == 0 {
			return EffectiveChallenge{}, fmt.Errorf("%w: http_port must be set for http-01 challenge", domain.ErrACMEChallengeInvalid)
		}
		return EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeHTTP01,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "http-01 challenge selected",
		}, nil

	case domain.ACMEChallengeCloudflareDNS01:
		token, source, err := resolveToken(ctx, resolver)
		if err != nil {
			return EffectiveChallenge{}, fmt.Errorf("resolve Cloudflare token: %w", err)
		}
		if token == "" {
			return EffectiveChallenge{}, domain.ErrCloudflareTokenMissing
		}
		return EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeCloudflareDNS01,
			Mode:           domain.ACMEChallengeCloudflareDNS01,
			Token:          token,
			TokenSource:    source,
			Reason:         "cloudflare dns-01 challenge selected",
		}, nil

	case domain.ACMEChallengeAuto:
		token, source := resolveOptionalToken(ctx, resolver)
		if token != "" {
			return EffectiveChallenge{
				ConfiguredMode: domain.ACMEChallengeAuto,
				Mode:           domain.ACMEChallengeCloudflareDNS01,
				Token:          token,
				TokenSource:    source,
				Reason:         "auto selected cloudflare dns-01 (token available)",
			}, nil
		}
		if cfg.HTTPPort == 0 {
			return EffectiveChallenge{}, fmt.Errorf("%w: http_port must be set for http-01 fallback in auto mode", domain.ErrACMEChallengeInvalid)
		}
		return EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeAuto,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "auto selected http-01 (no cloudflare token)",
		}, nil

	default:
		return EffectiveChallenge{}, fmt.Errorf("%w: %q", domain.ErrACMEChallengeInvalid, parsedMode)
	}
}

// resolveOptionalToken resolves the Cloudflare token, ignoring resolver errors.
// If the resolver is nil or returns an empty token, the result is empty with source none.
func resolveOptionalToken(ctx context.Context, resolver out.SecretResolver) (string, domain.ACMETokenSource) {
	if resolver == nil {
		return "", domain.ACMETokenSourceNone
	}
	sv, err := resolver.ResolveCloudflareToken(ctx)
	if err != nil {
		return "", domain.ACMETokenSourceNone
	}
	if sv.Value == "" {
		return "", domain.ACMETokenSourceNone
	}
	return sv.Value, sv.Source
}

// resolveToken resolves the Cloudflare token via the resolver, propagating errors.
// Returns empty token and none source if resolver is nil.
func resolveToken(ctx context.Context, resolver out.SecretResolver) (string, domain.ACMETokenSource, error) {
	if resolver == nil {
		return "", domain.ACMETokenSourceNone, nil
	}
	sv, err := resolver.ResolveCloudflareToken(ctx)
	if err != nil {
		return "", domain.ACMETokenSourceNone, err
	}
	if sv.Value == "" {
		return "", domain.ACMETokenSourceNone, nil
	}
	return sv.Value, sv.Source, nil
}
