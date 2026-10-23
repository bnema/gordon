package registrypush_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/pkg/registrypush"
)

func TestPushServerManifestDigest(t *testing.T) {
	for _, digest := range []string{"sha256:" + strings.Repeat("a", 64), "", "sha256:bad", "not-a-digest"} {
		t.Run(digest, func(t *testing.T) {
			img, err := random.Image(16, 1)
			require.NoError(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusOK)
					return
				}
				require.Equal(t, "/v2/demo/app/manifests/v1", r.URL.Path)
				w.Header().Set("Docker-Content-Digest", digest)
				w.WriteHeader(http.StatusCreated)
			}))
			defer server.Close()
			p := registrypush.New(registrypush.WithImageSource(func(context.Context, string) (v1.Image, error) { return img, nil }))
			got, err := p.Push(context.Background(), strings.TrimPrefix(server.URL, "http://")+"/demo/app:v1")
			if digest == "sha256:"+strings.Repeat("a", 64) {
				require.NoError(t, err)
				require.Equal(t, digest, got)
			} else {
				require.Error(t, err)
				require.Empty(t, got)
			}
		})
	}
}
