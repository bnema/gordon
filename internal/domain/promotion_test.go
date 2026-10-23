package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkloadPrincipal(t *testing.T) {
	for _, p := range []WorkloadPrincipal{{GordonIssuer, "owner"}, {"https://issuer.example", "repo:a#b"}} {
		got, err := ParseWorkloadPrincipal(p.String())
		require.NoError(t, err)
		require.Equal(t, p, got)
	}
	for _, s := range []string{"", "other:x", "gordon:", "oidc:#sub", "oidc:https://issuer", "oidc:http://issuer#sub", "oidc:https://issuer#"} {
		_, err := ParseWorkloadPrincipal(s)
		require.Error(t, err, s)
	}
}

func TestTrustedIssuer(t *testing.T) {
	good := TrustedIssuer{Issuer: "https://issuer.example/path", Audience: "aud"}
	require.NoError(t, good.Validate())
	defaults := good.WithDefaults()
	require.Equal(t, time.Hour, defaults.MaxTokenLifetime)
	require.Equal(t, time.Minute, defaults.ClockSkew)
	for _, raw := range []string{"", "http://issuer", "/issuer", "https:issuer", "https://user@issuer", "https://issuer?x", "https://issuer#x", "https://issuer?", "https://issuer#"} {
		c := good
		c.Issuer = raw
		require.Error(t, c.Validate(), raw)
		c = good
		c.JWKSURI = raw
		if raw != "" {
			require.Error(t, c.Validate(), raw)
		}
	}
	c := good
	c.Audience = ""
	require.Error(t, c.Validate())
	c = good
	c.MaxTokenLifetime = -time.Second
	require.Error(t, c.Validate())
	c = good
	c.ClockSkew = -time.Second
	require.Error(t, c.Validate())
}

func TestPromotionPolicySet(t *testing.T) {
	policies := []PromotionPolicy{{Issuer: GordonIssuer, Subjects: []string{"a", "b"}, App: "web", Service: "web", Repository: "web"}, {Issuer: "https://issuer", Subjects: []string{"a"}, App: "web", Service: "api", Repository: "api"}}
	set, err := NewPromotionPolicySet(policies)
	require.NoError(t, err)
	policies[0].Subjects[0] = "changed"
	policies[0].Repository = "changed"
	for _, sub := range []string{"a", "b"} {
		require.True(t, set.Allows(WorkloadPrincipal{GordonIssuer, sub}, "web", "web", "web"))
	}
	p := WorkloadPrincipal{GordonIssuer, "a"}
	require.True(t, set.HasAppPolicy(p, "web"))
	require.False(t, set.HasAppPolicy(p, "other"))
	require.Equal(t, []string{"web"}, set.Repositories(p))
	repos := set.Repositories(p)
	repos[0] = "changed"
	require.Equal(t, []string{"web"}, set.Repositories(p))
	for _, args := range [][4]string{{"other", "web", "web", "web"}, {"a", "other", "web", "web"}, {"a", "web", "other", "web"}, {"a", "web", "web", "other"}} {
		require.False(t, set.Allows(WorkloadPrincipal{GordonIssuer, args[0]}, args[1], args[2], args[3]))
	}
	require.False(t, set.Allows(WorkloadPrincipal{"https://issuer", "b"}, "web", "api", "api"))
	base := PromotionPolicy{Issuer: GordonIssuer, Subjects: []string{"a"}, App: "web", Service: "web", Repository: "web"}
	_, err = NewPromotionPolicySet([]PromotionPolicy{base, base})
	require.Error(t, err)
	for _, mutate := range []func(*PromotionPolicy){func(p *PromotionPolicy) { p.Issuer = "" }, func(p *PromotionPolicy) { p.Subjects = nil }, func(p *PromotionPolicy) { p.Subjects = []string{""} }, func(p *PromotionPolicy) { p.App = "Bad" }, func(p *PromotionPolicy) { p.Service = "Bad" }, func(p *PromotionPolicy) { p.Repository = "../bad" }, func(p *PromotionPolicy) { p.Issuer = "*" }, func(p *PromotionPolicy) { p.Subjects = []string{"a*"} }, func(p *PromotionPolicy) { p.App = "*" }, func(p *PromotionPolicy) { p.Service = "*" }, func(p *PromotionPolicy) { p.Repository = "*" }} {
		p := base
		mutate(&p)
		_, err := NewPromotionPolicySet([]PromotionPolicy{p})
		require.Error(t, err)
	}
}

func TestPromotionPersistence(t *testing.T) {
	var op AppOperation
	require.NoError(t, json.Unmarshal([]byte(`{"op":"old","kind":"deploy"}`), &op))
	require.Empty(t, op.Principal)
	require.Empty(t, op.Request.Image)
	var rev AppDesiredRevision
	require.NoError(t, json.Unmarshal([]byte(`{"revision":"old"}`), &rev))
	require.Nil(t, rev.Promotion)
	op.Principal = "gordon:owner"
	op.Kind = AppOperationKindPromote
	op.Request.Image = "web@sha256:abc"
	data, err := json.Marshal(op)
	require.NoError(t, err)
	var decoded AppOperation
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, op, decoded)
	rev.Promotion = &AppPromotionProvenance{Service: "web", Image: "web@sha256:abc", Digest: "sha256:abc", Principal: op.Principal, Operation: "op", AppID: "id"}
	data, err = json.Marshal(rev)
	require.NoError(t, err)
	var decodedRev AppDesiredRevision
	require.NoError(t, json.Unmarshal(data, &decodedRev))
	require.Equal(t, rev, decodedRev)
}

func TestPromotionPolicySetZeroAndDuplicateSubjects(t *testing.T) {
	var empty PromotionPolicySet
	p := WorkloadPrincipal{GordonIssuer, "owner"}
	require.False(t, empty.Allows(p, "web", "web", "web"))
	require.False(t, empty.HasAppPolicy(p, "web"))
	require.Empty(t, empty.Repositories(p))
	_, err := NewPromotionPolicySet([]PromotionPolicy{{Issuer: GordonIssuer, Subjects: []string{"owner", "owner"}, App: "web", Service: "web", Repository: "web"}})
	require.Error(t, err)
	set, err := NewPromotionPolicySet([]PromotionPolicy{{Issuer: GordonIssuer, Subjects: []string{"owner"}, App: "web", Service: "web", Repository: "shared"}, {Issuer: GordonIssuer, Subjects: []string{"owner"}, App: "web", Service: "api", Repository: "shared"}, {Issuer: GordonIssuer, Subjects: []string{"owner"}, App: "api", Service: "api", Repository: "api"}})
	require.NoError(t, err)
	require.Equal(t, []string{"api", "shared"}, set.Repositories(p))
}

func TestPromotionJSONOmitsAbsentFields(t *testing.T) {
	data, err := json.Marshal(AppOperation{})
	require.NoError(t, err)
	require.NotContains(t, string(data), `"principal"`)
	require.NotContains(t, string(data), `"image"`)
	data, err = json.Marshal(AppDesiredRevision{})
	require.NoError(t, err)
	require.NotContains(t, string(data), `"promotion"`)
}

func TestTrustedIssuerRejectsWildcards(t *testing.T) {
	for _, issuer := range []TrustedIssuer{{Issuer: "https://*.example", Audience: "aud"}, {Issuer: "https://issuer.example", Audience: "aud*"}, {Issuer: "https://issuer.example", Audience: "aud", JWKSURI: "https://issuer.example/*"}} {
		require.Error(t, issuer.Validate())
	}
}
