package app

import (
	"fmt"
	"time"

	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/domain"
)

// TrustedIssuerConfig preserves duration presence: omitted values get defaults,
// whereas an explicitly configured zero clock skew remains zero.
type TrustedIssuerConfig struct {
	Issuer              string `mapstructure:"issuer"`
	Audience            string `mapstructure:"audience"`
	JWKSURI             string `mapstructure:"jwks_uri"`
	MaxTokenLifetime    string `mapstructure:"max_token_lifetime"`
	ClockSkew           string `mapstructure:"clock_skew"`
	AllowPrivateNetwork bool   `mapstructure:"allow_private_network"`
}

// PromotionSnapshot is one immutable, atomically published config generation.
type PromotionSnapshot struct {
	issuers  []domain.TrustedIssuer
	policies domain.PromotionPolicySet
}

func (s PromotionSnapshot) Issuers() []domain.TrustedIssuer {
	return append([]domain.TrustedIssuer(nil), s.issuers...)
}
func (s PromotionSnapshot) Policies() domain.PromotionPolicySet { return s.policies }

// PromotionSnapshot returns a consistent generation; before initialization it denies all grants.
func (s *services) PromotionSnapshot() PromotionSnapshot {
	if current := s.promotionSnapshot.Load(); current != nil {
		return *current
	}
	return PromotionSnapshot{}
}

func buildPromotionSnapshot(cfg Config) (PromotionSnapshot, error) {
	var snapshot PromotionSnapshot
	if err := validateCIDomain(cfg); err != nil {
		return snapshot, err
	}
	declared := make(map[string]struct{})
	for index, raw := range cfg.TrustedIssuers {
		issuer, err := buildTrustedIssuer(raw)
		if err != nil {
			return snapshot, fmt.Errorf("trusted_issuers[%d]: %w", index, err)
		}
		if _, ok := declared[issuer.Issuer]; ok {
			return snapshot, fmt.Errorf("duplicate trusted issuer")
		}
		declared[issuer.Issuer] = struct{}{}
		snapshot.issuers = append(snapshot.issuers, issuer)
	}
	for _, policy := range cfg.PromotionPolicies {
		if _, ok := declared[policy.Issuer]; policy.Issuer != domain.GordonIssuer && !ok {
			return snapshot, fmt.Errorf("promotion policy issuer is not declared")
		}
	}
	policies, err := domain.NewPromotionPolicySet(cfg.PromotionPolicies)
	if err != nil {
		return snapshot, fmt.Errorf("promotion_policies: %w", redactedPromotionPolicyError{err})
	}
	snapshot.policies = policies
	return snapshot, nil
}

func buildTrustedIssuer(raw TrustedIssuerConfig) (domain.TrustedIssuer, error) {
	issuer := domain.TrustedIssuer{Issuer: raw.Issuer, Audience: raw.Audience, JWKSURI: raw.JWKSURI, AllowPrivateNetwork: raw.AllowPrivateNetwork, MaxTokenLifetime: domain.DefaultMaxTokenLifetime, ClockSkew: domain.DefaultClockSkew}
	var err error
	if raw.MaxTokenLifetime != "" {
		issuer.MaxTokenLifetime, err = time.ParseDuration(raw.MaxTokenLifetime)
		if err != nil || issuer.MaxTokenLifetime <= 0 {
			return issuer, fmt.Errorf("max_token_lifetime must be a positive duration")
		}
	}
	if raw.ClockSkew != "" {
		issuer.ClockSkew, err = time.ParseDuration(raw.ClockSkew)
		if err != nil {
			return issuer, fmt.Errorf("clock_skew must be a non-negative duration")
		}
	}
	return issuer, issuer.Validate()
}

// redactedPromotionPolicyError preserves errors.Is/As without echoing identifiers.
type redactedPromotionPolicyError struct{ cause error }

func (e redactedPromotionPolicyError) Error() string { return redactedDevicePolicyReason(e.cause) }
func (e redactedPromotionPolicyError) Unwrap() error { return e.cause }

func validateCIDomain(cfg Config) error {
	if cfg.Server.CIDomain == "" {
		return nil
	}
	if _, ok := domain.CanonicalRouteDomain(cfg.Server.CIDomain); !ok {
		return fmt.Errorf("server.ci_domain must be a public hostname without a port or trailing dot")
	}
	if !cfg.Auth.Enabled {
		return fmt.Errorf("server.ci_domain requires auth.enabled")
	}
	reserved := append([]string{cfg.Server.GordonDomain}, cfg.Server.LegacyRegistryDomains...)
	for _, other := range reserved {
		if canonicalHostsEqual(cfg.Server.CIDomain, other) {
			return fmt.Errorf("server.ci_domain collides with a management domain")
		}
	}
	return nil
}

func validatePromotionConfig(v *viper.Viper, cfg Config) error {
	for name, allowed := range knownPromotionConfigKeys {
		var entries []map[string]any
		if err := v.UnmarshalKey(name, &entries); err != nil {
			return fmt.Errorf("invalid %s: %w", name, err)
		}
		for _, entry := range entries {
			for key := range entry {
				if _, ok := allowed[key]; !ok {
					return fmt.Errorf("unknown %s key %q", name, key)
				}
			}
		}
	}
	_, err := buildPromotionSnapshot(cfg)
	return err
}

// validateCIAppHosts runs against the detached ACTIVE-derived view before publication.
func validateCIAppHosts(cfg Config, hosts appHostRoutes) error {
	if cfg.Server.CIDomain == "" || hosts == nil {
		return nil
	}
	for _, host := range hosts.AppHosts() {
		if canonicalHostsEqual(host.Host, cfg.Server.CIDomain) {
			return fmt.Errorf("server.ci_domain collides with an app host")
		}
	}
	return nil
}

// initPromotionSnapshot validates persisted served hosts before startup can continue.
func (si *serviceInit) initPromotionSnapshot() error {
	snapshot, err := buildPromotionSnapshot(si.cfg)
	if err != nil {
		return err
	}
	imagePolicy, err := buildImagePolicy(si.cfg)
	if err != nil {
		return err
	}
	if si.cfg.Server.CIDomain != "" && si.svc.appTrafficPublisher != nil {
		if _, err := si.svc.appTrafficPublisher.projectCandidate(si.ctx, si.cfg); err != nil {
			return err
		}
	}
	si.svc.publishPromotion(snapshot, imagePolicy, si.cfg)
	return nil
}

// Exact subtree key sets: quoted keys containing spaces are not individual keys.
var knownPromotionConfigKeys = map[string]map[string]struct{}{
	"trusted_issuers":    {"issuer": {}, "audience": {}, "jwks_uri": {}, "max_token_lifetime": {}, "clock_skew": {}, "allow_private_network": {}},
	"promotion_policies": {"issuer": {}, "subjects": {}, "app": {}, "service": {}, "repository": {}},
}
