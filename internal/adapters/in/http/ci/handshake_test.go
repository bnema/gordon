package ci

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	registryhttp "github.com/bnema/gordon/internal/adapters/in/http/registry"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/domain"
	registrysvc "github.com/bnema/gordon/internal/usecase/registry"
)

type noopPublisher struct{}

func (noopPublisher) Publish(domain.EventType, any) error { return nil }

// recordingHandler records method and path of every request the CI router
// receives, in order.
type recordingHandler struct {
	next  http.Handler
	mu    sync.Mutex
	steps []string
	codes []int
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
	h.next.ServeHTTP(rec, r)
	h.mu.Lock()
	h.steps = append(h.steps, r.Method+" "+r.URL.Path)
	h.codes = append(h.codes, rec.code)
	h.mu.Unlock()
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// TestHandshake_DockerStylePushThroughCIHost drives a registry client
// through the CI host over real TLS (httptest, so outside synctest): the
// unauthenticated ping gets the Bearer challenge, the client exchanges its
// OIDC JWT at the announced realm, then uploads blobs and PUTs the
// manifest into the real registry handler.
func TestHandshake_DockerStylePushThroughCIHost(t *testing.T) {
	log := zerowrap.Default()
	root := t.TempDir()
	blobs, err := filesystem.NewBlobStorage(root, log)
	require.NoError(t, err)
	manifests, err := filesystem.NewManifestStorage(root, log)
	require.NoError(t, err)
	raw := registryhttp.NewHandler(registrysvc.NewService(blobs, manifests, noopPublisher{}), log, 1<<20)

	h := newHarness(t, func(d *Deps) { d.Registry = raw })
	recorder := &recordingHandler{next: h.handler}
	srv := httptest.NewTLSServer(recorder)
	t.Cleanup(srv.Close)

	// The test certificate covers *.example.com; every name dials the server.
	transport := srv.Client().Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}

	img, err := random.Image(2048, 2)
	require.NoError(t, err)
	ref, err := name.ParseReference(testDomain + "/web:v1")
	require.NoError(t, err)
	creds := authn.FromConfig(authn.AuthConfig{Username: "oidc", Password: "jwt-web"})
	require.NoError(t, remote.Write(ref, img, remote.WithAuth(creds), remote.WithTransport(transport)))

	want, err := img.RawManifest()
	require.NoError(t, err)
	stored, _, err := manifests.GetManifest("web", "v1")
	require.NoError(t, err)
	assert.Equal(t, want, stored, "the manifest landed in the registry")

	recorder.mu.Lock()
	steps, codes := append([]string(nil), recorder.steps...), append([]int(nil), recorder.codes...)
	recorder.mu.Unlock()
	require.GreaterOrEqual(t, len(steps), 4)
	assert.Equal(t, "GET /v2/", steps[0])
	assert.Equal(t, http.StatusUnauthorized, codes[0], "the ping is challenged")
	assert.Equal(t, "GET /auth/token", steps[1])
	assert.Equal(t, http.StatusOK, codes[1])
	assert.Contains(t, steps, "POST /v2/web/blobs/uploads/")
	assert.Equal(t, "PUT /v2/web/manifests/v1", steps[len(steps)-1])
	assert.Equal(t, http.StatusCreated, codes[len(codes)-1])

	// The same identity cannot push into a repository outside its policies.
	other, err := name.ParseReference(testDomain + "/other:v1")
	require.NoError(t, err)
	require.Error(t, remote.Write(other, img, remote.WithAuth(creds), remote.WithTransport(transport)))
	_, _, err = manifests.GetManifest("other", "v1")
	assert.Error(t, err, "nothing was stored")

	// Pulls stay closed on the CI host, even with a valid token.
	_, err = remote.Image(ref, remote.WithAuth(creds), remote.WithTransport(transport))
	assert.Error(t, err)
}
