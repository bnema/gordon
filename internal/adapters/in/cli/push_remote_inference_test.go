package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferPushRemote_UsesUniqueMatchingSavedRemote(t *testing.T) {
	prod := newTagsTestServer(t, func(image string) ([]string, int) {
		assert.Equal(t, "myapp", image)
		return []string{"latest"}, http.StatusOK
	})
	staging := newTagsTestServer(t, func(image string) ([]string, int) {
		assert.Equal(t, "myapp", image)
		return nil, http.StatusOK
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"

[remotes.staging]
url = "`+staging.URL+`"
`)

	resolved, err := inferPushRemote(context.Background(), "myapp", "", "Dockerfile")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "prod", resolved.Name)
	assert.Equal(t, prod.URL, resolved.URL)
}

func TestInferPushRemote_ReturnsAmbiguousErrorWhenMultipleRemotesMatch(t *testing.T) {
	prod := newTagsTestServer(t, func(image string) ([]string, int) {
		return []string{"latest"}, http.StatusOK
	})
	staging := newTagsTestServer(t, func(image string) ([]string, int) {
		return []string{"latest"}, http.StatusOK
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"

[remotes.staging]
url = "`+staging.URL+`"
`)

	resolved, err := inferPushRemote(context.Background(), "myapp", "", "Dockerfile")
	require.Error(t, err)
	assert.Nil(t, resolved)
	assert.Contains(t, err.Error(), `multiple saved remotes match image "myapp"`)
	assert.Contains(t, err.Error(), "prod")
	assert.Contains(t, err.Error(), "staging")
}

func TestInferPushRemote_SkipsGuessWhenExplicitRemoteSelected(t *testing.T) {
	originalRemoteFlag := remoteFlag
	t.Cleanup(func() {
		remoteFlag = originalRemoteFlag
	})

	called := false
	prod := newTagsTestServer(t, func(image string) ([]string, int) {
		called = true
		return []string{"latest"}, http.StatusOK
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"
`)
	t.Setenv("GORDON_REMOTE", "")
	remoteFlag = "prod"

	resolved, err := inferPushRemote(context.Background(), "myapp", "", "Dockerfile")
	require.NoError(t, err)
	assert.Nil(t, resolved)
	assert.False(t, called)
}

func TestInferPushRemote_SkipsGuessWhenActiveRemoteConfigured(t *testing.T) {
	called := false
	prod := newTagsTestServer(t, func(image string) ([]string, int) {
		called = true
		return []string{"latest"}, http.StatusOK
	})

	configurePushRemoteInferenceTestEnv(t, `
active = "prod"

[remotes.prod]
url = "`+prod.URL+`"
`)

	resolved, err := inferPushRemote(context.Background(), "myapp", "", "Dockerfile")
	require.NoError(t, err)
	assert.Nil(t, resolved)
	assert.False(t, called)
}

func TestInferPushRemote_FailsSafeWhenSomeRemotesCannotBeProbed(t *testing.T) {
	prod := newTagsTestServer(t, func(image string) ([]string, int) {
		return []string{"latest"}, http.StatusOK
	})
	broken := newTagsTestServer(t, func(image string) ([]string, int) {
		return nil, http.StatusInternalServerError
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"

[remotes.broken]
url = "`+broken.URL+`"
`)

	resolved, err := inferPushRemote(context.Background(), "myapp", "", "Dockerfile")
	require.Error(t, err)
	assert.Nil(t, resolved)
	assert.Contains(t, err.Error(), `could not safely infer remote for image "myapp"`)
	assert.Contains(t, err.Error(), "broken")
}

func TestInferRemoteForRepository_ReturnsAmbiguousErrorWhenMultipleRemotesMatch(t *testing.T) {
	prod := newTagsTestServer(t, func(repository string) ([]string, int) {
		return []string{"latest", "v1.0.0"}, http.StatusOK
	})
	staging := newTagsTestServer(t, func(repository string) ([]string, int) {
		return []string{"latest"}, http.StatusOK
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"

[remotes.staging]
url = "`+staging.URL+`"
`)

	resolved, err := inferRemoteForRepository(context.Background(), "myapp")
	require.Error(t, err)
	assert.Nil(t, resolved)
	assert.Contains(t, err.Error(), `multiple saved remotes match repository "myapp"`)
}

func configurePushRemoteInferenceTestEnv(t *testing.T, remotesTOML string) {
	t.Helper()

	originalRemoteFlag := remoteFlag
	originalTokenFlag := tokenFlag
	originalInsecureTLSFlag := insecureTLSFlag
	t.Cleanup(func() {
		remoteFlag = originalRemoteFlag
		tokenFlag = originalTokenFlag
		insecureTLSFlag = originalInsecureTLSFlag
	})

	remoteFlag = ""
	tokenFlag = ""
	insecureTLSFlag = false
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	t.Setenv("GORDON_INSECURE", "")
	t.Setenv("HOME", t.TempDir())

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	cliConfigPath := filepath.Join(configHome, "gordon", "remotes.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(cliConfigPath), 0o755))
	require.NoError(t, os.WriteFile(cliConfigPath, []byte(strings.TrimSpace(remotesTOML)), 0o600))
}

func newTagsTestServer(t *testing.T, handler func(repository string) ([]string, int)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleListTagsProbe(t, w, r, handler)
	}))
	t.Cleanup(server.Close)
	return server
}

func handleListTagsProbe(t *testing.T, w http.ResponseWriter, r *http.Request, handler func(repository string) ([]string, int)) {
	t.Helper()
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	repository, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/tags/"))
	if !assert.NoError(t, err) {
		return
	}
	tags, status := handler(repository)
	if status >= http.StatusBadRequest {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusNotFound {
			if !assert.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "repository not found"})) {
				return
			}
			return
		}
		if !assert.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "boom"})) {
			return
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"repository": repository, "tags": tags})) {
		return
	}
}
