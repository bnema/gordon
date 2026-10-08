package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// remoteWithRegistry returns a remote handle whose /auth/verify reports registryDomain.
func remoteWithRegistry(t *testing.T, registryDomain string) *controlPlaneHandle {
	t.Helper()
	plane := climocks.NewMockControlPlane(t)
	plane.EXPECT().VerifyAuth(mock.Anything).Return(&dto.AuthVerifyResponse{Valid: true, RegistryDomain: registryDomain}, nil).Once()
	return &controlPlaneHandle{plane: plane, isRemote: true}
}

func TestResolveImageTarget_BareNameUsesTargetRegistry(t *testing.T) {
	tests := []struct {
		name       string
		arg        string
		wantName   string
		wantSource string
	}{
		{"bare name", "myapp", "myapp", "myapp"},
		{"bare name with tag", "myapp:v1.2.3", "myapp", "myapp:v1.2.3"},
		{"namespaced name", "team/myapp:v1", "team/myapp", "team/myapp:v1"},
		{"bare digest", "myapp@sha256:deadbeef", "myapp", "myapp@sha256:deadbeef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			registry, name, source, err := resolveImageTarget(context.Background(), &out, remoteWithRegistry(t, "reg.example.com"), tt.arg, "Dockerfile")
			require.NoError(t, err)
			assert.Equal(t, "reg.example.com", registry)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantSource, source)
			assert.Contains(t, out.String(), "reg.example.com")
		})
	}
}

func TestResolveImageTarget_QualifiedRefMatchingTarget(t *testing.T) {
	registry, name, source, err := resolveImageTarget(context.Background(), io.Discard, remoteWithRegistry(t, "reg.example.com."), "Reg.Example.com/myapp:v1", "Dockerfile")
	require.NoError(t, err)
	assert.Equal(t, "Reg.Example.com", registry)
	assert.Equal(t, "myapp", name)
	assert.Equal(t, "Reg.Example.com/myapp:v1", source)
}

func TestResolveImageTarget_RejectsMismatchedRegistry(t *testing.T) {
	for _, ref := range []string{"other.example.org/myapp:v1", "reg.example.com:443/myapp:v1"} {
		t.Run(ref, func(t *testing.T) {
			_, _, _, err := resolveImageTarget(context.Background(), io.Discard, remoteWithRegistry(t, "reg.example.com"), ref, "Dockerfile")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "reg.example.com/myapp")
		})
	}
}

func TestTargetRegistryDomain_RemoteFallsBackToStatusForOlderServers(t *testing.T) {
	plane := climocks.NewMockControlPlane(t)
	plane.EXPECT().VerifyAuth(mock.Anything).Return(&dto.AuthVerifyResponse{Valid: true}, nil).Once()
	plane.EXPECT().GetStatus(mock.Anything).Return(&remote.Status{RegistryDomain: "reg.example.com"}, nil).Once()

	got, err := targetRegistryDomain(context.Background(), &controlPlaneHandle{plane: plane, isRemote: true})
	require.NoError(t, err)
	assert.Equal(t, "reg.example.com", got)
}

func TestTargetRegistryDomain_LocalSocketUsesStatus(t *testing.T) {
	plane := climocks.NewMockControlPlane(t)
	plane.EXPECT().GetStatus(mock.Anything).Return(&remote.Status{RegistryDomain: "reg.example.com"}, nil).Once()

	got, err := targetRegistryDomain(context.Background(), &controlPlaneHandle{plane: plane})
	require.NoError(t, err)
	assert.Equal(t, "reg.example.com", got)
}

func TestTargetRegistryDomain_Errors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*climocks.MockControlPlane)
		wantErr string
	}{
		{
			name: "verify fails",
			setup: func(p *climocks.MockControlPlane) {
				p.EXPECT().VerifyAuth(mock.Anything).Return(nil, errors.New("boom")).Once()
			},
			wantErr: "boom",
		},
		{
			name: "token without admin scope",
			setup: func(p *climocks.MockControlPlane) {
				p.EXPECT().VerifyAuth(mock.Anything).Return(nil, &remote.HTTPError{StatusCode: http.StatusForbidden, Status: "403 Forbidden"}).Once()
			},
			wantErr: "admin scope",
		},
		{
			name: "token invalid",
			setup: func(p *climocks.MockControlPlane) {
				p.EXPECT().VerifyAuth(mock.Anything).Return(&dto.AuthVerifyResponse{Valid: false}, nil).Once()
			},
			wantErr: "token is not valid",
		},
		{
			name: "older server denies status",
			setup: func(p *climocks.MockControlPlane) {
				p.EXPECT().VerifyAuth(mock.Anything).Return(&dto.AuthVerifyResponse{Valid: true}, nil).Once()
				p.EXPECT().GetStatus(mock.Anything).Return(nil, &remote.HTTPError{StatusCode: http.StatusForbidden, Status: "403 Forbidden"}).Once()
			},
			wantErr: "admin:status:read",
		},
		{
			name: "no registry domain configured",
			setup: func(p *climocks.MockControlPlane) {
				p.EXPECT().VerifyAuth(mock.Anything).Return(&dto.AuthVerifyResponse{Valid: true}, nil).Once()
				p.EXPECT().GetStatus(mock.Anything).Return(&remote.Status{}, nil).Once()
			},
			wantErr: "registry_domain",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plane := climocks.NewMockControlPlane(t)
			tt.setup(plane)
			_, err := targetRegistryDomain(context.Background(), &controlPlaneHandle{plane: plane, isRemote: true})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
