package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

func TestPushJSONAndHumanOutput(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		t.Run(fmt.Sprint(jsonOut), func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/admin/auth/verify", r.URL.Path)
				_, _ = io.WriteString(w, `{"valid":true,"subject":"ci","registry_domain":"registry.example.com"}`)
			})
			defer srv.Close()
			configurePushRemoteInferenceTestEnv(t, "")
			remoteFlag = srv.URL
			tokenFlag = "token"
			old := newImageOpsFn
			t.Cleanup(func() { newImageOpsFn = old })
			ops := &recordingPushOps{exists: true}
			newImageOpsFn = func() (pushImageOps, error) { return ops, nil }
			cmd := newPushCmd()
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			args := []string{"myapp", "--tag", "v1"}
			if jsonOut {
				args = append(args, "--json")
			}
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			if jsonOut {
				var result map[string]string
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
				require.Equal(t, map[string]string{"repository": "registry.example.com/myapp", "tag": "v1", "digest": "sha256:" + strings.Repeat("a", 64), "image": "registry.example.com/myapp@sha256:" + strings.Repeat("a", 64)}, result)
				require.NotContains(t, stdout.String(), "\x1b")
				require.NotContains(t, stdout.String(), "Pushing")
				require.Contains(t, stderr.String(), "Pushing")
				require.NotContains(t, stderr.String(), "Also:")
			} else {
				require.Contains(t, stdout.String(), "Also:")
				require.Contains(t, stdout.String(), "registry.example.com/myapp:latest")
				require.Contains(t, stdout.String(), "Push complete")
				require.Empty(t, stderr.String())
			}
		})
	}
}

func TestPushAuthRetryReturnsRetryDigest(t *testing.T) {
	exchanges := 0
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/verify" {
			_, _ = io.WriteString(w, `{"valid":true,"subject":"ci"}`)
			return
		}
		require.Equal(t, "/auth/token", r.URL.Path)
		exchanges++
		_, _ = fmt.Fprintf(w, `{"token":"registry-%d"}`, exchanges)
	})
	defer srv.Close()
	ops := &dockerImageOps{remoteClient: remote.NewClient(srv.URL, remote.WithToken("token"))}
	calls := 0
	digest, err := ops.pushWithAuthRetry(context.Background(), func(auth string) (string, error) {
		calls++
		require.Equal(t, fmt.Sprintf("Bearer registry-%d", calls), auth)
		if calls == 1 {
			return "", errors.New("manifest upload returned status 401")
		}
		return "sha256:" + strings.Repeat("b", 64), nil
	}, ops.exchangeRegistryAuth)
	require.NoError(t, err)
	require.Equal(t, "sha256:"+strings.Repeat("b", 64), digest)
	require.Equal(t, 2, calls)
	require.Equal(t, 2, exchanges)
}

func TestBuildAndPushReturnsRequestedTagDigest(t *testing.T) {
	dockerfile := filepath.Join(t.TempDir(), "Dockerfile")
	require.NoError(t, os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0600))
	for _, tag := range []string{"v1", "latest"} {
		ops := &recordingPushOps{}
		img := imagePush{Version: tag, VersionRef: "registry.example.com/app:" + tag, LatestRef: "registry.example.com/app:latest"}
		digest, err := buildAndPush(context.Background(), io.Discard, ops, buildConfig{Dockerfile: dockerfile}, img)
		require.NoError(t, err)
		if tag == "latest" {
			require.Equal(t, "sha256:"+strings.Repeat("b", 64), digest)
		} else {
			require.Equal(t, "sha256:"+strings.Repeat("a", 64), digest)
		}
		if tag == "latest" {
			require.Len(t, ops.pushes, 1)
		} else {
			require.Equal(t, []string{img.LatestRef, img.VersionRef}, ops.pushes)
		}
	}
}

func TestPushUploadFailureReturnsNoDigest(t *testing.T) {
	img := imagePush{SourceRef: "app", Version: "v1", VersionRef: "registry.example.com/app:v1", LatestRef: "registry.example.com/app:latest"}
	for _, failRef := range []string{img.VersionRef, img.LatestRef} {
		ops := &recordingPushOps{exists: true, failRef: failRef}
		digest, err := tagAndPush(context.Background(), io.Discard, ops, img)
		require.ErrorContains(t, err, "upload failed")
		require.Empty(t, digest)
	}
}

func TestPushBuildOutputUsesStderr(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\necho build-stdout\necho build-stderr >&2\n"), 0700))
	t.Setenv("PATH", dir)
	var stderr bytes.Buffer
	ops := &dockerImageOps{}
	configurePushOutput(ops, &stderr, true)
	require.NoError(t, ops.Build(context.Background(), []string{"buildx", "build"}))
	require.Contains(t, stderr.String(), "build-stdout")
	require.Contains(t, stderr.String(), "build-stderr")
	require.Same(t, &stderr, ops.progress)
}
