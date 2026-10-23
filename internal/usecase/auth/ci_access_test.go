package auth

import (
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

var ciSecret = []byte("test-secret-key-for-jwt-signing")

func ciService(t *testing.T, ttl time.Duration) *Service {
	t.Helper()
	// A strict mock: any token-store call (e.g. treating ci-access as stored) fails the test.
	return NewService(Config{Enabled: true, AuthType: domain.AuthTypeToken, TokenSecret: ciSecret, AccessTokenTTL: ttl},
		mocks.NewMockTokenStore(t), zerowrap.Default())
}

func ciPrincipal() domain.WorkloadPrincipal {
	return domain.WorkloadPrincipal{Issuer: "https://token.actions.githubusercontent.com", Subject: "repo:acme/web:environment:production"}
}

func decodeClaims(t *testing.T, raw string) jwt.MapClaims {
	t.Helper()
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return ciSecret, nil })
	require.NoError(t, err)
	return claims
}

func signCI(t *testing.T, secret []byte, method jwt.SigningMethod, claims jwt.MapClaims) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(method, claims).SignedString(secret)
	require.NoError(t, err)
	return raw
}

func TestCIAccessTokenRoundTrip(t *testing.T) {
	svc := ciService(t, 0)
	ctx := testContext()
	scopes := []string{"repository:web:push,pull"}
	for _, principal := range []domain.WorkloadPrincipal{ciPrincipal(), {Issuer: domain.GordonIssuer, Subject: "deployer"}} {
		raw, err := svc.GenerateCIAccessToken(ctx, principal, scopes, 0)
		require.NoError(t, err)

		claims := decodeClaims(t, raw)
		require.Equal(t, "ci-access", claims["token_type"])
		require.Equal(t, principal.String(), claims["sub"])
		require.Equal(t, TokenIssuer, claims["iss"])
		require.InDelta(t, DefaultAccessTokenTTL.Seconds(), claims["exp"].(float64)-claims["iat"].(float64), 1)

		gotPrincipal, gotScopes, err := svc.ValidateCIAccessToken(ctx, raw)
		require.NoError(t, err)
		require.Equal(t, principal, gotPrincipal)
		require.Equal(t, scopes, gotScopes)
	}
}

func TestCIAccessTokenTTL(t *testing.T) {
	ctx := testContext()
	cases := []struct {
		name       string
		configured time.Duration
		requested  time.Duration
		want       time.Duration
	}{
		{"configured access_token_ttl", 20 * time.Minute, 0, 20 * time.Minute},
		{"explicit ttl", 0, 5 * time.Minute, 5 * time.Minute},
		{"explicit ttl capped", 0, 2 * time.Hour, MaxAccessTokenLifetime},
		{"configured ttl capped", 3 * time.Hour, 0, MaxAccessTokenLifetime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := ciService(t, tc.configured).GenerateCIAccessToken(ctx, ciPrincipal(), nil, tc.requested)
			require.NoError(t, err)
			claims := decodeClaims(t, raw)
			require.InDelta(t, tc.want.Seconds(), claims["exp"].(float64)-claims["iat"].(float64), 1)
		})
	}
}

func TestGenerateCIAccessTokenRejectsInvalidPrincipal(t *testing.T) {
	svc := ciService(t, 0)
	for _, p := range []domain.WorkloadPrincipal{
		{},
		{Issuer: domain.GordonIssuer},
		{Issuer: "http://insecure.example", Subject: "x"},
		{Issuer: "https://issuer.example#frag", Subject: "x"},
		{Issuer: "https://issuer.example", Subject: ""},
	} {
		_, err := svc.GenerateCIAccessToken(testContext(), p, nil, 0)
		require.Error(t, err, "%+v", p)
	}
}

// ValidateToken backs every registry and admin authentication path
// (middleware.RegistryAuthV2, admin.AuthMiddleware, the /auth/token password
// exchange) and ExtendToken; all of them must refuse ci-access tokens.
func TestCIAccessTokenRejectedOutsideCIHost(t *testing.T) {
	svc := ciService(t, 0)
	ctx := testContext()
	raw, err := svc.GenerateCIAccessToken(ctx, ciPrincipal(), []string{"admin:*:*", "repository:web:push,pull"}, 0)
	require.NoError(t, err)

	claims, err := svc.ValidateToken(ctx, raw)
	require.ErrorIs(t, err, domain.ErrInvalidToken)
	require.Nil(t, claims)

	_, err = svc.ExtendToken(ctx, raw)
	require.Error(t, err)

	gordon, err := svc.GenerateCIAccessToken(ctx, domain.WorkloadPrincipal{Issuer: domain.GordonIssuer, Subject: "deployer"}, nil, 0)
	require.NoError(t, err)
	_, err = svc.ValidateToken(ctx, gordon)
	require.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestValidateCIAccessTokenRejectsOtherTokens(t *testing.T) {
	svc := ciService(t, 0)
	ctx := testContext()
	now := time.Now().UTC()
	access, err := svc.GenerateAccessToken(ctx, "deployer", []string{"repository:web:push"}, 5*time.Minute)
	require.NoError(t, err)

	valid := func() jwt.MapClaims {
		return jwt.MapClaims{"iss": TokenIssuer, "sub": ciPrincipal().String(), "token_type": "ci-access",
			"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	}
	with := func(change func(jwt.MapClaims)) jwt.MapClaims {
		c := valid()
		change(c)
		return c
	}
	rejected := map[string]string{
		"ephemeral access token": access,
		"stored token type":      signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { c["token_type"] = "stored" })),
		"no token type":          signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { delete(c, "token_type") })),
		"wrong secret":           signCI(t, []byte("other-secret"), jwt.SigningMethodHS256, valid()),
		"HS512":                  signCI(t, ciSecret, jwt.SigningMethodHS512, valid()),
		"wrong issuer":           signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { c["iss"] = "other" })),
		"no exp":                 signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { delete(c, "exp") })),
		"no iat":                 signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { delete(c, "iat") })),
		"expired":                signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { c["iat"], c["exp"] = now.Add(-time.Hour).Unix(), now.Add(-time.Minute).Unix() })),
		"future iat":             signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { c["iat"] = now.Add(5 * time.Minute).Unix() })),
		"lifetime above max":     signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { c["exp"] = now.Add(2 * time.Hour).Unix() })),
		"invalid principal":      signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { c["sub"] = "deployer" })),
		"empty principal":        signCI(t, ciSecret, jwt.SigningMethodHS256, with(func(c jwt.MapClaims) { c["sub"] = "" })),
		"none":                   noneToken(t, valid()),
		"garbage":                "garbage",
	}
	for name, raw := range rejected {
		t.Run(name, func(t *testing.T) {
			_, _, err := svc.ValidateCIAccessToken(ctx, raw)
			require.ErrorIs(t, err, domain.ErrInvalidToken)
		})
	}
	_, _, err = svc.ValidateCIAccessToken(ctx, signCI(t, ciSecret, jwt.SigningMethodHS256, valid()))
	require.NoError(t, err, "control: a hand-built valid ci-access token is accepted")
}

func noneToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	return raw
}
