package route_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/http/registry/route"
)

// validDigest is a syntactically valid sha256 digest.
const validDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// validUUID is a syntactically valid v4 UUID.
const validUUID = "12345678-1234-4123-8123-123456789012"

func TestParse_Routes(t *testing.T) {
	tests := []struct {
		path string
		want route.Operation
	}{
		{"/v2/", route.Operation{Kind: route.KindBase}},
		{"/v2", route.Operation{Kind: route.KindBase}},
		{"/v2/_catalog", route.Operation{Kind: route.KindCatalog}},
		{
			"/v2/myrepo/manifests/latest",
			route.Operation{Kind: route.KindManifest, Repository: "myrepo", Reference: "latest"},
		},
		{
			"/v2/myorg/myapp/manifests/v1.0",
			route.Operation{Kind: route.KindManifest, Repository: "myorg/myapp", Reference: "v1.0"},
		},
		{
			"/v2/myrepo/blobs/" + validDigest,
			route.Operation{Kind: route.KindBlob, Repository: "myrepo", Digest: validDigest},
		},
		{"/v2/myrepo/blobs/uploads/", route.Operation{Kind: route.KindUpload, Repository: "myrepo"}},
		{
			"/v2/myrepo/blobs/uploads/" + validUUID,
			route.Operation{Kind: route.KindUpload, Repository: "myrepo", UploadID: validUUID},
		},
		{"/v2/myrepo/tags/list", route.Operation{Kind: route.KindTagList, Repository: "myrepo"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			op, err := route.Parse(tt.path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, op)
		})
	}
}

// TestParse_ResolvesRepositoryFromLastMarker proves a reserved-looking
// component earlier in the path cannot shorten the repository: the parser
// (and therefore authorization) always sees the repository the handler
// would serve.
func TestParse_ResolvesRepositoryFromLastMarker(t *testing.T) {
	tests := []struct {
		path     string
		wantRepo string
	}{
		{"/v2/manifests/victim/manifests/latest", "manifests/victim"},
		{"/v2/allowed/tags/victim/manifests/latest", "allowed/tags/victim"},
		{"/v2/blobs/impostor/blobs/" + validDigest, "blobs/impostor"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			op, err := route.Parse(tt.path)
			require.NoError(t, err)
			assert.Equal(t, tt.wantRepo, op.Repository)
		})
	}
}

func TestParse_Errors(t *testing.T) {
	notFound := []string{
		"/healthz",
		"/v2/myrepo",
		"/v2/myrepo/manifests/",
		"/v2/myrepo/manifests/a/b",
		"/v2/myrepo/blobs/",
		"/v2/myrepo/tags/list/",
		"/v2//manifests/latest",
	}
	for _, path := range notFound {
		t.Run("not found "+path, func(t *testing.T) {
			_, err := route.Parse(path)
			assert.ErrorIs(t, err, route.ErrNotFound)
		})
	}

	cases := []struct {
		path     string
		wantCode string
	}{
		{"/v2/BadRepo/manifests/latest", route.CodeNameInvalid},
		{"/v2/myrepo/manifests/bad tag", route.CodeTagInvalid},
		{"/v2/myrepo/blobs/notadigest", route.CodeDigestInvalid},
		{"/v2/myrepo/blobs/uploads/not-a-uuid", route.CodeBlobUploadInvalid},
	}
	for _, tc := range cases {
		t.Run("invalid "+tc.path, func(t *testing.T) {
			_, err := route.Parse(tc.path)
			assert.ErrorIs(t, err, route.ErrInvalid)
			var parseErr *route.ParseError
			require.ErrorAs(t, err, &parseErr)
			assert.Equal(t, tc.wantCode, parseErr.Code)
		})
	}
}

func TestRequiresRepositoryAuth_ProtocolRootsOnly(t *testing.T) {
	base, err := route.Parse("/v2/")
	require.NoError(t, err)
	assert.False(t, base.RequiresRepositoryAuth())

	catalog, err := route.Parse("/v2/_catalog")
	require.NoError(t, err)
	assert.False(t, catalog.RequiresRepositoryAuth())

	manifest, err := route.Parse("/v2/myrepo/manifests/latest")
	require.NoError(t, err)
	assert.True(t, manifest.RequiresRepositoryAuth())
}
