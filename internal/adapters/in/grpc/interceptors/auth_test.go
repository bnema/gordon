package interceptors

import (
	"context"
	"errors"
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testAuthMethod = "/gordon.component.Runtime/Deploy"

type fakeComponentTokenValidator struct {
	identity *domain.ComponentIdentity
	err      error

	called   bool
	token    string
	required domain.ComponentScope
}

func (f *fakeComponentTokenValidator) ValidateToken(_ context.Context, token string, required domain.ComponentScope) (*domain.ComponentIdentity, error) {
	f.called = true
	f.token = token
	f.required = required
	if f.err != nil {
		return nil, f.err
	}
	return f.identity, nil
}

func TestAuthInterceptorsUnaryMissingToken(t *testing.T) {
	validator := &fakeComponentTokenValidator{}
	interceptor := ComponentAuthUnaryInterceptor(validator, testMethodScopes(), testMethodRoles())
	called := false

	resp, err := interceptor(context.Background(), nil, unaryInfo(), func(context.Context, any) (any, error) {
		called = true
		return "ok", nil
	})

	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, called)
	require.False(t, validator.called)
}

func TestAuthInterceptorsUnaryInvalidToken(t *testing.T) {
	validator := &fakeComponentTokenValidator{err: domain.ErrInvalidToken}
	interceptor := ComponentAuthUnaryInterceptor(validator, testMethodScopes(), testMethodRoles())

	resp, err := interceptor(ctxWithBearer("bad-token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	})

	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.True(t, validator.called)
	require.Equal(t, "bad-token", validator.token)
	require.Equal(t, domain.ComponentScopeRuntimeDeploy, validator.required)
}

func TestAuthInterceptorsUnaryValidTokenInjectsIdentity(t *testing.T) {
	identity := testIdentity()
	validator := &fakeComponentTokenValidator{identity: identity}
	interceptor := ComponentAuthUnaryInterceptor(validator, testMethodScopes(), testMethodRoles())
	called := false

	resp, err := interceptor(ctxWithBearer("good-token"), nil, unaryInfo(), func(ctx context.Context, _ any) (any, error) {
		called = true
		got, ok := ComponentIdentityFromContext(ctx)
		require.True(t, ok)
		require.Same(t, identity, got)
		return "ok", nil
	})

	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.True(t, called)
	require.True(t, validator.called)
	require.Equal(t, "good-token", validator.token)
	require.Equal(t, domain.ComponentScopeRuntimeDeploy, validator.required)
}

func TestAuthInterceptorsUnaryWrongScopePermissionDenied(t *testing.T) {
	validator := &fakeComponentTokenValidator{err: domain.ErrUnauthorized}
	interceptor := ComponentAuthUnaryInterceptor(validator, testMethodScopes(), testMethodRoles())
	called := false

	resp, err := interceptor(ctxWithBearer("limited-token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
		called = true
		return "ok", nil
	})

	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, called)
	require.True(t, validator.called)
}

func TestAuthInterceptorsUnaryRevokedExpiredAndPermissionLikeDenied(t *testing.T) {
	for _, err := range []error{
		domain.ErrRevokedToken,
		domain.ErrExpiredToken,
		domain.ErrInsufficientScope,
		domain.ErrInvalidScope,
	} {
		t.Run(err.Error(), func(t *testing.T) {
			validator := &fakeComponentTokenValidator{err: err}
			interceptor := ComponentAuthUnaryInterceptor(validator, testMethodScopes(), testMethodRoles())

			resp, gotErr := interceptor(ctxWithBearer("token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
				t.Fatal("handler must not be called")
				return nil, nil
			})

			require.Nil(t, resp)
			require.Error(t, gotErr)
			require.Equal(t, codes.PermissionDenied, status.Code(gotErr))
		})
	}
}

func TestAuthInterceptorsPreservesValidatorStatusError(t *testing.T) {
	validatorErr := status.Error(codes.ResourceExhausted, "validator overloaded")
	validator := &fakeComponentTokenValidator{err: validatorErr}
	interceptor := ComponentAuthUnaryInterceptor(validator, testMethodScopes(), testMethodRoles())

	resp, err := interceptor(ctxWithBearer("token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	})

	require.Nil(t, resp)
	require.ErrorIs(t, err, validatorErr)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, "validator overloaded", status.Convert(err).Message())
}

func TestAuthInterceptorsMapsInfrastructureErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code codes.Code
	}{
		{name: "canceled", err: context.Canceled, code: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
		{name: "unknown", err: errors.New("database password leaked"), code: codes.Internal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := &fakeComponentTokenValidator{err: tt.err}
			interceptor := ComponentAuthUnaryInterceptor(validator, testMethodScopes(), testMethodRoles())

			resp, err := interceptor(ctxWithBearer("token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
				t.Fatal("handler must not be called")
				return nil, nil
			})

			require.Nil(t, resp)
			require.Equal(t, tt.code, status.Code(err))
			require.NotContains(t, status.Convert(err).Message(), "password")
			require.NotEqual(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

func TestAuthInterceptorsStreamValidTokenInjectsIdentity(t *testing.T) {
	identity := testIdentity()
	validator := &fakeComponentTokenValidator{identity: identity}
	interceptor := ComponentAuthStreamInterceptor(validator, testMethodScopes(), testMethodRoles())
	stream := &fakeServerStream{ctx: ctxWithBearer("stream-token")}
	called := false

	err := interceptor(nil, stream, streamInfo(), func(_ any, stream grpc.ServerStream) error {
		called = true
		got, ok := ComponentIdentityFromContext(stream.Context())
		require.True(t, ok)
		require.Same(t, identity, got)
		return nil
	})

	require.NoError(t, err)
	require.True(t, called)
	require.True(t, validator.called)
	require.Equal(t, "stream-token", validator.token)
	require.Equal(t, domain.ComponentScopeRuntimeDeploy, validator.required)
}

func TestAuthInterceptorsRejectWrongRoleForUnaryAndStream(t *testing.T) {
	validator := &fakeComponentTokenValidator{identity: &domain.ComponentIdentity{
		Role:   domain.ComponentRoleRuntime,
		Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy},
	}}

	unary := ComponentAuthUnaryInterceptor(validator, testMethodScopes(), testMethodRoles())
	resp, err := unary(ctxWithBearer("runtime-token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	})
	require.Nil(t, resp)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	stream := ComponentAuthStreamInterceptor(validator, testMethodScopes(), testMethodRoles())
	err = stream(nil, &fakeServerStream{ctx: ctxWithBearer("runtime-token")}, streamInfo(), func(any, grpc.ServerStream) error {
		t.Fatal("handler must not be called")
		return nil
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestAuthInterceptorsStreamMissingToken(t *testing.T) {
	validator := &fakeComponentTokenValidator{identity: testIdentity()}
	interceptor := ComponentAuthStreamInterceptor(validator, testMethodScopes(), testMethodRoles())
	called := false

	err := interceptor(nil, &fakeServerStream{ctx: context.Background()}, streamInfo(), func(any, grpc.ServerStream) error {
		called = true
		return nil
	})

	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, called)
	require.False(t, validator.called)
}

func TestAuthInterceptorsFailClosedWhenValidatorOrScopeMissing(t *testing.T) {
	interceptor := ComponentAuthUnaryInterceptor(nil, testMethodScopes(), testMethodRoles())
	resp, err := interceptor(ctxWithBearer("token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	})
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	validator := &fakeComponentTokenValidator{identity: testIdentity()}
	interceptor = ComponentAuthUnaryInterceptor(validator, map[string]domain.ComponentScope{}, testMethodRoles())
	resp, err = interceptor(ctxWithBearer("token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	})
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, validator.called)

	interceptor = ComponentAuthUnaryInterceptor(validator, testMethodScopes(), map[string]domain.ComponentRole{})
	resp, err = interceptor(ctxWithBearer("token"), nil, unaryInfo(), func(context.Context, any) (any, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	})
	require.Nil(t, resp)
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, validator.called)
}

func TestBearerTokenFromIncomingContextEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
		ok     bool
	}{
		{name: "malformed scheme", values: []string{"Basic token"}, ok: false},
		{name: "malformed bearer value", values: []string{"Bearer"}, ok: false},
		{name: "duplicate authorization accepts later valid", values: []string{"Bearer bad token", "Bearer good-token"}, want: "good-token", ok: true},
		{name: "case insensitive scheme with whitespace", values: []string{"  bEaReR \tgood-token  "}, want: "good-token", ok: true},
		{name: "embedded whitespace token rejected", values: []string{"Bearer bad token"}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := metadata.MD{"authorization": tt.values}
			got, ok := bearerTokenFromIncomingContext(metadata.NewIncomingContext(context.Background(), md))
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func testMethodRoles() map[string]domain.ComponentRole {
	return map[string]domain.ComponentRole{
		testAuthMethod: domain.ComponentRoleControl,
	}
}

func testMethodScopes() map[string]domain.ComponentScope {
	return map[string]domain.ComponentScope{
		testAuthMethod: domain.ComponentScopeRuntimeDeploy,
	}
}

func unaryInfo() *grpc.UnaryServerInfo {
	return &grpc.UnaryServerInfo{FullMethod: testAuthMethod}
}

func streamInfo() *grpc.StreamServerInfo {
	return &grpc.StreamServerInfo{FullMethod: testAuthMethod}
}

func ctxWithBearer(token string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func testIdentity() *domain.ComponentIdentity {
	return &domain.ComponentIdentity{
		KeyID:  "key-1",
		Name:   "runtime-1",
		Role:   domain.ComponentRoleControl,
		Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy},
	}
}

type fakeServerStream struct {
	ctx context.Context
}

func (s *fakeServerStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeServerStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeServerStream) SetTrailer(metadata.MD)       {}
func (s *fakeServerStream) Context() context.Context     { return s.ctx }
func (s *fakeServerStream) SendMsg(any) error            { return nil }
func (s *fakeServerStream) RecvMsg(any) error            { return nil }
