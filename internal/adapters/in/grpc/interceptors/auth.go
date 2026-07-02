package interceptors

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/bnema/gordon/internal/domain"
)

// ComponentTokenValidator validates component bearer tokens for a required RPC scope.
type ComponentTokenValidator interface {
	ValidateToken(ctx context.Context, token string, required domain.ComponentScope) (*domain.ComponentIdentity, error)
}

// ComponentAuthUnaryInterceptor authenticates unary component RPCs using bearer tokens.
func ComponentAuthUnaryInterceptor(
	validator ComponentTokenValidator,
	methodScopes map[string]domain.ComponentScope,
) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		authedCtx, err := authenticateComponent(ctx, validator, methodScopes, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(authedCtx, req)
	}
}

// ComponentAuthStreamInterceptor authenticates streaming component RPCs using bearer tokens.
func ComponentAuthStreamInterceptor(
	validator ComponentTokenValidator,
	methodScopes map[string]domain.ComponentScope,
) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		authedCtx, err := authenticateComponent(stream.Context(), validator, methodScopes, info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &componentAuthServerStream{ServerStream: stream, ctx: authedCtx})
	}
}

type componentAuthServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *componentAuthServerStream) Context() context.Context {
	return s.ctx
}

func authenticateComponent(
	ctx context.Context,
	validator ComponentTokenValidator,
	methodScopes map[string]domain.ComponentScope,
	fullMethod string,
) (context.Context, error) {
	if validator == nil {
		return nil, status.Error(codes.PermissionDenied, "component auth validator not configured")
	}

	required, ok := methodScopes[fullMethod]
	if !ok || required == "" {
		return nil, status.Error(codes.PermissionDenied, "component RPC scope not configured")
	}

	token, ok := bearerTokenFromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "bearer token required")
	}

	identity, err := validator.ValidateToken(ctx, token, required)
	if err != nil {
		return nil, componentAuthStatusError(err)
	}
	if identity == nil {
		return nil, status.Error(codes.Unauthenticated, "component identity required")
	}

	return ContextWithComponentIdentity(ctx, identity), nil
}

func bearerTokenFromIncomingContext(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}

	for _, value := range md.Get("authorization") {
		scheme, token, ok := strings.Cut(strings.TrimSpace(value), " ")
		if ok && strings.EqualFold(scheme, "bearer") {
			token = strings.TrimSpace(token)
			if token != "" && !strings.ContainsAny(token, " \t\r\n") {
				return token, true
			}
		}
	}

	return "", false
}

func componentAuthStatusError(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}

	switch {
	case errors.Is(err, domain.ErrInvalidToken), errors.Is(err, domain.ErrTokenNotFound):
		return status.Error(codes.Unauthenticated, "invalid bearer token")
	case errors.Is(err, domain.ErrUnauthorized),
		errors.Is(err, domain.ErrRevokedToken),
		errors.Is(err, domain.ErrExpiredToken),
		errors.Is(err, domain.ErrInsufficientScope),
		errors.Is(err, domain.ErrInvalidScope):
		return status.Error(codes.PermissionDenied, "component token not permitted")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "component token validation canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "component token validation deadline exceeded")
	default:
		return status.Error(codes.Internal, "component token validation failed")
	}
}
