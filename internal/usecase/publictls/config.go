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

// validPort checks that port is in the valid TCP port range 1..65535.
func validPort(p int) bool {
	return p >= 1 && p <= 65535
}

// ResolveEffectiveChallenge resolves the effective ACME challenge configuration
// from the given Config and optional token resolver.
func ResolveEffectiveChallenge(ctx context.Context, cfg Config, resolver out.SecretResolver) (EffectiveChallenge, error) {
	if !cfg.Enabled {
		configuredMode := domain.ACMEChallengeAuto
		if parsed, err := domain.ParseACMEChallengeMode(cfg.Challenge); err == nil {
			configuredMode = parsed
		}
		return EffectiveChallenge{
			ConfiguredMode: configuredMode,
			Mode:           domain.ACMEChallengeAuto,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "ACME disabled",
		}, nil
	}

	if cfg.Email == "" {
		return EffectiveChallenge{}, domain.ErrACMEEmailRequired
	}

	if !validPort(cfg.TLSPort) {
		return EffectiveChallenge{}, fmt.Errorf("%w: tls_port must be in range 1..65535", domain.ErrACMEChallengeInvalid)
	}

	parsedMode, err := domain.ParseACMEChallengeMode(cfg.Challenge)
	if err != nil {
		return EffectiveChallenge{}, err
	}

	switch parsedMode {
	case domain.ACMEChallengeHTTP01:
		if !validPort(cfg.HTTPPort) {
			return EffectiveChallenge{}, fmt.Errorf("%w: http_port must be in range 1..65535 for http-01 challenge", domain.ErrACMEChallengeInvalid)
		}
		return EffectiveChallenge{
			ConfiguredMode: domain.ACMEChallengeHTTP01,
			Mode:           domain.ACMEChallengeHTTP01,
			TokenSource:    domain.ACMETokenSourceNone,
			Reason:         "http-01 challenge selected",
		}, nil

	case domain.ACMEChallengeCloudflareDNS01:
		token, source, err := resolveToken(ctx, resolver, true)
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
		token, source, _ := resolveToken(ctx, resolver, false)
		if token != "" {
			return EffectiveChallenge{
				ConfiguredMode: domain.ACMEChallengeAuto,
				Mode:           domain.ACMEChallengeCloudflareDNS01,
				Token:          token,
				TokenSource:    source,
				Reason:         "auto selected cloudflare dns-01 (token available)",
			}, nil
		}
		if !validPort(cfg.HTTPPort) {
			return EffectiveChallenge{}, fmt.Errorf("%w: http_port must be in range 1..65535 for http-01 fallback in auto mode", domain.ErrACMEChallengeInvalid)
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

// resolveToken resolves the Cloudflare token via the resolver. When strict is
// false, resolver errors are ignored so auto mode can fall back to HTTP-01.
func resolveToken(ctx context.Context, resolver out.SecretResolver, strict bool) (string, domain.ACMETokenSource, error) {
	if resolver == nil {
		return "", domain.ACMETokenSourceNone, nil
	}
	sv, err := resolver.ResolveCloudflareToken(ctx)
	if err != nil {
		if strict {
			return "", domain.ACMETokenSourceNone, err
		}
		return "", domain.ACMETokenSourceNone, nil
	}
	if sv.Value == "" {
		return "", domain.ACMETokenSourceNone, nil
	}
	return sv.Value, sv.Source, nil
}
