// Package auth implements the authentication use case for the registry.
package auth

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"gordon/internal/boundaries/out"
	"gordon/internal/domain"
)

const (
	// TokenIssuer is the issuer claim for generated tokens.
	TokenIssuer = "gordon-registry"
	// DefaultBcryptCost is the default cost for bcrypt hashing.
	DefaultBcryptCost = 12
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

	if err := s.ensureTokenNotRevoked(ctx, tokenClaims, log); err != nil {
		return nil, err
	}

	if err := ensureTokenNotExpired(tokenClaims, log); err != nil {
		return nil, err
	}

	log.Debug().Str("subject", tokenClaims.Subject).Msg("token validation successful")
	return tokenClaims, nil
}

func (s *Service) parseTokenClaims(tokenString string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, domain.ErrInvalidToken
		}
		return s.config.TokenSecret, nil
	})
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
		log.Warn().Err(err).Msg("failed to check token revocation")
		return nil
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

	if time.Now().Unix() > tokenClaims.ExpiresAt {
		log.Debug().Str("token_id", tokenClaims.ID).Msg("token has expired")
		return domain.ErrExpiredToken
	}

	return nil
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
	now := time.Now()

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
