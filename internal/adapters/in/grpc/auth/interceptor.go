package auth

import (
	"context"
	"reflect"
	"strings"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryInterceptor extracts and validates tokens from gRPC metadata.
func UnaryInterceptor(authSvc in.AuthService) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if isNilAuthService(authSvc) || !authSvc.IsEnabled() || !shouldAuthenticate(info.FullMethod) {
			return handler(ctx, req)
		}

		ctx, err := authenticate(ctx, authSvc)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// StreamInterceptor authenticates streaming RPCs.
func StreamInterceptor(authSvc in.AuthService) grpc.StreamServerInterceptor {
	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isNilAuthService(authSvc) || !authSvc.IsEnabled() || !shouldAuthenticate(info.FullMethod) {
			return handler(srv, stream)
		}

		ctx, err := authenticate(stream.Context(), authSvc)
		if err != nil {
			return err
		}

		wrapped := &wrappedStream{
			ServerStream: stream,
			ctx:          ctx,
		}

		return handler(srv, wrapped)
	}
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context {
	return w.ctx
}

// CheckAccess verifies the caller has required permissions.
func CheckAccess(ctx context.Context, resource, action string) error {
	scopes, ok := ctx.Value(domain.ContextKeyScopes).([]string)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing scopes in context")
	}

	if !domain.HasAdminAccess(scopes, resource, action) {
		return status.Error(codes.PermissionDenied, "insufficient permissions")
	}

	return nil
}

// GetSubject returns the authenticated subject from context.
func GetSubject(ctx context.Context) (string, error) {
	subject, ok := ctx.Value(domain.ContextKeySubject).(string)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing subject in context")
	}
	return subject, nil
}

func authenticate(ctx context.Context, authSvc in.AuthService) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	authHeader := md.Get("authorization")
	if len(authHeader) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization header")
	}

	headerValue := authHeader[0]
	if !strings.HasPrefix(headerValue, "Bearer ") {
		return nil, status.Error(codes.Unauthenticated, "invalid authorization header format: expected 'Bearer <token>'")
	}

	token := strings.TrimPrefix(headerValue, "Bearer ")
	if token == "" {
		return nil, status.Error(codes.Unauthenticated, "missing token")
	}

	claims, err := authSvc.ValidateToken(ctx, token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	if !hasAdminScope(claims.Scopes) {
		return nil, status.Error(codes.PermissionDenied, "admin scope required")
	}

	ctx = context.WithValue(ctx, domain.ContextKeyScopes, claims.Scopes)
	ctx = context.WithValue(ctx, domain.ContextKeySubject, claims.Subject)
	ctx = context.WithValue(ctx, domain.TokenClaimsKey, claims)

	return ctx, nil
}

func hasAdminScope(scopes []string) bool {
	for _, scope := range scopes {
		if strings.HasPrefix(scope, domain.ScopeTypeAdmin+":") {
			return true
		}
	}
	return false
}

func shouldAuthenticate(fullMethod string) bool {
	if !strings.HasPrefix(fullMethod, "/gordon.AdminService/") {
		return false
	}

	return !strings.HasSuffix(fullMethod, "/AuthenticatePassword")
}

func isNilAuthService(authSvc in.AuthService) bool {
	if authSvc == nil {
		return true
	}

	value := reflect.ValueOf(authSvc)
	if value.Kind() == reflect.Ptr {
		return value.IsNil()
	}

	return false
}
