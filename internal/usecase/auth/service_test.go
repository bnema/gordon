package auth

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"gordon/internal/boundaries/out/mocks"
	"gordon/internal/domain"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func TestService_GetAuthType(t *testing.T) {
	tests := []struct {
		name     string
		authType domain.AuthType
	}{
		{"password type", domain.AuthTypePassword},
		{"token type", domain.AuthTypeToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(Config{AuthType: tt.authType}, nil, zerowrap.Default())
			assert.Equal(t, tt.authType, svc.GetAuthType())
		})
	}
}

func TestService_IsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
	}{
		{"enabled", true},
		{"disabled", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(Config{Enabled: tt.enabled}, nil, zerowrap.Default())
			assert.Equal(t, tt.enabled, svc.IsEnabled())
		})
	}
}

func TestService_ValidatePassword_Success(t *testing.T) {
	password := "testpassword123"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)

	svc := NewService(Config{
		Enabled:      true,
		AuthType:     domain.AuthTypePassword,
		Username:     "testuser",
		PasswordHash: string(hash),
	}, nil, zerowrap.Default())

	ctx := testContext()
	assert.True(t, svc.ValidatePassword(ctx, "testuser", password))
}

func TestService_ValidatePassword_WrongPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correctpassword"), bcrypt.DefaultCost)
	require.NoError(t, err)

	svc := NewService(Config{
		Enabled:      true,
		AuthType:     domain.AuthTypePassword,
		Username:     "testuser",
		PasswordHash: string(hash),
	}, nil, zerowrap.Default())

	ctx := testContext()
	assert.False(t, svc.ValidatePassword(ctx, "testuser", "wrongpassword"))
}

func TestService_ValidatePassword_WrongUsername(t *testing.T) {
	password := "testpassword123"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)

	svc := NewService(Config{
		Enabled:      true,
		AuthType:     domain.AuthTypePassword,
		Username:     "testuser",
		PasswordHash: string(hash),
	}, nil, zerowrap.Default())

	ctx := testContext()
	assert.False(t, svc.ValidatePassword(ctx, "wronguser", password))
}

func TestService_GenerateToken_Success(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	// Capture the token to verify its properties
	var capturedToken *domain.Token
	var capturedJWT string

	tokenStore.EXPECT().
		SaveToken(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			mock.MatchedBy(func(token *domain.Token) bool {
				capturedToken = token
				return token.Subject == "testsubject" &&
					len(token.ID) > 0 &&
					len(token.Scopes) == 2 &&
					token.Scopes[0] == "push" &&
					token.Scopes[1] == "pull" &&
					!token.ExpiresAt.IsZero() &&
					!token.Revoked
			}),
			mock.MatchedBy(func(jwt string) bool {
				capturedJWT = jwt
				// JWT should have 3 parts separated by dots
				parts := 0
				for _, c := range jwt {
					if c == '.' {
						parts++
					}
				}
				return parts == 2 && len(jwt) > 50
			}),
		).
		Return(nil)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()
	token, err := svc.GenerateToken(ctx, "testsubject", []string{"push", "pull"}, time.Hour)

	assert.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.Equal(t, capturedJWT, token)
	assert.NotNil(t, capturedToken)
	assert.WithinDuration(t, time.Now().Add(time.Hour), capturedToken.ExpiresAt, 5*time.Second)
}

func TestService_GenerateToken_NeverExpires(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	tokenStore.EXPECT().
		SaveToken(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			mock.MatchedBy(func(token *domain.Token) bool {
				return token.Subject == "ci-bot" &&
					token.ExpiresAt.IsZero() // Never expires
			}),
			mock.MatchedBy(func(jwt string) bool {
				return len(jwt) > 50
			}),
		).
		Return(nil)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()
	token, err := svc.GenerateToken(ctx, "ci-bot", []string{"push", "pull"}, 0)

	assert.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestService_ValidateToken_Success(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	// Capture the token ID when saving
	var savedTokenID string
	tokenStore.EXPECT().
		SaveToken(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			mock.MatchedBy(func(token *domain.Token) bool {
				savedTokenID = token.ID
				return token.Subject == "testsubject"
			}),
			mock.MatchedBy(func(jwt string) bool { return len(jwt) > 50 }),
		).
		Return(nil)

	ctx := testContext()
	tokenStr, err := svc.GenerateToken(ctx, "testsubject", []string{"push", "pull"}, time.Hour)
	require.NoError(t, err)

	// Expect GetToken to be called to verify token exists
	tokenStore.EXPECT().
		GetToken(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			"testsubject",
		).
		Return(tokenStr, &domain.Token{
			ID:      savedTokenID,
			Subject: "testsubject",
			Scopes:  []string{"push", "pull"},
		}, nil)

	// Expect IsRevoked to be called with the same token ID
	tokenStore.EXPECT().
		IsRevoked(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			mock.MatchedBy(func(tokenID string) bool {
				return tokenID == savedTokenID
			}),
		).
		Return(false, nil)

	claims, err := svc.ValidateToken(ctx, tokenStr)

	assert.NoError(t, err)
	assert.NotNil(t, claims)
	assert.Equal(t, "testsubject", claims.Subject)
	assert.Equal(t, savedTokenID, claims.ID)
	assert.Equal(t, TokenIssuer, claims.Issuer)
	assert.Contains(t, claims.Scopes, "push")
	assert.Contains(t, claims.Scopes, "pull")
}

func TestService_ValidateToken_Revoked(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	var savedTokenID string
	tokenStore.EXPECT().
		SaveToken(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			mock.MatchedBy(func(token *domain.Token) bool {
				savedTokenID = token.ID
				return token.Subject == "testsubject"
			}),
			mock.MatchedBy(func(jwt string) bool { return len(jwt) > 50 }),
		).
		Return(nil)

	ctx := testContext()
	tokenStr, err := svc.GenerateToken(ctx, "testsubject", []string{"push", "pull"}, time.Hour)
	require.NoError(t, err)

	// Expect GetToken to be called to verify token exists
	tokenStore.EXPECT().
		GetToken(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			"testsubject",
		).
		Return(tokenStr, &domain.Token{
			ID:      savedTokenID,
			Subject: "testsubject",
			Scopes:  []string{"push", "pull"},
		}, nil)

	// Token is revoked
	tokenStore.EXPECT().
		IsRevoked(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			mock.MatchedBy(func(tokenID string) bool { return tokenID == savedTokenID }),
		).
		Return(true, nil)

	claims, err := svc.ValidateToken(ctx, tokenStr)

	assert.Error(t, err)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, domain.ErrRevokedToken)
}

func TestService_ValidateToken_InvalidSignature(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	// Service with different secret for validation
	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("different-secret-key"),
	}, tokenStore, zerowrap.Default())

	// Create a token with a different secret
	otherSvc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("original-secret-key"),
	}, tokenStore, zerowrap.Default())

	tokenStore.EXPECT().
		SaveToken(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			mock.MatchedBy(func(token *domain.Token) bool { return token.Subject == "testsubject" }),
			mock.MatchedBy(func(jwt string) bool { return len(jwt) > 50 }),
		).
		Return(nil)

	ctx := testContext()
	tokenStr, err := otherSvc.GenerateToken(ctx, "testsubject", []string{"push", "pull"}, time.Hour)
	require.NoError(t, err)

	// Try to validate with wrong secret - should fail before checking revocation
	claims, err := svc.ValidateToken(ctx, tokenStr)

	assert.Error(t, err)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestService_ValidateToken_Expired(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key"),
	}, tokenStore, zerowrap.Default())

	// Generate a token that expires immediately
	tokenStore.EXPECT().
		SaveToken(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			mock.MatchedBy(func(token *domain.Token) bool { return token.Subject == "testsubject" }),
			mock.MatchedBy(func(jwt string) bool { return len(jwt) > 50 }),
		).
		Return(nil)

	ctx := testContext()
	// Generate with very short expiry
	tokenStr, err := svc.GenerateToken(ctx, "testsubject", []string{"push"}, time.Millisecond)
	require.NoError(t, err)

	// Wait for token to expire
	time.Sleep(10 * time.Millisecond)

	// JWT library handles expiry during parsing - returns invalid token error
	// No IsRevoked call is made because parsing fails first
	claims, err := svc.ValidateToken(ctx, tokenStr)

	assert.Error(t, err)
	assert.Nil(t, claims)
	// JWT library returns invalid token for expired tokens during parsing
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestService_ValidateToken_MalformedToken(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()

	// Test with various malformed tokens
	malformedTokens := []string{
		"",
		"not-a-jwt",
		"only.two.parts.here.extra",
		"invalid.base64.token",
	}

	for _, token := range malformedTokens {
		claims, err := svc.ValidateToken(ctx, token)
		assert.Error(t, err, "expected error for token: %s", token)
		assert.Nil(t, claims)
		assert.ErrorIs(t, err, domain.ErrInvalidToken)
	}
}

func TestService_RevokeToken(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	tokenID := "550e8400-e29b-41d4-a716-446655440000"

	tokenStore.EXPECT().
		Revoke(
			mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil }),
			tokenID,
		).
		Return(nil)

	svc := NewService(Config{
		Enabled:  true,
		AuthType: domain.AuthTypeToken,
	}, tokenStore, zerowrap.Default())

	ctx := testContext()
	err := svc.RevokeToken(ctx, tokenID)

	assert.NoError(t, err)
}

func TestService_ListTokens(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	expectedTokens := []domain.Token{
		{
			ID:        "token-1",
			Subject:   "user1",
			Scopes:    []string{"push", "pull"},
			IssuedAt:  time.Now().Add(-24 * time.Hour),
			ExpiresAt: time.Now().Add(24 * time.Hour),
			Revoked:   false,
		},
		{
			ID:        "token-2",
			Subject:   "ci-bot",
			Scopes:    []string{"push", "pull"},
			IssuedAt:  time.Now().Add(-48 * time.Hour),
			ExpiresAt: time.Time{}, // Never expires
			Revoked:   false,
		},
	}

	tokenStore.EXPECT().
		ListTokens(mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil })).
		Return(expectedTokens, nil)

	svc := NewService(Config{
		Enabled:  true,
		AuthType: domain.AuthTypeToken,
	}, tokenStore, zerowrap.Default())

	ctx := testContext()
	tokens, err := svc.ListTokens(ctx)

	assert.NoError(t, err)
	assert.Len(t, tokens, 2)
	assert.Equal(t, "user1", tokens[0].Subject)
	assert.Equal(t, "ci-bot", tokens[1].Subject)
	assert.True(t, tokens[1].ExpiresAt.IsZero(), "CI bot token should never expire")
}

func TestService_GeneratePasswordHash(t *testing.T) {
	svc := NewService(Config{}, nil, zerowrap.Default())

	password := "securePassword123!"
	hash, err := svc.GeneratePasswordHash(password)

	assert.NoError(t, err)
	assert.NotEmpty(t, hash)
	assert.NotEqual(t, password, hash, "hash should not equal plain password")

	// Verify the hash works with bcrypt
	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	assert.NoError(t, err)

	// Verify wrong password fails
	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrongpassword"))
	assert.Error(t, err)
}

func TestService_GeneratePasswordHash_DifferentHashesForSamePassword(t *testing.T) {
	svc := NewService(Config{}, nil, zerowrap.Default())

	password := "testpassword"
	hash1, err := svc.GeneratePasswordHash(password)
	require.NoError(t, err)

	hash2, err := svc.GeneratePasswordHash(password)
	require.NoError(t, err)

	// Bcrypt should generate different hashes each time due to salt
	assert.NotEqual(t, hash1, hash2, "bcrypt should generate unique hashes")

	// But both should validate correctly
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash1), []byte(password)))
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash2), []byte(password)))
}
