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

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferPushRemote_UsesUniqueMatchingSavedRemote(t *testing.T) {
	prod := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
		assert.Equal(t, "myapp", image)
		return []domain.Route{{Domain: "app.example.com", Image: "registry.example.com/myapp:latest"}}, http.StatusOK
	})
	staging := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
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
	prod := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
		return []domain.Route{{Domain: "app.example.com", Image: "registry.example.com/myapp:latest"}}, http.StatusOK
	})
	staging := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
		return []domain.Route{{Domain: "staging.example.com", Image: "registry.example.com/myapp:latest"}}, http.StatusOK
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
	prod := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
		called = true
		return []domain.Route{{Domain: "app.example.com", Image: "registry.example.com/myapp:latest"}}, http.StatusOK
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
	prod := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
		called = true
		return []domain.Route{{Domain: "app.example.com", Image: "registry.example.com/myapp:latest"}}, http.StatusOK
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

func TestInferPushRemote_IgnoresPreviewOnlyMatches(t *testing.T) {
	prod := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
		return []domain.Route{{Domain: "preview--pr-42.example.com", Image: "registry.example.com/myapp:latest"}}, http.StatusOK
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"
`)

	resolved, err := inferPushRemote(context.Background(), "myapp", "", "Dockerfile")
	require.NoError(t, err)
	assert.Nil(t, resolved)
}

func TestInferPushRemote_FailsSafeWhenSomeRemotesCannotBeProbed(t *testing.T) {
	prod := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
		return []domain.Route{{Domain: "app.example.com", Image: "registry.example.com/myapp:latest"}}, http.StatusOK
	})
	broken := newFindRoutesByImageTestServer(t, func(image string) ([]domain.Route, int) {
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

func TestInferRemoteForRouteDomain_UsesUniqueMatchingSavedRemote(t *testing.T) {
	prod := newGetRouteTestServer(t, func(domainName string) (*domain.Route, int) {
		return &domain.Route{Domain: domainName, Image: "registry.example.com/myapp:latest"}, http.StatusOK
	})
	staging := newGetRouteTestServer(t, func(domainName string) (*domain.Route, int) {
		return nil, http.StatusNotFound
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"

[remotes.staging]
url = "`+staging.URL+`"
`)

	resolved, err := inferRemoteForRouteDomain(context.Background(), "app.example.com")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "prod", resolved.Name)
}

func TestInferRemoteForRouteDomain_ReturnsAmbiguousErrorWhenMultipleRemotesMatch(t *testing.T) {
	prod := newGetRouteTestServer(t, func(domainName string) (*domain.Route, int) {
		return &domain.Route{Domain: domainName, Image: "registry.example.com/myapp:latest"}, http.StatusOK
	})
	staging := newGetRouteTestServer(t, func(domainName string) (*domain.Route, int) {
		return &domain.Route{Domain: domainName, Image: "registry.example.com/myapp:latest"}, http.StatusOK
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"

[remotes.staging]
url = "`+staging.URL+`"
`)

	resolved, err := inferRemoteForRouteDomain(context.Background(), "app.example.com")
	require.Error(t, err)
	assert.Nil(t, resolved)
	assert.Contains(t, err.Error(), `multiple saved remotes match route "app.example.com"`)
}

func TestInferRemoteForAttachmentImage_UsesUniqueMatchingSavedRemote(t *testing.T) {
	prod := newAttachmentTargetsByImageTestServer(t, func(image string) ([]string, int) {
		return []string{"app.example.com"}, http.StatusOK
	})
	staging := newAttachmentTargetsByImageTestServer(t, func(image string) ([]string, int) {
		return nil, http.StatusOK
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"

[remotes.staging]
url = "`+staging.URL+`"
`)

	resolved, err := inferRemoteForAttachmentImage(context.Background(), "postgres:18")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "prod", resolved.Name)
}

func TestInferRemoteForAttachmentTarget_FallsBackToRouteLookup(t *testing.T) {
	prod := newMultiAdminProbeTestServer(t, multiAdminProbeHandlers{
		getRoute: func(domainName string) (*domain.Route, int) {
			return &domain.Route{Domain: domainName, Image: "registry.example.com/myapp:latest"}, http.StatusOK
		},
		getAttachmentsConfig: func(target string) ([]string, int) {
			return nil, http.StatusNotFound
		},
	})
	staging := newMultiAdminProbeTestServer(t, multiAdminProbeHandlers{
		getRoute: func(domainName string) (*domain.Route, int) {
			return nil, http.StatusNotFound
		},
		getAttachmentsConfig: func(target string) ([]string, int) {
			return nil, http.StatusNotFound
		},
	})

	configurePushRemoteInferenceTestEnv(t, `
[remotes.prod]
url = "`+prod.URL+`"

[remotes.staging]
url = "`+staging.URL+`"
`)

	resolved, err := inferRemoteForAttachmentTarget(context.Background(), "app.example.com")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "prod", resolved.Name)
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
	configPath := filepath.Join(configHome, "gordon", "remotes.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(strings.TrimSpace(remotesTOML)), 0o600))
}

func newFindRoutesByImageTestServer(t *testing.T, handler func(image string) ([]domain.Route, int)) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin/routes/by-image/") {
			http.NotFound(w, r)
			return
		}

		image, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/routes/by-image/"))
		require.NoError(t, err)

		routes, status := handler(image)
		if status >= http.StatusBadRequest {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "boom"}))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"image":  image,
			"routes": routes,
		}))
	}))
	t.Cleanup(server.Close)
	return server
}

func newGetRouteTestServer(t *testing.T, handler func(domainName string) (*domain.Route, int)) *httptest.Server {
	t.Helper()
	return newMultiAdminProbeTestServer(t, multiAdminProbeHandlers{getRoute: handler})
}

type multiAdminProbeHandlers struct {
	getRoute             func(domainName string) (*domain.Route, int)
	getAttachmentsConfig func(target string) ([]string, int)
	listTags             func(repository string) ([]string, int)
	findAttachmentTarget func(image string) ([]string, int)
}

func newMultiAdminProbeTestServer(t *testing.T, handlers multiAdminProbeHandlers) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/admin/routes/"):
			handleGetRouteProbe(t, w, r, handlers.getRoute)
		case strings.HasPrefix(r.URL.Path, "/admin/attachments/by-image/"):
			handleAttachmentTargetsByImageProbe(t, w, r, handlers.findAttachmentTarget)
		case strings.HasPrefix(r.URL.Path, "/admin/attachments/"):
			handleGetAttachmentsConfigProbe(t, w, r, handlers.getAttachmentsConfig)
		case strings.HasPrefix(r.URL.Path, "/admin/tags/"):
			handleListTagsProbe(t, w, r, handlers.listTags)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newAttachmentTargetsByImageTestServer(t *testing.T, handler func(image string) ([]string, int)) *httptest.Server {
	t.Helper()
	return newMultiAdminProbeTestServer(t, multiAdminProbeHandlers{findAttachmentTarget: handler})
}

func newTagsTestServer(t *testing.T, handler func(repository string) ([]string, int)) *httptest.Server {
	t.Helper()
	return newMultiAdminProbeTestServer(t, multiAdminProbeHandlers{listTags: handler})
}

func handleGetRouteProbe(t *testing.T, w http.ResponseWriter, r *http.Request, handler func(domainName string) (*domain.Route, int)) {
	t.Helper()
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	domainName, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/routes/"))
	require.NoError(t, err)
	route, status := handler(domainName)
	if status >= http.StatusBadRequest {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusNotFound {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": domain.ErrRouteNotFound.Error()}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "boom"}))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(route))
}

func handleAttachmentTargetsByImageProbe(t *testing.T, w http.ResponseWriter, r *http.Request, handler func(image string) ([]string, int)) {
	t.Helper()
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	image, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/attachments/by-image/"))
	require.NoError(t, err)
	targets, status := handler(image)
	if status >= http.StatusBadRequest {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "boom"}))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"image": image, "targets": targets}))
}

func handleGetAttachmentsConfigProbe(t *testing.T, w http.ResponseWriter, r *http.Request, handler func(target string) ([]string, int)) {
	t.Helper()
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	target, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/attachments/"))
	require.NoError(t, err)
	images, status := handler(target)
	if status >= http.StatusBadRequest {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusNotFound {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "no attachments found for target"}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "boom"}))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"target": target, "images": images}))
}

func handleListTagsProbe(t *testing.T, w http.ResponseWriter, r *http.Request, handler func(repository string) ([]string, int)) {
	t.Helper()
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	repository, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/admin/tags/"))
	require.NoError(t, err)
	tags, status := handler(repository)
	if status >= http.StatusBadRequest {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusNotFound {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "repository not found"}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "boom"}))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"repository": repository, "tags": tags}))
}
