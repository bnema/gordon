package domain

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/validation"
)

const (
	GordonIssuer             = "gordon"
	AppOperationKindSetImage = "set-image"
	DefaultMaxTokenLifetime  = time.Hour
	DefaultClockSkew         = time.Minute
)

// WorkloadPrincipal identifies an exact subject in one identity authority.
type WorkloadPrincipal struct{ Issuer, Subject string }

func (p WorkloadPrincipal) String() string {
	if p.Issuer == GordonIssuer {
		return "gordon:" + p.Subject
	}
	return "oidc:" + p.Issuer + "#" + p.Subject
}

// ParseWorkloadPrincipal parses a stored principal without normalizing its issuer or subject.
func ParseWorkloadPrincipal(value string) (WorkloadPrincipal, error) {
	if sub, ok := strings.CutPrefix(value, "gordon:"); ok && sub != "" {
		return WorkloadPrincipal{GordonIssuer, sub}, nil
	}
	if rest, ok := strings.CutPrefix(value, "oidc:"); ok {
		issuer, sub, found := strings.Cut(rest, "#")
		if found && sub != "" && ValidateIssuerURL(issuer) == nil {
			return WorkloadPrincipal{issuer, sub}, nil
		}
	}
	return WorkloadPrincipal{}, fmt.Errorf("invalid workload principal")
}

// TrustedIssuer is a pure workload identity verification policy.
type TrustedIssuer struct {
	Issuer              string
	Audience            string
	JWKSURI             string
	MaxTokenLifetime    time.Duration
	ClockSkew           time.Duration
	AllowPrivateNetwork bool
}

// WithDefaults substitutes defaults for zero durations. Config decoding preserves
// explicitly configured zero skew without calling this helper.
func (i TrustedIssuer) WithDefaults() TrustedIssuer {
	if i.MaxTokenLifetime == 0 {
		i.MaxTokenLifetime = DefaultMaxTokenLifetime
	}
	if i.ClockSkew == 0 {
		i.ClockSkew = DefaultClockSkew
	}
	return i
}

func (i TrustedIssuer) Validate() error {
	if strings.Contains(i.Issuer+i.Audience+i.JWKSURI, "*") {
		return fmt.Errorf("trusted issuer values must contain no wildcards")
	}
	if err := ValidateIssuerURL(i.Issuer); err != nil {
		return fmt.Errorf("issuer: %w", err)
	}
	if i.JWKSURI != "" {
		if err := ValidateIssuerURL(i.JWKSURI); err != nil {
			return fmt.Errorf("jwks_uri: %w", err)
		}
	}
	if strings.TrimSpace(i.Audience) == "" {
		return fmt.Errorf("audience must not be empty")
	}
	if i.MaxTokenLifetime < 0 || i.ClockSkew < 0 {
		return fmt.Errorf("token lifetime and clock skew must not be negative")
	}
	return nil
}

// ValidateIssuerURL checks syntax only; network access policy belongs to the OIDC adapter.
func ValidateIssuerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !u.IsAbs() || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return fmt.Errorf("must be an absolute HTTPS URL without userinfo, query or fragment")
	}
	return nil
}

type SetImagePolicy struct {
	Issuer     string   `mapstructure:"issuer"`
	Subjects   []string `mapstructure:"subjects"`
	App        string   `mapstructure:"app"`
	Service    string   `mapstructure:"service"`
	Repository string   `mapstructure:"repository"`
}

func (p SetImagePolicy) Validate() error {
	for _, s := range append([]string{p.Issuer, p.App, p.Service, p.Repository}, p.Subjects...) {
		if s == "" || strings.Contains(s, "*") {
			return fmt.Errorf("set-image policy values must be non-empty and contain no wildcards")
		}
	}
	if len(p.Subjects) == 0 {
		return fmt.Errorf("subjects must not be empty")
	}
	if p.Issuer != GordonIssuer {
		if err := ValidateIssuerURL(p.Issuer); err != nil {
			return err
		}
	}
	if err := ValidateAppName(p.App); err != nil {
		return err
	}
	if err := ValidateServiceName(p.Service); err != nil {
		return err
	}
	return validation.ValidateRepositoryName(p.Repository)
}

type setImageGrant struct {
	principal                WorkloadPrincipal
	app, service, repository string
}
type setImageIdentity struct {
	principal    WorkloadPrincipal
	app, service string
}

// SetImagePolicySet owns its grants; callers cannot mutate a constructed set.
// Its zero value denies everything and concurrent readers need no locks.
type SetImagePolicySet struct{ grants map[setImageGrant]struct{} }

func NewSetImagePolicySet(policies []SetImagePolicy) (SetImagePolicySet, error) {
	set := SetImagePolicySet{grants: make(map[setImageGrant]struct{})}
	seen := make(map[setImageIdentity]struct{})
	for _, p := range policies {
		if err := p.Validate(); err != nil {
			return SetImagePolicySet{}, err
		}
		for _, sub := range p.Subjects {
			principal := WorkloadPrincipal{p.Issuer, sub}
			key := setImageIdentity{principal, p.App, p.Service}
			if _, ok := seen[key]; ok {
				return SetImagePolicySet{}, fmt.Errorf("duplicate set-image policy issuer, subject, app and service")
			}
			seen[key] = struct{}{}
			set.grants[setImageGrant{principal, p.App, p.Service, p.Repository}] = struct{}{}
		}
	}
	return set, nil
}

func (s SetImagePolicySet) Allows(principal WorkloadPrincipal, app, service, repository string) bool {
	_, ok := s.grants[setImageGrant{principal, app, service, repository}]
	return ok
}

// Repositories returns a sorted, deduplicated, caller-owned push allowlist.
func (s SetImagePolicySet) Repositories(principal WorkloadPrincipal) []string {
	repos := make(map[string]struct{})
	for g := range s.grants {
		if g.principal == principal {
			repos[g.repository] = struct{}{}
		}
	}
	result := make([]string, 0, len(repos))
	for repo := range repos {
		result = append(result, repo)
	}
	sort.Strings(result)
	return result
}

func (s SetImagePolicySet) HasAppPolicy(principal WorkloadPrincipal, app string) bool {
	for g := range s.grants {
		if g.principal == principal && g.app == app {
			return true
		}
	}
	return false
}

// AppSetImageRevert is one service whose desired image was introduced by a
// set-image change and that an apply would replace with the manifest image.
type AppSetImageRevert struct {
	Service     string
	ServerImage string
	FileImage   string
	Revision    string
	Principal   string
	ChangedAt   time.Time
}

// SetImageRevertError is ErrAppSetImageRevert carrying the desired revision
// the apply was checked against and the set-image changes it would revert.
type SetImageRevertError struct {
	DesiredRevision string
	Reverts         []AppSetImageRevert
}

func (e *SetImageRevertError) Error() string {
	services := make([]string, 0, len(e.Reverts))
	for _, revert := range e.Reverts {
		services = append(services, revert.Service)
	}
	return fmt.Sprintf("%s: desired revision %s, services %s", ErrAppSetImageRevert, e.DesiredRevision, strings.Join(services, ", "))
}

// Is matches ErrAppSetImageRevert.
func (e *SetImageRevertError) Is(target error) bool { return target == ErrAppSetImageRevert }

// AppSetImageProvenance records the accepted image change's immutable origin.
type AppSetImageProvenance struct {
	Service   string `json:"service,omitempty"`
	Image     string `json:"image,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Principal string `json:"principal,omitempty"`
	Operation string `json:"operation,omitempty"`
	AppID     string `json:"app_id,omitempty"`
}
