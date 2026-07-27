package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	runtimev1 "github.com/bnema/gordon/api/gordon/runtime/v1"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestCreateRuntimeCommandClientFromEndpoint(t *testing.T) {
	client, err := createRuntimeCommandClient(t.Context(), RuntimeControlConfig{Endpoint: "runtime.example.com:443", Token: "token"})
	require.NoError(t, err)
	require.NotNil(t, client)
	closer, ok := client.(interface{ Close() error })
	require.True(t, ok, "created runtime clients must expose connection cleanup")
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	client, err = createRuntimeCommandClient(t.Context(), RuntimeControlConfig{})
	assert.NoError(t, err)
	assert.Nil(t, client)
}

func TestCreatedRuntimeCommandClientClosesWithApplicationContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	client, err := createRuntimeCommandClient(ctx, RuntimeControlConfig{Endpoint: "runtime.example.com:443", Token: "token"})
	require.NoError(t, err)
	owner, ok := client.(interface{ AddOwnedCloser(io.Closer) error })
	require.True(t, ok)
	closed := make(signalCloser)
	require.NoError(t, owner.AddOwnedCloser(closed))

	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("application cancellation did not close its runtime client resources")
	}
	require.NoError(t, client.(io.Closer).Close(), "explicit cleanup after cancellation must be idempotent")
}

type signalCloser chan struct{}

func (closer signalCloser) Close() error {
	close(closer)
	return nil
}

func TestCreateRuntimeCommandClientUsesPrivateUnixSocketWithRequiredToken(t *testing.T) {
	client, err := createRuntimeCommandClient(context.Background(), RuntimeControlConfig{
		Endpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock",
		Token:    "component-token",
	})
	require.NoError(t, err)
	require.NotNil(t, client)
	closer, ok := client.(interface{ Close() error })
	require.True(t, ok)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })
	_, err = createRuntimeCommandClient(context.Background(), RuntimeControlConfig{Endpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock"})
	require.Error(t, err, "Unix transport requires a component token")
}

func TestCreateRuntimeCommandClientRejectsUnixEndpointOutsideMigrationState(t *testing.T) {
	_, err := createRuntimeCommandClient(context.Background(), RuntimeControlConfig{
		Endpoint: "unix:///tmp/runtime-control.sock",
		Token:    "component-token",
	})
	require.Error(t, err, "only the generated migration Unix socket is allowed")
}

func TestPostHandoffRuntimeClientDialsOnlyValidatedHostMigrationSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "gordon-recovery-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	path := filepath.Join(root, "migration", "fixture", bootstrapRuntimeSocketName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	const token = "generated-recovery-token"
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if authorization := metadata.ValueFromIncomingContext(ctx, "authorization"); len(authorization) != 1 || authorization[0] != "Bearer "+token {
			return nil, status.Error(codes.Unauthenticated, "runtime authentication failed")
		}
		return handler(ctx, req)
	}))
	runtimev1.RegisterRuntimeServiceServer(server, recoveryRuntimeHealthServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	endpoint := "unix://" + path
	client, err := createPostHandoffRuntimeCommandClient(t.Context(), RuntimeControlConfig{Endpoint: endpoint, Token: token}, root)
	require.NoError(t, err)
	closer, ok := client.(interface{ Close() error })
	require.True(t, ok)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })
	health, ok := client.(out.RuntimeHealthClient)
	require.True(t, ok)
	require.NoError(t, health.PingRuntime(t.Context()), "the generated recovery token must authenticate over the host socket")

	_, err = createRuntimeCommandClient(t.Context(), RuntimeControlConfig{Endpoint: endpoint, Token: token})
	require.Error(t, err, "the generic runtime client must not accept host migration or engine sockets")
}

func TestPrivateBootstrapRuntimeClientWaitsForConnectableSocketBeforeConstruction(t *testing.T) {
	path := newStaleValidatedRuntimeSocketPath(t)
	var listener net.Listener
	var server *grpc.Server
	retryCalls := 0
	retry := func(context.Context) error {
		retryCalls++
		require.NoError(t, os.Remove(path))
		var err error
		listener, err = net.Listen("unix", path)
		require.NoError(t, err)
		server = newAuthenticatedBootstrapRuntimeServer("bootstrap-token")
		go func() { _ = server.Serve(listener) }()
		return nil
	}

	client, err := createPrivateBootstrapRuntimeCommandClientWithRetry(t.Context(), RuntimeControlConfig{Token: "bootstrap-token"}, "passthrough:///runtime-bootstrap", func(ctx context.Context) (net.Conn, error) {
		return dialValidatedRuntimeSocket(ctx, path)
	}, retry)
	require.NoError(t, err)
	require.Equal(t, 1, retryCalls, "the missing endpoint must be observed before client construction")
	closer := client.(io.Closer)
	t.Cleanup(func() {
		require.NoError(t, closer.Close())
		server.Stop()
		_ = listener.Close()
	})

	probe, ok := client.(out.RuntimeEnvironmentProbe)
	require.True(t, ok)
	result, err := probe.ProbeRuntimeEnvironment(t.Context())
	require.NoError(t, err)
	assert.True(t, result.APIReachable)
	assert.True(t, result.Rootless)
}

func TestPrivateBootstrapRuntimeClientDoesNotRetryTransportAfterReadinessBarrier(t *testing.T) {
	var dialCalls atomic.Int32
	var readinessPeer net.Conn
	dial := func(context.Context) (net.Conn, error) {
		if dialCalls.Add(1) == 1 {
			server, client := net.Pipe()
			readinessPeer = server
			return client, nil
		}
		return nil, errPrivateRuntimeTransportUnavailable
	}

	client, err := createPrivateBootstrapRuntimeCommandClientWithRetry(t.Context(), RuntimeControlConfig{Token: "bootstrap-token"}, "passthrough:///runtime-bootstrap", dial, func(context.Context) error {
		t.Fatal("the connectable endpoint must satisfy the application readiness barrier")
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, readinessPeer.Close())
	closer := client.(io.Closer)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	_, err = client.(out.RuntimeEnvironmentProbe).ProbeRuntimeEnvironment(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Less(t, time.Since(started), 500*time.Millisecond, "gRPC calls must not own a second transport-readiness retry loop")
}

func TestPrivateBootstrapRuntimeClientRejectsMissingTokenBeforeTransportWait(t *testing.T) {
	_, err := createPrivateBootstrapRuntimeCommandClientWithRetry(t.Context(), RuntimeControlConfig{}, "passthrough:///runtime-bootstrap", func(context.Context) (net.Conn, error) {
		t.Fatal("invalid credentials must fail before transport readiness")
		return nil, nil
	}, func(context.Context) error {
		t.Fatal("invalid credentials must not enter readiness retries")
		return nil
	})
	require.EqualError(t, err, "private runtime authentication token is required")
}

func TestPrivateBootstrapRuntimeClientResolvesTokenBeforeTransportWait(t *testing.T) {
	path := newStaleValidatedRuntimeSocketPath(t)
	const (
		tokenEnv = "GORDON_TEST_RUNTIME_TOKEN"
		token    = "resolved-bootstrap-token"
	)
	t.Setenv(tokenEnv, token)
	var listener net.Listener
	var server *grpc.Server
	retry := func(context.Context) error {
		t.Setenv(tokenEnv, "")
		require.NoError(t, os.Remove(path))
		var err error
		listener, err = net.Listen("unix", path)
		require.NoError(t, err)
		server = newAuthenticatedBootstrapRuntimeServer(token)
		go func() { _ = server.Serve(listener) }()
		return nil
	}

	client, err := createPrivateBootstrapRuntimeCommandClientWithRetry(t.Context(), RuntimeControlConfig{TokenEnv: tokenEnv}, "passthrough:///runtime-bootstrap", func(ctx context.Context) (net.Conn, error) {
		return dialValidatedRuntimeSocket(ctx, path)
	}, retry)
	require.NoError(t, err, "client construction must retain the token resolved before transport readiness")
	closer := client.(io.Closer)
	t.Cleanup(func() {
		require.NoError(t, closer.Close())
		server.Stop()
		_ = listener.Close()
	})

	result, err := client.(out.RuntimeEnvironmentProbe).ProbeRuntimeEnvironment(t.Context())
	require.NoError(t, err, "the retained token must authenticate protocol calls")
	assert.True(t, result.APIReachable)
}

func TestWaitForPrivateRuntimeTransportClosesReadinessProbe(t *testing.T) {
	server, client := net.Pipe()
	closed := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1)
		_, err := server.Read(buffer)
		closed <- err
	}()
	t.Cleanup(func() { require.NoError(t, server.Close()) })

	err := waitForPrivateRuntimeTransport(t.Context(), func(context.Context) (net.Conn, error) {
		return client, nil
	}, func(context.Context) error {
		t.Fatal("a successful readiness probe must not retry")
		return nil
	})
	require.NoError(t, err)
	require.ErrorIs(t, <-closed, io.EOF)
}

func TestWaitForPrivateRuntimeTransportMissingUntilDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	dialCalls := 0
	err := waitForPrivateRuntimeTransport(ctx, func(context.Context) (net.Conn, error) {
		dialCalls++
		return nil, errPrivateRuntimeTransportUnavailable
	}, func(context.Context) error {
		t.Fatal("an expired deadline must stop before retry")
		return nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, dialCalls)
}

func TestWaitForPrivateRuntimeTransportStopsPromptlyWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	waitStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- waitForPrivateRuntimeTransport(ctx, func(context.Context) (net.Conn, error) {
			return nil, errPrivateRuntimeTransportUnavailable
		}, func(waitCtx context.Context) error {
			close(waitStarted)
			<-waitCtx.Done()
			return waitCtx.Err()
		})
	}()
	<-waitStarted
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestDialValidatedRuntimeSocketReportsValueFreeValidationCategories(t *testing.T) {
	tests := []struct {
		name         string
		category     string
		prepare      func(*testing.T) string
		connectError error
		code         codes.Code
	}{
		{
			name:     "relative shape",
			category: "invalid_shape",
			prepare: func(*testing.T) string {
				return filepath.Join("private-fixture", bootstrapRuntimeSocketName)
			},
			code: codes.PermissionDenied,
		},
		{
			name:     "non-canonical shape",
			category: "invalid_shape",
			prepare: func(t *testing.T) string {
				return t.TempDir() + string(filepath.Separator) + "nested" + string(filepath.Separator) + ".." + string(filepath.Separator) + bootstrapRuntimeSocketName
			},
			code: codes.PermissionDenied,
		},
		{
			name:     "invalid basename",
			category: "invalid_shape",
			prepare: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "wrong.sock")
			},
			code: codes.PermissionDenied,
		},
		{
			name:     "symlink ancestor",
			category: "symlink_ancestor",
			prepare: func(t *testing.T) string {
				root := t.TempDir()
				ancestor := filepath.Join(root, "private-ancestor")
				realDirectory := t.TempDir()
				require.NoError(t, os.Symlink(realDirectory, ancestor))
				return filepath.Join(ancestor, bootstrapRuntimeSocketName)
			},
			code: codes.PermissionDenied,
		},
		{
			name:     "missing node inspection",
			category: "inspection_failure",
			prepare: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), bootstrapRuntimeSocketName)
			},
			code: codes.PermissionDenied,
		},
		{
			name:     "symlink node",
			category: "invalid_node",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), bootstrapRuntimeSocketName)
				require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "outside.sock"), path))
				return path
			},
			code: codes.PermissionDenied,
		},
		{
			name:     "non-socket node",
			category: "invalid_node",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), bootstrapRuntimeSocketName)
				require.NoError(t, os.WriteFile(path, []byte("not a socket"), 0o600))
				return path
			},
			code: codes.PermissionDenied,
		},
		{
			name:         "connect permission",
			category:     "connect_permission",
			prepare:      newValidatedRuntimeSocketPath,
			connectError: os.ErrPermission,
			code:         codes.PermissionDenied,
		},
		{
			name:         "connect unavailable",
			category:     "connect_unavailable",
			prepare:      newValidatedRuntimeSocketPath,
			connectError: syscall.ECONNREFUSED,
			code:         codes.Unknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.prepare(t)
			dialCalls := 0
			_, err := dialValidatedRuntimeSocketWithDialer(t.Context(), path, func(context.Context, string, string) (net.Conn, error) {
				dialCalls++
				if tc.category == "connect_permission" {
					return nil, fmt.Errorf("endpoint=%s uid=12345 mode=0777 value=private-value: %w", path, tc.connectError)
				}
				return nil, tc.connectError
			})
			require.Error(t, err)
			assert.Equal(t, tc.code, status.Code(err))
			assert.Contains(t, err.Error(), "category="+tc.category)
			assert.NotContains(t, err.Error(), path)
			assert.NotContains(t, err.Error(), "uid=12345")
			assert.NotContains(t, err.Error(), "mode=0777")
			assert.NotContains(t, err.Error(), "value=private-value")
			if tc.connectError == nil {
				assert.Zero(t, dialCalls)
			} else {
				assert.Equal(t, 1, dialCalls)
			}
		})
	}
}

func TestValidatePrivateRuntimeSocketPathReportsValueFreeInspectionFailures(t *testing.T) {
	rootInfo, err := os.Lstat(string(filepath.Separator))
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		path   string
		failAt string
	}{
		{
			name:   "ancestor permission failure",
			path:   filepath.Join(string(filepath.Separator), "private-ancestor", bootstrapRuntimeSocketName),
			failAt: filepath.Join(string(filepath.Separator), "private-ancestor"),
		},
		{
			name:   "node permission failure",
			path:   filepath.Join(string(filepath.Separator), bootstrapRuntimeSocketName),
			failAt: filepath.Join(string(filepath.Separator), bootstrapRuntimeSocketName),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePrivateRuntimeSocketPath(tc.path, func(path string) (os.FileInfo, error) {
				if path == tc.failAt {
					return nil, os.ErrPermission
				}
				return rootInfo, nil
			})
			require.Error(t, err)
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
			assert.Contains(t, err.Error(), "category=inspection_failure")
			assert.NotContains(t, err.Error(), tc.path)
		})
	}
}

func TestPrivateBootstrapRuntimeClientReportsBoundedConnectUnavailable(t *testing.T) {
	path := newValidatedRuntimeSocketPath(t)
	_, err := createPrivateBootstrapRuntimeCommandClientWithRetry(t.Context(), RuntimeControlConfig{Token: "bootstrap-token"}, "passthrough:///runtime-bootstrap", func(ctx context.Context) (net.Conn, error) {
		return dialValidatedRuntimeSocketWithDialer(ctx, path, func(context.Context, string, string) (net.Conn, error) {
			return nil, wrappedUnixConnectError(syscall.ECONNREFUSED)
		})
	}, func(context.Context) error {
		return context.DeadlineExceeded
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "wait for private runtime transport")
	assert.Contains(t, err.Error(), "category=connect_unavailable")
	assert.NotContains(t, err.Error(), path)
}

func TestWaitForPrivateRuntimeTransportRejectsInvalidTransportsWithoutRetry(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name    string
		path    string
		prepare func(string) error
	}{
		{name: "invalid path", path: filepath.Join(root, "wrong.sock")},
		{name: "missing socket", path: filepath.Join(root, "missing", bootstrapRuntimeSocketName), prepare: func(path string) error {
			return os.MkdirAll(filepath.Dir(path), 0o700)
		}},
		{name: "regular file", path: filepath.Join(root, "regular", bootstrapRuntimeSocketName), prepare: func(path string) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			return os.WriteFile(path, []byte("not a socket"), 0o600)
		}},
		{name: "symlink", path: filepath.Join(root, "symlink", bootstrapRuntimeSocketName), prepare: func(path string) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(root, "outside.sock"), path)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				require.NoError(t, tc.prepare(tc.path))
			}
			err := waitForPrivateRuntimeTransport(t.Context(), func(ctx context.Context) (net.Conn, error) {
				return dialValidatedRuntimeSocket(ctx, tc.path)
			}, func(context.Context) error {
				t.Fatal("invalid transport must fail closed without retry")
				return nil
			})
			require.Error(t, err)
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

func TestWaitForPrivateRuntimeTransportReportsValueFreeTerminalCategories(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dialErr  error
		category string
	}{
		{
			name:     "permission failure",
			dialErr:  fmt.Errorf("endpoint=private uid=12345 mode=0777 value=secret: %w", os.ErrPermission),
			category: "connect_permission",
		},
		{
			name:     "unvalidated failure",
			dialErr:  fmt.Errorf("endpoint=private uid=12345 mode=0777 value=secret: %w", syscall.EIO),
			category: "unvalidated_failure",
		},
		{
			name:     "unclassified permission status",
			dialErr:  status.Error(codes.PermissionDenied, "endpoint=private uid=12345 mode=0777 value=secret"),
			category: "unvalidated_failure",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := waitForPrivateRuntimeTransport(t.Context(), func(context.Context) (net.Conn, error) {
				return nil, tc.dialErr
			}, func(context.Context) error {
				t.Fatal("terminal failure must fail closed without retry")
				return nil
			})
			require.Error(t, err)
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
			assert.Contains(t, err.Error(), "category="+tc.category)
			assert.NotContains(t, err.Error(), "endpoint=private")
			assert.NotContains(t, err.Error(), "uid=12345")
			assert.NotContains(t, err.Error(), "mode=0777")
			assert.NotContains(t, err.Error(), "value=secret")
		})
	}
}

func TestWaitForPrivateRuntimeTransportDoesNotRetryUnvalidatedConnectErrors(t *testing.T) {
	for _, connectErr := range []error{syscall.ENOENT, syscall.ECONNREFUSED, syscall.EAGAIN} {
		t.Run(connectErr.Error(), func(t *testing.T) {
			err := waitForPrivateRuntimeTransport(t.Context(), func(context.Context) (net.Conn, error) {
				return nil, wrappedUnixConnectError(connectErr)
			}, func(context.Context) error {
				t.Fatal("a connect error must not retry without validated-socket classification")
				return nil
			})
			require.Error(t, err)
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

func TestWaitForPrivateRuntimeTransportRetriesValidatedSocketConnectFailures(t *testing.T) {
	path := newValidatedRuntimeSocketPath(t)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "again", err: syscall.EAGAIN},
		{name: "interrupted", err: syscall.EINTR},
		{name: "invalid argument", err: syscall.EINVAL},
		{name: "no such device or address", err: syscall.ENXIO},
		{name: "connection reset", err: syscall.ECONNRESET},
		{name: "connection refused", err: syscall.ECONNREFUSED},
		{name: "socket disappeared", err: syscall.ENOENT},
		{name: "unknown non-permission failure", err: syscall.EIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialCalls := 0
			retryCalls := 0
			var peer net.Conn
			err := waitForPrivateRuntimeTransport(t.Context(), func(ctx context.Context) (net.Conn, error) {
				return dialValidatedRuntimeSocketWithDialer(ctx, path, func(context.Context, string, string) (net.Conn, error) {
					dialCalls++
					if dialCalls == 1 {
						return nil, wrappedUnixConnectError(tc.err)
					}
					server, client := net.Pipe()
					peer = server
					return client, nil
				})
			}, func(context.Context) error {
				retryCalls++
				return nil
			})
			require.NoError(t, err)
			require.NoError(t, peer.Close())
			assert.Equal(t, 2, dialCalls)
			assert.Equal(t, 1, retryCalls)
		})
	}
}

func TestWaitForPrivateRuntimeTransportRejectsValidatedSocketSecurityErrorsImmediately(t *testing.T) {
	path := newValidatedRuntimeSocketPath(t)
	for _, permissionErr := range []error{syscall.EACCES, syscall.EPERM} {
		t.Run(permissionErr.Error(), func(t *testing.T) {
			dialCalls := 0
			started := time.Now()
			err := waitForPrivateRuntimeTransport(t.Context(), func(ctx context.Context) (net.Conn, error) {
				return dialValidatedRuntimeSocketWithDialer(ctx, path, func(context.Context, string, string) (net.Conn, error) {
					dialCalls++
					return nil, wrappedUnixConnectError(permissionErr)
				})
			}, func(context.Context) error {
				t.Fatal("permission failures from a validated socket must not retry")
				return nil
			})
			require.Error(t, err)
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
			assert.Equal(t, 1, dialCalls)
			assert.Less(t, time.Since(started), 250*time.Millisecond)
		})
	}
}

func TestWaitForPrivateRuntimeTransportRejectsWrappedConnectContextErrorsImmediately(t *testing.T) {
	path := newValidatedRuntimeSocketPath(t)
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextErr.Error(), func(t *testing.T) {
			err := waitForPrivateRuntimeTransport(t.Context(), func(ctx context.Context) (net.Conn, error) {
				return dialValidatedRuntimeSocketWithDialer(ctx, path, func(context.Context, string, string) (net.Conn, error) {
					return nil, wrappedUnixConnectError(contextErr)
				})
			}, func(context.Context) error {
				t.Fatal("connect context failures must not retry")
				return nil
			})
			require.ErrorIs(t, err, contextErr)
		})
	}
}

func TestWaitForPrivateRuntimeTransportValidatedConnectFailureStopsAtDeadline(t *testing.T) {
	path := newValidatedRuntimeSocketPath(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := waitForPrivateRuntimeTransport(ctx, func(ctx context.Context) (net.Conn, error) {
		return dialValidatedRuntimeSocketWithDialer(ctx, path, func(context.Context, string, string) (net.Conn, error) {
			return nil, wrappedUnixConnectError(syscall.EAGAIN)
		})
	}, waitRuntimeHandoffRetry)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), 250*time.Millisecond, "the shared startup deadline must interrupt retry sleep")
}

func newValidatedRuntimeSocketPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), bootstrapRuntimeSocketName)
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	return path
}

func newStaleValidatedRuntimeSocketPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), bootstrapRuntimeSocketName)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	listener.SetUnlinkOnClose(false)
	require.NoError(t, listener.Close())
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSocket)
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove stale Unix socket: %v", err)
		}
	})
	return path
}

func wrappedUnixConnectError(err error) error {
	return fmt.Errorf("outer connect wrapper: %w", &net.OpError{
		Op:  "dial",
		Net: "unix",
		Err: fmt.Errorf("syscall connect: %w", err),
	})
}

func TestPrivateNonBootstrapRuntimeClientRemainsFailFast(t *testing.T) {
	path := newStaleValidatedRuntimeSocketPath(t)
	client, err := createPrivateRuntimeCommandClient(RuntimeControlConfig{Token: "runtime-token"}, "passthrough:///runtime", func(ctx context.Context) (net.Conn, error) {
		return dialValidatedRuntimeSocket(ctx, path)
	})
	require.NoError(t, err)
	closer := client.(interface{ Close() error })
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	_, err = client.(out.RuntimeEnvironmentProbe).ProbeRuntimeEnvironment(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Less(t, time.Since(started), 500*time.Millisecond, "non-bootstrap clients must retain fail-fast calls")
}

func TestPostHandoffRuntimeClientRejectsMissingRegularAndSymlinkSockets(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "gordon-recovery-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	path := filepath.Join(root, "migration", "fixture", bootstrapRuntimeSocketName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	endpoint := "unix://" + path

	for _, tc := range []struct {
		name    string
		prepare func() error
	}{
		{name: "missing"},
		{name: "regular file", prepare: func() error { return os.WriteFile(path, []byte("not a socket"), 0o600) }},
		{name: "symlink", prepare: func() error {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return os.Symlink(filepath.Join(root, "outside.sock"), path)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				require.NoError(t, tc.prepare())
			}
			_, err := createPostHandoffRuntimeCommandClient(t.Context(), RuntimeControlConfig{Endpoint: endpoint, Token: "generated-recovery-token"}, root)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "generated-recovery-token")
			if tc.prepare != nil {
				require.NoError(t, os.Remove(path))
			}
		})
	}
}

func newAuthenticatedBootstrapRuntimeServer(token string) *grpc.Server {
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if authorization := metadata.ValueFromIncomingContext(ctx, "authorization"); len(authorization) != 1 || authorization[0] != "Bearer "+token {
			return nil, status.Error(codes.Unauthenticated, "runtime authentication failed")
		}
		return handler(ctx, req)
	}))
	runtimev1.RegisterRuntimeServiceServer(server, bootstrapRuntimeProbeServer{})
	return server
}

type bootstrapRuntimeProbeServer struct {
	runtimev1.UnimplementedRuntimeServiceServer
}

func (bootstrapRuntimeProbeServer) ProbeEnvironment(context.Context, *runtimev1.ProbeEnvironmentRequest) (*runtimev1.ProbeEnvironmentResponse, error) {
	return &runtimev1.ProbeEnvironmentResponse{Engine: "podman", Rootless: true, ApiReachable: true}, nil
}

type recoveryRuntimeHealthServer struct {
	runtimev1.UnimplementedRuntimeServiceServer
}

func (recoveryRuntimeHealthServer) GetHealth(context.Context, *runtimev1.GetHealthRequest) (*runtimev1.GetHealthResponse, error) {
	return &runtimev1.GetHealthResponse{Message: "healthy"}, nil
}

func TestRuntimeComponentLauncherOwnsOnlySuccessfulHandoffClients(t *testing.T) {
	oldRuntime := &ownedHandoffRuntime{}
	component := ComponentLaunchComponent{
		Role:        domain.ComponentRoleRuntime,
		ComponentID: "gordon-runtime-fixture-g1",
		Labels: map[string]string{
			domain.LabelComponentVersion:     "v2",
			domain.LabelComponentGeneration:  "1",
			domain.LabelComponentMigrationID: "fixture",
		},
	}
	validState := domain.RuntimeActualStateSnapshot{
		SourceComponentID: component.ComponentID,
		Containers: []domain.RuntimeContainerState{{
			Name:   component.ComponentID,
			Status: domain.ContainerStatusRunning,
			Labels: map[string]string{
				domain.LabelComponent:           "true",
				domain.LabelComponentRole:       string(domain.ComponentRoleRuntime),
				domain.LabelComponentGeneration: "1",
			},
		}},
	}
	first := &ownedHandoffRuntime{handoffRuntime: handoffRuntime{probe: out.RuntimeEnvironment{APIReachable: true, Rootless: true}, states: []domain.RuntimeActualStateSnapshot{validState}}}
	second := &ownedHandoffRuntime{handoffRuntime: handoffRuntime{probe: out.RuntimeEnvironment{APIReachable: true, Rootless: true}, states: []domain.RuntimeActualStateSnapshot{validState}}}
	targets := []*ownedHandoffRuntime{first, second}
	launcher, err := NewRuntimeComponentLauncherWithHandoff(oldRuntime, func(context.Context, ComponentLaunchComponent) (RuntimeHandoffClient, error) {
		target := targets[0]
		targets = targets[1:]
		return target, nil
	})
	require.NoError(t, err)

	require.NoError(t, launcher.TransferRuntimeCommandChannel(t.Context(), component))
	assert.Zero(t, oldRuntime.closeCalls, "the launcher must not close an injected initial authority")
	assert.Zero(t, first.closeCalls, "the active handoff connection must remain open")

	require.NoError(t, launcher.TransferRuntimeCommandChannel(t.Context(), component))
	assert.Equal(t, 1, first.closeCalls, "replacing an owned handoff connection must close it")
	assert.Zero(t, second.closeCalls)

	require.NoError(t, launcher.Close())
	require.NoError(t, launcher.Close(), "launcher cleanup must be idempotent")
	assert.Equal(t, 1, second.closeCalls, "launcher shutdown must close the active handoff connection")
	assert.Zero(t, oldRuntime.closeCalls, "launcher shutdown must not close an injected initial authority")
}

func TestRuntimeComponentLauncherClosesExplicitlyOwnedInitialClient(t *testing.T) {
	runtime := &ownedHandoffRuntime{}
	launcher, err := NewRuntimeComponentLauncherWithOwnedRuntime(runtime)
	require.NoError(t, err)

	require.NoError(t, launcher.Close())
	assert.Equal(t, 1, runtime.closeCalls)
}

type ownedHandoffRuntime struct {
	handoffRuntime
	closeCalls int
}

func (r *ownedHandoffRuntime) Close() error {
	r.closeCalls++
	return nil
}

func TestRuntimeControlConfigDefaultsAndMapsInsecureTransportOptIn(t *testing.T) {
	v := viper.New()
	require.NoError(t, loadConfig(v, ""))
	assert.False(t, v.GetBool("runtime.insecure"))

	v.Set("runtime.insecure", true)
	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	assert.True(t, cfg.Runtime.Insecure)
}

func TestRuntimeLogReaderUsesCommandClientForControlRole(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	reader := runtimeLogReaderForServices(&services{role: RoleControl, runtimeCommandClient: client})
	_, ok := reader.(fakeRuntimeCommandClientForApp)
	assert.True(t, ok)
}

func TestRuntimeLogReaderKeepsLocalReaderForMonolith(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	reader := runtimeLogReaderForServices(&services{role: RoleMonolith, runtimeCommandClient: client})
	_, ok := reader.(fakeRuntimeCommandClientForApp)
	assert.False(t, ok)
}

func TestRuntimeVolumeManagerUsesCommandClientForControlRole(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	manager := runtimeVolumeManagerForServices(&services{role: RoleControl, runtimeCommandClient: client})
	_, ok := manager.(fakeRuntimeCommandClientForApp)
	assert.True(t, ok)
}

func TestRuntimeVolumeManagerKeepsLocalManagerForMonolith(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	manager := runtimeVolumeManagerForServices(&services{role: RoleMonolith, runtimeCommandClient: client})
	_, ok := manager.(fakeRuntimeCommandClientForApp)
	assert.False(t, ok)
}

func TestRuntimeImageManagerUsesCommandClientForControlRole(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	manager := runtimeImageManagerForServices(&services{role: RoleControl, runtimeCommandClient: client})
	_, ok := manager.(fakeRuntimeCommandClientForApp)
	assert.True(t, ok)
}

func TestRuntimeImageManagerKeepsLocalManagerForMonolith(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	manager := runtimeImageManagerForServices(&services{role: RoleMonolith, runtimeCommandClient: client})
	_, ok := manager.(fakeRuntimeCommandClientForApp)
	assert.False(t, ok)
}

func TestRuntimeControlFacadeConstructedWhenCommandClientAvailableForControlRole(t *testing.T) {
	svc := &services{role: RoleControl, runtimeCommandClient: fakeRuntimeCommandClientForApp{}}

	initRuntimeControlFacade(svc)

	assert.NotNil(t, svc.runtimeControl)
}

func TestRuntimeControlFacadeNotConstructedWithoutCommandClient(t *testing.T) {
	svc := &services{role: RoleControl}

	initRuntimeControlFacade(svc)

	assert.Nil(t, svc.runtimeControl)
}

func TestRuntimeControlFacadeNotConstructedForMonolithRole(t *testing.T) {
	svc := &services{role: RoleMonolith, runtimeCommandClient: fakeRuntimeCommandClientForApp{}}

	initRuntimeControlFacade(svc)

	assert.Nil(t, svc.runtimeControl)
}

type fakeRuntimeCommandClientForApp struct{}

func (fakeRuntimeCommandClientForApp) DeployRoute(context.Context, domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) RestartRoute(context.Context, domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) RemoveRoute(context.Context, domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) Reconcile(context.Context, domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) ReadRouteLogs(context.Context, string, bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (fakeRuntimeCommandClientForApp) ListRuntimeVolumes(context.Context) ([]*domain.VolumeInfo, error) {
	return nil, nil
}

func (fakeRuntimeCommandClientForApp) RemoveRuntimeVolume(context.Context, string, bool) error {
	return nil
}

func (fakeRuntimeCommandClientForApp) ListRuntimeImages(context.Context) ([]domain.RuntimeImageDetail, error) {
	return nil, nil
}

func (fakeRuntimeCommandClientForApp) PruneRuntimeImages(context.Context, bool) (domain.RuntimePruneResult, error) {
	return domain.RuntimePruneResult{}, nil
}

func (fakeRuntimeCommandClientForApp) ApplyStandaloneService(context.Context, domain.ApplyStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) RemoveStandaloneService(context.Context, domain.RemoveStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) ListStandaloneServiceState(context.Context) ([]domain.RuntimeStandaloneServiceState, error) {
	return nil, nil
}
