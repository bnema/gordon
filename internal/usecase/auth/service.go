// Package auth implements the authentication use case for the registry.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	// TokenIssuer is the issuer claim for generated tokens.
	TokenIssuer = "gordon-registry"
	// DefaultBcryptCost is the default cost for bcrypt hashing.
	DefaultBcryptCost = 12
	// MaxAccessTokenLifetime is the maximum lifetime for ephemeral access tokens.
	// Tokens with this lifetime or less skip store validation.
	MaxAccessTokenLifetime = 5 * time.Minute
	// maxAccessTokenLifetimeSecs is MaxAccessTokenLifetime in seconds for JWT comparisons.
	maxAccessTokenLifetimeSecs = int64(MaxAccessTokenLifetime / time.Second)
	// tokenExtensionTTL is the amount of time added to a token's expiry when extended.
	tokenExtensionTTL = 24 * time.Hour
	// tokenExtensionDebounce is the minimum time between token extensions.
	tokenExtensionDebounce = time.Hour
	// serviceTokenSubject is the subject used for internal service tokens.
	// Service tokens are not extended to avoid churn on the token store.
	serviceTokenSubject = "gordon-service"
)

// Config holds the authentication configuration.
type Config struct {
	Enabled      bool
	AuthType     domain.AuthType
	Username     string
	PasswordHash string        // bcrypt hash for password auth
	TokenSecret  []byte        // signing secret for token auth
	TokenExpiry  time.Duration // default token expiry (0 = never)
}

// Service implements the AuthService interface.
type Service struct {
	config     Config
	tokenStore out.TokenStore
	log        zerowrap.Logger
}

// NewService creates a new auth service.
func NewService(config Config, tokenStore out.TokenStore, log zerowrap.Logger) *Service {
	return &Service{
		config:     config,
		tokenStore: tokenStore,
		log:        log,
	}
}

// GetAuthType returns the configured authentication type.
func (s *Service) GetAuthType() domain.AuthType {
	return s.config.AuthType
}

// IsEnabled returns whether authentication is enabled.
func (s *Service) IsEnabled() bool {
	return s.config.Enabled
}

// ValidatePassword checks if the username and password are valid.
func (s *Service) ValidatePassword(ctx context.Context, username, password string) bool {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ValidatePassword",
		"username":            username,
	})
	log := zerowrap.FromCtx(ctx)

	// Constant-time username comparison
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(s.config.Username)) == 1

	// Bcrypt comparison (already constant-time)
	err := bcrypt.CompareHashAndPassword([]byte(s.config.PasswordHash), []byte(password))
	passwordMatch := err == nil

	if !usernameMatch || !passwordMatch {
		log.Debug().Bool("username_match", usernameMatch).Msg("password validation failed")
		return false
	}

	log.Debug().Msg("password validation successful")
	return true
}

// ValidateToken validates a JWT token and returns its claims.
func (s *Service) ValidateToken(ctx context.Context, tokenString string) (*domain.TokenClaims, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ValidateToken",
	})
	log := zerowrap.FromCtx(ctx)

	claims, err := s.parseTokenClaims(tokenString)
	if err != nil {
		log.Debug().Err(err).Msg("failed to parse token")
		return nil, err
	}

	tokenClaims := buildTokenClaims(claims)

	// Access tokens (short-lived, ≤5min, recently issued) skip store validation.
	// CLI-generated tokens must exist in store to prevent use of externally-created tokens.
	// Security: require token to be recently issued (within MaxAccessTokenLifetime) to prevent
	// attackers with stolen secrets from creating arbitrary short-lived tokens.
	isAccessToken := s.isEphemeralAccessToken(tokenClaims)

	if !isAccessToken {
		if err := s.ensureTokenExists(ctx, tokenClaims, log); err != nil {
			return nil, err
		}
		if err := s.ensureTokenNotRevoked(ctx, tokenClaims, log); err != nil {
			return nil, err
		}
	}

	if err := ensureTokenNotExpired(tokenClaims, log); err != nil {
		return nil, err
	}

	log.Debug().Str("subject", tokenClaims.Subject).Msg("token validation successful")
	return tokenClaims, nil
}

// ensureTokenExists verifies the token exists in the store and the JTI matches.
func (s *Service) ensureTokenExists(ctx context.Context, tokenClaims *domain.TokenClaims, log zerowrap.Logger) error {
	_, storedToken, err := s.tokenStore.GetToken(ctx, tokenClaims.Subject)
	if err != nil {
		if errors.Is(err, domain.ErrTokenNotFound) {
			log.Debug().Str("subject", tokenClaims.Subject).Msg("token not found in store")
			return domain.ErrInvalidToken
		}
		// SECURITY: Fail closed on store errors
		log.Error().Err(err).Msg("failed to verify token existence")
		return fmt.Errorf("unable to verify token: %w", err)
	}

	// Verify the token ID matches the stored token
	if storedToken.ID != tokenClaims.ID {
		log.Debug().
			Str("expected_id", storedToken.ID).
			Str("actual_id", tokenClaims.ID).
			Msg("token ID mismatch")
		return domain.ErrInvalidToken
	}

	return nil
}

func (s *Service) parseTokenClaims(tokenString string) (jwt.MapClaims, error) {
	// Parse token with issuer validation
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, domain.ErrInvalidToken
		}
		return s.config.TokenSecret, nil
	}, jwt.WithIssuer(TokenIssuer)) // SECURITY: Enforce issuer validation
	if err != nil {
		return nil, domain.ErrInvalidToken
	}

	if !token.Valid {
		return nil, domain.ErrInvalidToken
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, domain.ErrInvalidToken
	}

	// SECURITY: Double-check issuer claim matches expected value
	if iss, ok := claims["iss"].(string); !ok || iss != TokenIssuer {
		return nil, domain.ErrInvalidToken
	}

	return claims, nil
}

func buildTokenClaims(claims jwt.MapClaims) *domain.TokenClaims {
	tokenClaims := &domain.TokenClaims{
		Issuer: getStringClaim(claims, "iss"),
	}

	if jti, ok := claims["jti"].(string); ok {
		tokenClaims.ID = jti
	}
	if sub, ok := claims["sub"].(string); ok {
		tokenClaims.Subject = sub
	}
	if iat, ok := claims["iat"].(float64); ok {
		tokenClaims.IssuedAt = int64(iat)
	}
	if exp, ok := claims["exp"].(float64); ok {
		tokenClaims.ExpiresAt = int64(exp)
	}
	if scopes, ok := claims["scopes"].([]any); ok {
		for _, scope := range scopes {
			if s, ok := scope.(string); ok {
				tokenClaims.Scopes = append(tokenClaims.Scopes, s)
			}
		}
	}

	return tokenClaims
}

func (s *Service) ensureTokenNotRevoked(ctx context.Context, tokenClaims *domain.TokenClaims, log zerowrap.Logger) error {
	revoked, err := s.tokenStore.IsRevoked(ctx, tokenClaims.ID)
	if err != nil {
		// SECURITY: Fail closed - deny access on revocation check error
		log.Error().Err(err).Str("token_id", tokenClaims.ID).Msg("failed to check token revocation, denying access")
		return fmt.Errorf("unable to verify token status: %w", err)
	}
	if revoked {
		log.Debug().Str("token_id", tokenClaims.ID).Msg("token has been revoked")
		return domain.ErrRevokedToken
	}

	return nil
}

func ensureTokenNotExpired(tokenClaims *domain.TokenClaims, log zerowrap.Logger) error {
	if tokenClaims.ExpiresAt <= 0 {
		return nil
	}

	if time.Now().UTC().Unix() > tokenClaims.ExpiresAt {
		log.Debug().Str("token_id", tokenClaims.ID).Msg("token has expired")
		return domain.ErrExpiredToken
	}

	return nil
}

// isEphemeralAccessToken checks if a token is a short-lived access token that
// doesn't require store validation. Access tokens must:
// 1. Have a positive expiry (not never-expiring)
// 2. Have lifetime ≤ MaxAccessTokenLifetime
// 3. Be recently issued (within MaxAccessTokenLifetime) to prevent replay attacks
func (s *Service) isEphemeralAccessToken(claims *domain.TokenClaims) bool {
	if claims.ExpiresAt <= 0 {
		return false // Never-expiring tokens require store validation
	}

	lifetime := claims.ExpiresAt - claims.IssuedAt
	if lifetime > maxAccessTokenLifetimeSecs {
		return false // Long-lived tokens require store validation
	}

	// Security: ensure token was recently issued to prevent attackers with
	// stolen secrets from creating arbitrary short-lived tokens
	now := time.Now().UTC().Unix()
	tokenAge := now - claims.IssuedAt
	if tokenAge > maxAccessTokenLifetimeSecs {
		return false // Old tokens require store validation
	}

	return true
}

// GenerateToken creates a new JWT token for the given subject.
func (s *Service) GenerateToken(ctx context.Context, subject string, scopes []string, expiry time.Duration) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GenerateToken",
		"subject":             subject,
	})
	log := zerowrap.FromCtx(ctx)

	tokenID := uuid.New().String()
	now := time.Now().UTC()

	// Build token
	token := &domain.Token{
		ID:       tokenID,
		Subject:  subject,
		Scopes:   scopes,
		IssuedAt: now,
		Revoked:  false,
	}

	// Build JWT claims
	claims := jwt.MapClaims{
		"jti":    tokenID,
		"sub":    subject,
		"iss":    TokenIssuer,
		"iat":    now.Unix(),
		"nbf":    now.Unix(), // SECURITY: Not-before claim for clock skew protection
		"scopes": scopes,
	}

	// Set expiry if specified
	if expiry > 0 {
		expiresAt := now.Add(expiry)
		token.ExpiresAt = expiresAt
		claims["exp"] = expiresAt.Unix()
	}
	// If expiry is 0, token never expires (no exp claim)

	// Sign token
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := jwtToken.SignedString(s.config.TokenSecret)
	if err != nil {
		return "", log.WrapErr(err, "failed to sign token")
	}

	// Store token
	if err := s.tokenStore.SaveToken(ctx, token, tokenString); err != nil {
		return "", log.WrapErr(err, "failed to save token")
	}

	log.Info().
		Str("token_id", tokenID).
		Str("subject", subject).
		Time("expires_at", token.ExpiresAt).
		Msg("token generated")

	return tokenString, nil
}

// GenerateAccessToken creates a short-lived JWT for registry access without storing it.
// Used by /v2/token endpoint - these tokens don't need persistence.
// Expiry must be > 0 and <= MaxAccessTokenLifetime to prevent misuse.
func (s *Service) GenerateAccessToken(ctx context.Context, subject string, scopes []string, expiry time.Duration) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GenerateAccessToken",
		"subject":             subject,
	})
	log := zerowrap.FromCtx(ctx)

	// Enforce expiry constraints to prevent misuse
	if expiry <= 0 {
		return "", fmt.Errorf("access token expiry must be positive")
	}
	if expiry > MaxAccessTokenLifetime {
		return "", fmt.Errorf("access token expiry exceeds maximum of %v", MaxAccessTokenLifetime)
	}

	tokenID := uuid.New().String()
	now := time.Now().UTC()

	claims := jwt.MapClaims{
		"jti":    tokenID,
		"sub":    subject,
		"iss":    TokenIssuer,
		"iat":    now.Unix(),
		"nbf":    now.Unix(), // SECURITY: Not-before claim for clock skew protection
		"scopes": scopes,
		"exp":    now.Add(expiry).Unix(),
	}

	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := jwtToken.SignedString(s.config.TokenSecret)
	if err != nil {
		return "", log.WrapErr(err, "failed to sign access token")
	}

	log.Debug().
		Str("token_id", tokenID).
		Str("subject", subject).
		Msg("access token generated")

	return tokenString, nil
}

// RevokeToken revokes a token by its ID.
func (s *Service) RevokeToken(ctx context.Context, tokenID string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "RevokeToken",
		"token_id":            tokenID,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.tokenStore.Revoke(ctx, tokenID); err != nil {
		return log.WrapErr(err, "failed to revoke token")
	}

	log.Info().Msg("token revoked")
	return nil
}

// RevokeAllTokens revokes all stored tokens and returns the count of revoked tokens.
func (s *Service) RevokeAllTokens(ctx context.Context) (int, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "RevokeAllTokens",
	})
	log := zerowrap.FromCtx(ctx)

	tokens, err := s.tokenStore.ListTokens(ctx)
	if err != nil {
		return 0, log.WrapErr(err, "failed to list tokens")
	}

	revoked := 0
	for _, token := range tokens {
		if token.Revoked {
			continue // Already revoked
		}
		if err := s.tokenStore.Revoke(ctx, token.ID); err != nil {
			log.Warn().Err(err).Str("token_id", token.ID).Msg("failed to revoke token")
			continue
		}
		revoked++
	}

	log.Info().Int("count", revoked).Msg("all tokens revoked")
	return revoked, nil
}

// ListTokens returns all stored tokens.
func (s *Service) ListTokens(ctx context.Context) ([]domain.Token, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListTokens",
	})
	log := zerowrap.FromCtx(ctx)

	tokens, err := s.tokenStore.ListTokens(ctx)
	if err != nil {
		return nil, log.WrapErr(err, "failed to list tokens")
	}

	return tokens, nil
}

// ExtendToken re-issues the token with expiry slid forward by 24h.
// Returns the same token string if it was already extended within the debounce window (1h).
// Skips extension for ephemeral access tokens (≤5min) and service tokens.
func (s *Service) ExtendToken(ctx context.Context, tokenString string) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ExtendToken",
	})
	log := zerowrap.FromCtx(ctx)

	// Parse claims without store validation first (to check skip conditions cheaply)
	rawClaims, err := s.parseTokenClaims(tokenString)
	if err != nil {
		return "", domain.ErrInvalidToken
	}

	tokenClaims := buildTokenClaims(rawClaims)

	// Skip ephemeral access tokens (≤5min lifetime)
	if s.isEphemeralAccessToken(tokenClaims) {
		log.Debug().Msg("skipping extension for ephemeral access token")
		return tokenString, nil
	}

	// Skip service tokens
	if tokenClaims.Subject == serviceTokenSubject {
		log.Debug().Msg("skipping extension for service token")
		return tokenString, nil
	}

	// Full validation including store checks (not expired, not revoked, exists in store)
	if _, err := s.ValidateToken(ctx, tokenString); err != nil {
		return "", fmt.Errorf("token validation failed: %w", err)
	}

	// Fetch stored token to check debounce and get metadata
	_, storedToken, err := s.tokenStore.GetToken(ctx, tokenClaims.Subject)
	if err != nil {
		return "", fmt.Errorf("failed to fetch stored token: %w", err)
	}

	// Debounce: skip if already extended within the last hour
	if !storedToken.LastExtendedAt.IsZero() && time.Since(storedToken.LastExtendedAt) < tokenExtensionDebounce {
		log.Debug().
			Str("subject", tokenClaims.Subject).
			Time("last_extended_at", storedToken.LastExtendedAt).
			Msg("token extension debounced")
		return tokenString, nil
	}

	// Re-sign with same JTI but new expiry
	now := time.Now().UTC()
	newExpiresAt := now.Add(tokenExtensionTTL)

	newClaims := jwt.MapClaims{
		"jti":    tokenClaims.ID, // Reuse existing JTI to avoid invalidating concurrent requests
		"sub":    tokenClaims.Subject,
		"iss":    TokenIssuer,
		"iat":    now.Unix(), // Update to current time (token is being re-issued)
		"nbf":    now.Unix(), // SECURITY: not-before matches issuance time
		"scopes": tokenClaims.Scopes,
		"exp":    newExpiresAt.Unix(),
	}

	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, newClaims)
	newTokenString, err := jwtToken.SignedString(s.config.TokenSecret)
	if err != nil {
		return "", log.WrapErr(err, "failed to re-sign token")
	}

	// Update stored token with new JWT, expiry, and LastExtendedAt
	updatedToken := &domain.Token{
		ID:             storedToken.ID,
		Subject:        storedToken.Subject,
		Scopes:         storedToken.Scopes,
		IssuedAt:       storedToken.IssuedAt,
		ExpiresAt:      newExpiresAt,
		Revoked:        storedToken.Revoked,
		LastExtendedAt: now,
	}

	if err := s.tokenStore.UpdateTokenExpiry(ctx, updatedToken, newTokenString); err != nil {
		// Non-fatal: log and return original token rather than failing the request
		log.Warn().Err(err).Str("subject", tokenClaims.Subject).Msg("failed to persist extended token, returning original")
		return tokenString, nil
	}

	log.Debug().
		Str("subject", tokenClaims.Subject).
		Time("new_expires_at", newExpiresAt).
		Msg("token expiry extended")

	return newTokenString, nil
}

// GeneratePasswordHash generates a bcrypt hash for a password.
func (s *Service) GeneratePasswordHash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), DefaultBcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// getStringClaim safely extracts a string claim from JWT claims.
func getStringClaim(claims jwt.MapClaims, key string) string {
	if val, ok := claims[key].(string); ok {
		return val
	}
	return ""
}

// GetAuthStatus returns authentication status from context.
// Claims are already validated by AdminAuth middleware and stored in context.
func (s *Service) GetAuthStatus(ctx context.Context) (*domain.AuthStatus, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetAuthStatus",
	})

	// If auth is disabled, return valid status with no claims
	if !s.IsEnabled() {
		return &domain.AuthStatus{Valid: true}, nil
	}

	// Extract claims from context (set by AdminAuth middleware)
	claims := domain.GetTokenClaims(ctx)
	if claims == nil {
		return &domain.AuthStatus{Valid: false}, nil
	}

	return &domain.AuthStatus{
		Valid:     true,
		Subject:   claims.Subject,
		Scopes:    claims.Scopes,
		ExpiresAt: claims.ExpiresAt,
		IssuedAt:  claims.IssuedAt,
	}, nil
}
