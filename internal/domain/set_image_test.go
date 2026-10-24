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

func TestSetImagePolicySet(t *testing.T) {
	policies := []SetImagePolicy{{Issuer: GordonIssuer, Subjects: []string{"a", "b"}, App: "web", Service: "web", Repository: "web"}, {Issuer: "https://issuer", Subjects: []string{"a"}, App: "web", Service: "api", Repository: "api"}}
	set, err := NewSetImagePolicySet(policies)
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
	base := SetImagePolicy{Issuer: GordonIssuer, Subjects: []string{"a"}, App: "web", Service: "web", Repository: "web"}
	_, err = NewSetImagePolicySet([]SetImagePolicy{base, base})
	require.Error(t, err)
	for _, mutate := range []func(*SetImagePolicy){func(p *SetImagePolicy) { p.Issuer = "" }, func(p *SetImagePolicy) { p.Subjects = nil }, func(p *SetImagePolicy) { p.Subjects = []string{""} }, func(p *SetImagePolicy) { p.App = "Bad" }, func(p *SetImagePolicy) { p.Service = "Bad" }, func(p *SetImagePolicy) { p.Repository = "../bad" }, func(p *SetImagePolicy) { p.Issuer = "*" }, func(p *SetImagePolicy) { p.Subjects = []string{"a*"} }, func(p *SetImagePolicy) { p.App = "*" }, func(p *SetImagePolicy) { p.Service = "*" }, func(p *SetImagePolicy) { p.Repository = "*" }} {
		p := base
		mutate(&p)
		_, err := NewSetImagePolicySet([]SetImagePolicy{p})
		require.Error(t, err)
	}
}

func TestSetImagePersistence(t *testing.T) {
	var op AppOperation
	require.NoError(t, json.Unmarshal([]byte(`{"op":"old","kind":"deploy"}`), &op))
	require.Empty(t, op.Principal)
	require.Empty(t, op.Request.Image)
	var rev AppDesiredRevision
	require.NoError(t, json.Unmarshal([]byte(`{"revision":"old"}`), &rev))
	require.Nil(t, rev.SetImage)
	op.Principal = "gordon:owner"
	op.Kind = AppOperationKindSetImage
	op.Request.Image = "web@sha256:abc"
	data, err := json.Marshal(op)
	require.NoError(t, err)
	var decoded AppOperation
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, op, decoded)
	rev.SetImage = &AppSetImageProvenance{Service: "web", Image: "web@sha256:abc", Digest: "sha256:abc", Principal: op.Principal, Operation: "op", AppID: "id"}
	data, err = json.Marshal(rev)
	require.NoError(t, err)
	var decodedRev AppDesiredRevision
	require.NoError(t, json.Unmarshal(data, &decodedRev))
	require.Equal(t, rev, decodedRev)
}

func TestSetImagePolicySetZeroAndDuplicateSubjects(t *testing.T) {
	var empty SetImagePolicySet
	p := WorkloadPrincipal{GordonIssuer, "owner"}
	require.False(t, empty.Allows(p, "web", "web", "web"))
	require.False(t, empty.HasAppPolicy(p, "web"))
	require.Empty(t, empty.Repositories(p))
	_, err := NewSetImagePolicySet([]SetImagePolicy{{Issuer: GordonIssuer, Subjects: []string{"owner", "owner"}, App: "web", Service: "web", Repository: "web"}})
	require.Error(t, err)
	set, err := NewSetImagePolicySet([]SetImagePolicy{{Issuer: GordonIssuer, Subjects: []string{"owner"}, App: "web", Service: "web", Repository: "shared"}, {Issuer: GordonIssuer, Subjects: []string{"owner"}, App: "web", Service: "api", Repository: "shared"}, {Issuer: GordonIssuer, Subjects: []string{"owner"}, App: "api", Service: "api", Repository: "api"}})
	require.NoError(t, err)
	require.Equal(t, []string{"api", "shared"}, set.Repositories(p))
}

func TestSetImageJSONOmitsAbsentFields(t *testing.T) {
	data, err := json.Marshal(AppOperation{})
	require.NoError(t, err)
	require.NotContains(t, string(data), `"principal"`)
	require.NotContains(t, string(data), `"image"`)
	data, err = json.Marshal(AppDesiredRevision{})
	require.NoError(t, err)
	require.NotContains(t, string(data), `"set_image"`)
}

func TestTrustedIssuerRejectsWildcards(t *testing.T) {
	for _, issuer := range []TrustedIssuer{{Issuer: "https://*.example", Audience: "aud"}, {Issuer: "https://issuer.example", Audience: "aud*"}, {Issuer: "https://issuer.example", Audience: "aud", JWKSURI: "https://issuer.example/*"}} {
		require.Error(t, issuer.Validate())
	}
}

func TestSetImageExactIdentityEdges(t *testing.T) {
	// HTTPS issuer URLs accept Unicode hosts and explicit ports syntactically.
	// Identity matching remains byte-exact; no IDNA, port or case normalization.
	for _, issuer := range []string{"https://issuer.example", "https://issuer.example/", "https://ISSUER.example", "https://例え.example", "https://issuer.example:443"} {
		require.NoError(t, (TrustedIssuer{Issuer: issuer, Audience: "aud"}).Validate())
		principal := WorkloadPrincipal{issuer, " subject:é\t "}
		parsed, err := ParseWorkloadPrincipal(principal.String())
		require.NoError(t, err)
		require.Equal(t, principal, parsed)
		set, err := NewSetImagePolicySet([]SetImagePolicy{{Issuer: issuer, Subjects: []string{principal.Subject}, App: "web", Service: "web", Repository: "web"}})
		require.NoError(t, err)
		require.True(t, set.Allows(principal, "web", "web", "web"))
		for _, other := range []string{"subject:é", " subject:e\u0301\t ", " subject:é\t"} {
			require.False(t, set.Allows(WorkloadPrincipal{issuer, other}, "web", "web", "web"))
		}
		for _, other := range []string{"https://issuer.example", "https://issuer.example/", "https://ISSUER.example", "https://issuer.example:443"} {
			if other != issuer {
				require.False(t, set.Allows(WorkloadPrincipal{other, principal.Subject}, "web", "web", "web"))
			}
		}
	}
}
