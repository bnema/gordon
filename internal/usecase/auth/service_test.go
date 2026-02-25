package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
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

func TestService_GenerateAccessToken_NoStorage(t *testing.T) {
	// Access tokens should NOT be stored - this is the fix for the 401 bug
	tokenStore := mocks.NewMockTokenStore(t)
	// No SaveToken expectation - it should NOT be called

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()
	token, err := svc.GenerateAccessToken(ctx, "testuser", []string{"repository:myrepo:push,pull"}, 5*time.Minute)

	assert.NoError(t, err)
	assert.NotEmpty(t, token)

	// Verify the token is valid and has correct claims
	claims, err := svc.ValidateToken(ctx, token)
	assert.NoError(t, err)
	assert.Equal(t, "testuser", claims.Subject)
	assert.Contains(t, claims.Scopes, "repository:myrepo:push,pull")
	// Access token should have exactly 5 min expiry (allow 2 sec tolerance for test execution)
	assert.InDelta(t, 300, claims.ExpiresAt-claims.IssuedAt, 2)
}

func TestService_GenerateAccessToken_RejectsInvalidExpiry(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()

	// Zero expiry should be rejected
	_, err := svc.GenerateAccessToken(ctx, "testuser", []string{"repository:myrepo:pull"}, 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expiry must be positive")

	// Negative expiry should be rejected
	_, err = svc.GenerateAccessToken(ctx, "testuser", []string{"repository:myrepo:pull"}, -time.Minute)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expiry must be positive")

	// Expiry exceeding max should be rejected
	_, err = svc.GenerateAccessToken(ctx, "testuser", []string{"repository:myrepo:pull"}, 10*time.Minute)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestService_ValidateToken_LongLivedRequiresStore(t *testing.T) {
	// Tokens with expiry > 5 minutes must exist in store
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()

	// Generate a long-lived token (stored)
	var capturedToken *domain.Token
	tokenStore.EXPECT().
		SaveToken(mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, t *domain.Token, _ string) {
			capturedToken = t
		}).
		Return(nil)

	token, err := svc.GenerateToken(ctx, "testuser", []string{"push", "pull"}, time.Hour)
	assert.NoError(t, err)

	// Validation should call GetToken and IsRevoked
	tokenStore.EXPECT().
		GetToken(mock.Anything, "testuser").
		Return(token, capturedToken, nil)
	tokenStore.EXPECT().
		IsRevoked(mock.Anything, capturedToken.ID).
		Return(false, nil)

	claims, err := svc.ValidateToken(ctx, token)
	assert.NoError(t, err)
	assert.Equal(t, "testuser", claims.Subject)
}

func TestService_ValidateToken_NeverExpiringRequiresStore(t *testing.T) {
	// Tokens with ExpiresAt = 0 (never expires) must exist in store
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()

	// Generate a never-expiring token (stored)
	var capturedToken *domain.Token
	tokenStore.EXPECT().
		SaveToken(mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, t *domain.Token, _ string) {
			capturedToken = t
		}).
		Return(nil)

	token, err := svc.GenerateToken(ctx, "ci-bot", []string{"push", "pull"}, 0)
	assert.NoError(t, err)

	// Validation should call GetToken and IsRevoked (not bypass store)
	tokenStore.EXPECT().
		GetToken(mock.Anything, "ci-bot").
		Return(token, capturedToken, nil)
	tokenStore.EXPECT().
		IsRevoked(mock.Anything, capturedToken.ID).
		Return(false, nil)

	claims, err := svc.ValidateToken(ctx, token)
	assert.NoError(t, err)
	assert.Equal(t, "ci-bot", claims.Subject)
	assert.Equal(t, int64(0), claims.ExpiresAt) // Never expires
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

func TestService_RevokeAllTokens(t *testing.T) {
	t.Run("revokes all active tokens", func(t *testing.T) {
		tokenStore := mocks.NewMockTokenStore(t)

		tokens := []domain.Token{
			{ID: "token-1", Subject: "user1", Revoked: false},
			{ID: "token-2", Subject: "user2", Revoked: false},
			{ID: "token-3", Subject: "user3", Revoked: true}, // already revoked
		}

		tokenStore.EXPECT().
			ListTokens(mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil })).
			Return(tokens, nil)

		// Should only revoke the two non-revoked tokens
		tokenStore.EXPECT().
			Revoke(mock.Anything, "token-1").
			Return(nil)
		tokenStore.EXPECT().
			Revoke(mock.Anything, "token-2").
			Return(nil)

		svc := NewService(Config{
			Enabled:  true,
			AuthType: domain.AuthTypeToken,
		}, tokenStore, zerowrap.Default())

		ctx := testContext()
		count, err := svc.RevokeAllTokens(ctx)

		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})

	t.Run("returns zero when no active tokens", func(t *testing.T) {
		tokenStore := mocks.NewMockTokenStore(t)

		tokens := []domain.Token{
			{ID: "token-1", Subject: "user1", Revoked: true},
		}

		tokenStore.EXPECT().
			ListTokens(mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil })).
			Return(tokens, nil)

		svc := NewService(Config{
			Enabled:  true,
			AuthType: domain.AuthTypeToken,
		}, tokenStore, zerowrap.Default())

		ctx := testContext()
		count, err := svc.RevokeAllTokens(ctx)

		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("returns zero when no tokens exist", func(t *testing.T) {
		tokenStore := mocks.NewMockTokenStore(t)

		tokenStore.EXPECT().
			ListTokens(mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil })).
			Return([]domain.Token{}, nil)

		svc := NewService(Config{
			Enabled:  true,
			AuthType: domain.AuthTypeToken,
		}, tokenStore, zerowrap.Default())

		ctx := testContext()
		count, err := svc.RevokeAllTokens(ctx)

		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("returns error when ListTokens fails", func(t *testing.T) {
		tokenStore := mocks.NewMockTokenStore(t)

		tokenStore.EXPECT().
			ListTokens(mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil })).
			Return(nil, errors.New("storage unavailable"))

		svc := NewService(Config{
			Enabled:  true,
			AuthType: domain.AuthTypeToken,
		}, tokenStore, zerowrap.Default())

		ctx := testContext()
		count, err := svc.RevokeAllTokens(ctx)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list tokens")
		assert.Equal(t, 0, count)
	})

	t.Run("continues on partial revoke failure", func(t *testing.T) {
		tokenStore := mocks.NewMockTokenStore(t)

		tokens := []domain.Token{
			{ID: "token-1", Subject: "user1", Revoked: false},
			{ID: "token-2", Subject: "user2", Revoked: false},
			{ID: "token-3", Subject: "user3", Revoked: false},
		}

		tokenStore.EXPECT().
			ListTokens(mock.MatchedBy(func(ctx context.Context) bool { return ctx != nil })).
			Return(tokens, nil)

		// First token succeeds
		tokenStore.EXPECT().
			Revoke(mock.Anything, "token-1").
			Return(nil)
		// Second token fails
		tokenStore.EXPECT().
			Revoke(mock.Anything, "token-2").
			Return(errors.New("revoke failed"))
		// Third token succeeds
		tokenStore.EXPECT().
			Revoke(mock.Anything, "token-3").
			Return(nil)

		svc := NewService(Config{
			Enabled:  true,
			AuthType: domain.AuthTypeToken,
		}, tokenStore, zerowrap.Default())

		ctx := testContext()
		count, err := svc.RevokeAllTokens(ctx)

		// Should not return error, but count only successful revokes
		require.NoError(t, err)
		assert.Equal(t, 2, count)
	})
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

func TestService_GenerateToken_HasNbfClaim(t *testing.T) {
	// Test that generated tokens include the nbf (not-before) claim
	// This is a security requirement per SEC-MED-006
	tokenStore := mocks.NewMockTokenStore(t)

	tokenStore.EXPECT().
		SaveToken(mock.Anything, mock.Anything, mock.Anything).
		Return(nil)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()
	tokenStr, err := svc.GenerateToken(ctx, "testsubject", []string{"push"}, time.Hour)
	require.NoError(t, err)

	// Parse the token to verify nbf claim
	claims, err := svc.parseTokenClaims(tokenStr)
	require.NoError(t, err)

	// Verify nbf claim exists and is valid
	nbf, ok := claims["nbf"].(float64)
	require.True(t, ok, "nbf claim should exist and be a number")

	iat, ok := claims["iat"].(float64)
	require.True(t, ok, "iat claim should exist")

	// nbf should equal iat for immediate validity
	assert.Equal(t, iat, nbf, "nbf should equal iat")
	assert.InDelta(t, time.Now().Unix(), int64(nbf), 5, "nbf should be current time")
}

func TestService_GenerateAccessToken_HasNbfClaim(t *testing.T) {
	// Test that access tokens also include the nbf claim
	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, nil, zerowrap.Default())

	ctx := testContext()
	tokenStr, err := svc.GenerateAccessToken(ctx, "testsubject", []string{"pull"}, time.Minute)
	require.NoError(t, err)

	// Parse the token to verify nbf claim
	claims, err := svc.parseTokenClaims(tokenStr)
	require.NoError(t, err)

	// Verify nbf claim exists
	nbf, ok := claims["nbf"].(float64)
	require.True(t, ok, "nbf claim should exist and be a number")

	iat, ok := claims["iat"].(float64)
	require.True(t, ok, "iat claim should exist")

	assert.Equal(t, iat, nbf, "nbf should equal iat for access tokens")
}

func TestExtendTokenSlidesExpiry(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()

	// Generate a token with a short remaining life (1h) so ExtendToken produces a different exp
	var capturedToken *domain.Token
	tokenStore.EXPECT().
		SaveToken(mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, t *domain.Token, _ string) {
			capturedToken = t
		}).
		Return(nil)

	tokenStr, err := svc.GenerateToken(ctx, "testuser", []string{"admin:*:*"}, time.Hour)
	require.NoError(t, err)

	// ExtendToken flow:
	// 1. ValidateToken → GetToken (ensureTokenExists) + IsRevoked
	tokenStore.EXPECT().
		GetToken(mock.Anything, "testuser").
		Return(tokenStr, capturedToken, nil).Once()
	tokenStore.EXPECT().
		IsRevoked(mock.Anything, capturedToken.ID).
		Return(false, nil).Once()

	// 2. GetToken again for debounce check
	tokenStore.EXPECT().
		GetToken(mock.Anything, "testuser").
		Return(tokenStr, capturedToken, nil).Once()

	// 3. UpdateTokenExpiry
	var updatedToken *domain.Token
	tokenStore.EXPECT().
		UpdateTokenExpiry(mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, t *domain.Token, _ string) {
			updatedToken = t
		}).
		Return(nil)

	// Extend it
	newTokenStr, err := svc.ExtendToken(ctx, tokenStr)
	require.NoError(t, err, "ExtendToken should not fail")

	// Since the original token expires in 1h and the new one expires in 24h, they must differ
	assert.NotEqual(t, tokenStr, newTokenStr, "expected a new token string after extension")
	assert.NotNil(t, updatedToken)
	assert.False(t, updatedToken.LastExtendedAt.IsZero(), "LastExtendedAt should be set")

	// New token must be valid — set up mock expectations for ValidateToken
	tokenStore.EXPECT().
		GetToken(mock.Anything, "testuser").
		Return(newTokenStr, updatedToken, nil)
	tokenStore.EXPECT().
		IsRevoked(mock.Anything, capturedToken.ID).
		Return(false, nil)

	claims, err := svc.ValidateToken(ctx, newTokenStr)
	require.NoError(t, err, "new token must be valid")

	// New token should have ~24h expiry from now
	expectedExpiry := time.Now().Add(24 * time.Hour)
	actualExpiry := time.Unix(claims.ExpiresAt, 0)
	assert.WithinDuration(t, expectedExpiry, actualExpiry, 5*time.Minute, "expiry should be ~24h from now")
}

func TestExtendTokenDebounce(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()

	// Generate a short-lived token (1h) so the first extension produces a different JWT
	var capturedToken *domain.Token
	tokenStore.EXPECT().
		SaveToken(mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, t *domain.Token, _ string) {
			capturedToken = t
		}).
		Return(nil)

	tokenStr, err := svc.GenerateToken(ctx, "testuser", []string{"admin:*:*"}, time.Hour)
	require.NoError(t, err)

	// First extend: flow is ValidateToken(GetToken+IsRevoked) + GetToken(debounce) + UpdateTokenExpiry
	tokenStore.EXPECT().
		GetToken(mock.Anything, "testuser").
		Return(tokenStr, capturedToken, nil).Once()
	tokenStore.EXPECT().
		IsRevoked(mock.Anything, capturedToken.ID).
		Return(false, nil).Once()
	tokenStore.EXPECT().
		GetToken(mock.Anything, "testuser").
		Return(tokenStr, capturedToken, nil).Once()

	var extendedToken *domain.Token
	var extendedJWT string
	tokenStore.EXPECT().
		UpdateTokenExpiry(mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, t *domain.Token, jwt string) {
			extendedToken = t
			extendedJWT = jwt
		}).
		Return(nil).Once()

	newToken1, err := svc.ExtendToken(ctx, tokenStr)
	require.NoError(t, err)
	assert.NotEqual(t, tokenStr, newToken1, "first extend should produce a new token")
	assert.NotNil(t, extendedToken)

	// Second extend immediately — debounce should kick in (LastExtendedAt just set to now)
	// Flow: ValidateToken(GetToken+IsRevoked) + GetToken(debounce) → debounced, no UpdateTokenExpiry
	tokenStore.EXPECT().
		GetToken(mock.Anything, "testuser").
		Return(extendedJWT, extendedToken, nil).Once()
	tokenStore.EXPECT().
		IsRevoked(mock.Anything, capturedToken.ID).
		Return(false, nil).Once()
	tokenStore.EXPECT().
		GetToken(mock.Anything, "testuser").
		Return(extendedJWT, extendedToken, nil).Once()

	// No UpdateTokenExpiry should be called — debounced
	newToken2, err := svc.ExtendToken(ctx, newToken1)
	require.NoError(t, err)

	assert.Equal(t, newToken1, newToken2, "expected debounce to return same token within 1h window")
}

func TestExtendTokenSkipsEphemeral(t *testing.T) {
	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, nil, zerowrap.Default())

	ctx := testContext()

	// Generate an ephemeral access token (≤5min)
	tokenStr, err := svc.GenerateAccessToken(ctx, "testuser", []string{"pull"}, 5*time.Minute)
	require.NoError(t, err)

	// ExtendToken should skip ephemeral tokens and return the same string
	result, err := svc.ExtendToken(ctx, tokenStr)
	require.NoError(t, err)
	assert.Equal(t, tokenStr, result, "ephemeral tokens should not be extended")
}

func newTestAuthService(t *testing.T) (*Service, *mocks.MockTokenStore) {
	t.Helper()
	tokenStore := mocks.NewMockTokenStore(t)
	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())
	return svc, tokenStore
}

func TestIsEphemeralAccessTokenRejectsFutureIat(t *testing.T) {
	// A token with iat in the future should NOT be treated as ephemeral
	// (otherwise it bypasses revocation checks)
	svc, _ := newTestAuthService(t)

	// Craft a token with iat = now+10min, exp = now+15min (age appears as -10min → negative → < 5min).
	// nbf is set to current time (not future) so the JWT library's nbf check does not reject it —
	// we need to isolate the iat bypass specifically.
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"jti":    "test-jti",
		"sub":    "testuser",
		"iss":    TokenIssuer,
		"iat":    now.Add(10 * time.Minute).Unix(), // future iat — the attack vector
		"nbf":    now.Add(-1 * time.Second).Unix(), // current nbf so nbf check passes
		"exp":    now.Add(15 * time.Minute).Unix(), // exp - iat = 5min → looks ephemeral
		"scopes": []string{"admin:*:*"},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(svc.config.TokenSecret)
	if err != nil {
		t.Fatal(err)
	}

	// Parsing should either reject the token outright (future iat) or not classify it as ephemeral
	rawClaims, err := svc.parseTokenClaims(tokenStr)
	if err != nil {
		// Acceptable: future iat rejected at parse time
		t.Logf("token with future iat rejected at parse: %v", err)
		return
	}
	tokenClaims := buildTokenClaims(rawClaims)
	if svc.isEphemeralAccessToken(tokenClaims) {
		t.Error("token with future iat must NOT be classified as ephemeral — it bypasses revocation")
	}
}

func TestExtendTokenSkipsServiceToken(t *testing.T) {
	tokenStore := mocks.NewMockTokenStore(t)

	svc := NewService(Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: []byte("test-secret-key-for-jwt-signing"),
	}, tokenStore, zerowrap.Default())

	ctx := testContext()

	// Generate a service token — service tokens skip extension before store checks
	tokenStore.EXPECT().
		SaveToken(mock.Anything, mock.Anything, mock.Anything).
		Return(nil)

	const svcSubject = "gordon-service"
	tokenStr, err := svc.GenerateToken(ctx, svcSubject, []string{"pull"}, 24*time.Hour)
	require.NoError(t, err)

	// ExtendToken should skip service tokens WITHOUT touching the store
	result, err := svc.ExtendToken(ctx, tokenStr)
	require.NoError(t, err)
	assert.Equal(t, tokenStr, result, "service tokens should not be extended")
}
