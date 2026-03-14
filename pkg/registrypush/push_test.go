package registrypush_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/pkg/registrypush"
)

func TestNew_defaults(t *testing.T) {
	p := registrypush.New()
	assert.NotNil(t, p)
}

func TestNew_withChunkSize(t *testing.T) {
	p := registrypush.New(registrypush.WithChunkSize(10 * 1024 * 1024))
	assert.NotNil(t, p)
}

func TestPusher_UploadBlob(t *testing.T) {
	testCases := []struct {
		name               string
		blobSize           int
		chunkSize          int64
		headStatus         int
		expectedPatchSizes []int
		expectedHeadCalls  int
		expectedPostCalls  int
		expectedPutCalls   int
	}{
		{
			name:               "small blob under chunk size",
			blobSize:           100,
			chunkSize:          1024,
			headStatus:         http.StatusNotFound,
			expectedPatchSizes: []int{100},
			expectedHeadCalls:  1,
			expectedPostCalls:  1,
			expectedPutCalls:   1,
		},
		{
			name:               "exact chunk size",
			blobSize:           1024,
			chunkSize:          1024,
			headStatus:         http.StatusNotFound,
			expectedPatchSizes: []int{1024},
			expectedHeadCalls:  1,
			expectedPostCalls:  1,
			expectedPutCalls:   1,
		},
		{
			name:               "large blob across multiple chunks",
			blobSize:           3072,
			chunkSize:          1024,
			headStatus:         http.StatusNotFound,
			expectedPatchSizes: []int{1024, 1024, 1024},
			expectedHeadCalls:  1,
			expectedPostCalls:  1,
			expectedPutCalls:   1,
		},
		{
			name:               "blob already exists",
			blobSize:           512,
			chunkSize:          1024,
			headStatus:         http.StatusOK,
			expectedPatchSizes: nil,
			expectedHeadCalls:  1,
			expectedPostCalls:  0,
			expectedPutCalls:   0,
		},
		{
			name:               "auth header present on every request",
			blobSize:           1536,
			chunkSize:          1024,
			headStatus:         http.StatusNotFound,
			expectedPatchSizes: []int{1024, 512},
			expectedHeadCalls:  1,
			expectedPostCalls:  1,
			expectedPutCalls:   1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			const authHeader = "Bearer test-token"
			const repo = "demo/app"
			const digest = "sha256:testdigest"

			blob := bytes.Repeat([]byte("a"), tc.blobSize)
			serverState := newFakeBlobUploadRegistry(t, authHeader, digest, tc.headStatus)
			server := httptest.NewServer(serverState.handler())
			defer server.Close()

			p := registrypush.New(
				registrypush.WithChunkSize(tc.chunkSize),
				registrypush.WithTransport(server.Client().Transport),
			)

			err := p.UploadBlob(context.Background(), server.URL, repo, digest, int64(len(blob)), bytes.NewReader(blob), authHeader)
			require.NoError(t, err)

			assert.Equal(t, tc.expectedHeadCalls, serverState.headCalls)
			assert.Equal(t, tc.expectedPostCalls, serverState.postCalls)
			assert.Equal(t, tc.expectedPutCalls, serverState.putCalls)
			assert.Equal(t, tc.expectedPatchSizes, serverState.patchSizes)
			assert.Equal(t, tc.expectedPatchSizes, serverState.rangeSizes)
			assert.Equal(t, serverState.requestCount, serverState.authCount)
		})
	}
}

func TestPusher_Push(t *testing.T) {
	testCases := []struct {
		name                       string
		imageSize                  int64
		layerCount                 int64
		existingBlobDigests        func(t *testing.T, img v1.Image) map[string]bool
		expectedUploadedBlobCount  int
		expectedManifestMediaTypes []string
	}{
		{
			name:                      "single-layer image uploads config layer and manifest",
			imageSize:                 1024,
			layerCount:                1,
			existingBlobDigests:       func(t *testing.T, img v1.Image) map[string]bool { return map[string]bool{} },
			expectedUploadedBlobCount: 2,
			expectedManifestMediaTypes: []string{
				"application/vnd.docker.distribution.manifest.v2+json",
				typesOCIManifestSchema1(),
			},
		},
		{
			name:                      "multi-layer image uploads all blobs and manifest",
			imageSize:                 1024,
			layerCount:                2,
			existingBlobDigests:       func(t *testing.T, img v1.Image) map[string]bool { return map[string]bool{} },
			expectedUploadedBlobCount: 3,
			expectedManifestMediaTypes: []string{
				"application/vnd.docker.distribution.manifest.v2+json",
				typesOCIManifestSchema1(),
			},
		},
		{
			name:       "existing layer is skipped while others upload",
			imageSize:  1024,
			layerCount: 2,
			existingBlobDigests: func(t *testing.T, img v1.Image) map[string]bool {
				t.Helper()
				layers, err := img.Layers()
				require.NoError(t, err)
				digest, err := layers[0].Digest()
				require.NoError(t, err)
				return map[string]bool{digest.String(): true}
			},
			expectedUploadedBlobCount: 2,
			expectedManifestMediaTypes: []string{
				"application/vnd.docker.distribution.manifest.v2+json",
				typesOCIManifestSchema1(),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			img, err := random.Image(tc.imageSize, tc.layerCount)
			require.NoError(t, err)

			serverState := newFakePushRegistry(t, img, tc.existingBlobDigests(t, img))
			server := httptest.NewServer(serverState.handler())
			defer server.Close()

			serverURL, err := url.Parse(server.URL)
			require.NoError(t, err)
			ref := serverURL.Host + "/demo/app:v1"

			p := registrypush.New(
				registrypush.WithTransport(server.Client().Transport),
				registrypush.WithImageSource(func(context.Context, string) (v1.Image, error) {
					return img, nil
				}),
			)

			err = p.Push(context.Background(), ref)
			require.NoError(t, err)

			expectedDigests := make([]string, 0, len(serverState.expectedBlobs))
			for digest := range serverState.expectedBlobs {
				expectedDigests = append(expectedDigests, digest)
				assert.Contains(t, serverState.headChecks, digest)
			}
			assert.Len(t, serverState.headChecks, len(expectedDigests))

			assert.Len(t, serverState.uploadedBlobs, tc.expectedUploadedBlobCount)
			for digest, content := range serverState.uploadedBlobs {
				assert.Equal(t, serverState.expectedBlobs[digest], content)
			}

			manifestPut, ok := serverState.manifestPuts[serverState.expectedRepo+":"+serverState.expectedTag]
			require.True(t, ok)
			assert.Contains(t, tc.expectedManifestMediaTypes, manifestPut.contentType)
			assert.Equal(t, serverState.expectedManifest, manifestPut.body)
		})
	}
}

func TestPusher_Push_InvokesCleanup(t *testing.T) {
	var cleanupCalled bool
	img, err := random.Image(1024, 1)
	require.NoError(t, err)

	registry := httptest.NewServer(newFakePushRegistry(t, img, map[string]bool{}).handler())
	defer registry.Close()

	p := registrypush.New(
		registrypush.WithChunkSize(1024),
		registrypush.WithImageSource(func(ctx context.Context, ref string) (v1.Image, error) {
			return img, nil
		}),
		registrypush.WithImageCleanup(func() {
			cleanupCalled = true
		}),
	)

	registryHost := strings.TrimPrefix(registry.URL, "http://")
	err = p.Push(context.Background(), registryHost+"/demo/app:v1")

	require.NoError(t, err)
	assert.True(t, cleanupCalled, "cleanup callback should have been invoked after push")
}

func TestPusher_WithInsecureTLS_AllowsSelfSignedCertificate(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost {
			w.Header().Set("Location", "/v2/repo/blobs/uploads/test-uuid")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.Method == http.MethodPatch {
			w.Header().Set("Location", "/v2/repo/blobs/uploads/test-uuid")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusCreated)
			return
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	content := []byte("test-blob-data")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))

	pusherSecure := registrypush.New(registrypush.WithChunkSize(1024))
	err := pusherSecure.UploadBlob(ctx, srv.URL, "repo", digest, int64(len(content)), bytes.NewReader(content), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate")

	pusherInsecure := registrypush.New(registrypush.WithChunkSize(1024), registrypush.WithInsecureTLS(true))
	err = pusherInsecure.UploadBlob(ctx, srv.URL, "repo", digest, int64(len(content)), bytes.NewReader(content), "")
	require.NoError(t, err)
}

func TestUploadBlob_RejectsCrossOriginRedirect(t *testing.T) {
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request leaked to attacker: %s %s (Authorization: %s)", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost:
			w.Header().Set("Location", attacker.URL+"/v2/repo/blobs/uploads/evil")
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer registry.Close()

	p := registrypush.New(registrypush.WithChunkSize(1024))
	ctx := context.Background()
	err := p.UploadBlob(ctx, registry.URL, "test/repo", "sha256:abc", 5, strings.NewReader("hello"), "Basic secret")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-origin")
}

// authRecordingTransport records Authorization headers per request URL.
type authRecordingTransport struct {
	inner   http.RoundTripper
	records []authRecord
}

type authRecord struct {
	URL  string
	Auth string
}

func (t *authRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.records = append(t.records, authRecord{
		URL:  req.URL.String(),
		Auth: req.Header.Get("Authorization"),
	})
	return t.inner.RoundTrip(req)
}

func TestUploadBlob_AuthHeaderOnlySentToSameOrigin(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost:
			w.Header().Set("Location", "/v2/repo/blobs/uploads/abc?chunk=0")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPatch:
			w.Header().Set("Location", "/v2/repo/blobs/uploads/abc?chunk=1")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer registry.Close()

	recorder := &authRecordingTransport{inner: http.DefaultTransport}
	p := registrypush.New(
		registrypush.WithChunkSize(1024),
		registrypush.WithTransport(recorder),
	)
	ctx := context.Background()
	secret := "Basic dXNlcjpwYXNz"
	err := p.UploadBlob(ctx, registry.URL, "test/repo", "sha256:abc", 5, strings.NewReader("hello"), secret)

	require.NoError(t, err)
	require.NotEmpty(t, recorder.records)

	registryURL, _ := url.Parse(registry.URL)
	for _, rec := range recorder.records {
		reqURL, _ := url.Parse(rec.URL)
		if reqURL.Host == registryURL.Host {
			assert.Equal(t, secret, rec.Auth, "same-origin request to %s should have auth", rec.URL)
		} else {
			assert.Empty(t, rec.Auth, "cross-origin request to %s should NOT have auth", rec.URL)
		}
	}
}

type fakeBlobUploadRegistry struct {
	t              *testing.T
	expectedAuth   string
	expectedDigest string
	headStatus     int
	patchSizes     []int
	rangeSizes     []int
	headCalls      int
	postCalls      int
	putCalls       int
	requestCount   int
	authCount      int
	nextOffset     int
	location       string
	uuid           string
}

func newFakeBlobUploadRegistry(t *testing.T, expectedAuth, expectedDigest string, headStatus int) *fakeBlobUploadRegistry {
	t.Helper()
	return &fakeBlobUploadRegistry{
		t:              t,
		expectedAuth:   expectedAuth,
		expectedDigest: expectedDigest,
		headStatus:     headStatus,
		location:       "/upload/session-1",
		uuid:           "session-1",
	}
}

func (f *fakeBlobUploadRegistry) handler() http.Handler {
	f.t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requestCount++
		if r.Header.Get("Authorization") == f.expectedAuth {
			f.authCount++
		}

		switch {
		case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
			f.headCalls++
			w.WriteHeader(f.headStatus)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
			f.postCalls++
			w.Header().Set("Location", f.location)
			w.Header().Set("Docker-Upload-UUID", f.uuid)
			w.Header().Set("Range", rangeHeaderValue(0, f.nextOffset-1))
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPatch && r.URL.Path == f.location:
			f.handlePatch(w, r)
		case r.Method == http.MethodPut && r.URL.Path == f.location:
			f.putCalls++
			assert.Equal(f.t, f.expectedDigest, r.URL.Query().Get("digest"))
			body, err := io.ReadAll(r.Body)
			require.NoError(f.t, err)
			assert.Len(f.t, body, 0)
			w.Header().Set("Location", f.location)
			w.Header().Set("Docker-Content-Digest", f.expectedDigest)
			w.WriteHeader(http.StatusCreated)
		default:
			f.t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
}

func (f *fakeBlobUploadRegistry) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	require.NoError(f.t, err)

	start, end := parseContentRange(f.t, r.Header.Get("Content-Range"))
	assert.Equal(f.t, f.nextOffset, start)
	assert.Equal(f.t, len(body), end-start+1)

	contentLength, err := strconv.Atoi(r.Header.Get("Content-Length"))
	require.NoError(f.t, err)
	assert.Equal(f.t, len(body), contentLength)
	assert.Equal(f.t, "application/octet-stream", r.Header.Get("Content-Type"))

	f.patchSizes = append(f.patchSizes, len(body))
	f.rangeSizes = append(f.rangeSizes, end-start+1)
	f.nextOffset = end + 1

	w.Header().Set("Location", f.location)
	w.Header().Set("Docker-Upload-UUID", f.uuid)
	w.Header().Set("Range", rangeHeaderValue(0, end))
	w.WriteHeader(http.StatusAccepted)
}

func parseContentRange(t *testing.T, value string) (int, int) {
	t.Helper()
	parts := strings.Split(value, "-")
	require.Len(t, parts, 2)

	start, err := strconv.Atoi(parts[0])
	require.NoError(t, err)

	end, err := strconv.Atoi(parts[1])
	require.NoError(t, err)

	return start, end
}

func rangeHeaderValue(start, end int) string {
	if end < start {
		return ""
	}
	return fmt.Sprintf("%d-%d", start, end)
}

type fakePushRegistry struct {
	t                *testing.T
	existingBlobs    map[string]bool
	uploadedBlobs    map[string][]byte
	manifestPuts     map[string]fakeManifestPut
	uploads          map[string]*fakeUploadSession
	nextUploadID     int
	headChecks       []string
	expectedBlobs    map[string][]byte
	expectedRepo     string
	expectedTag      string
	expectedMT       string
	expectedManifest []byte
}

type fakeUploadSession struct {
	repo string
	data []byte
}

type fakeManifestPut struct {
	contentType string
	body        []byte
}

func newFakePushRegistry(t *testing.T, img v1.Image, existingBlobs map[string]bool) *fakePushRegistry {
	t.Helper()

	layers, err := img.Layers()
	require.NoError(t, err)

	expectedBlobs := make(map[string][]byte, len(layers)+1)
	for _, layer := range layers {
		digest, err := layer.Digest()
		require.NoError(t, err)
		rc, err := layer.Compressed()
		require.NoError(t, err)
		content, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		expectedBlobs[digest.String()] = content
	}

	config, err := img.RawConfigFile()
	require.NoError(t, err)
	configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
	expectedBlobs[configDigest] = config

	manifest, err := img.RawManifest()
	require.NoError(t, err)
	mediaType, err := img.MediaType()
	require.NoError(t, err)

	return &fakePushRegistry{
		t:                t,
		existingBlobs:    existingBlobs,
		uploadedBlobs:    make(map[string][]byte),
		manifestPuts:     make(map[string]fakeManifestPut),
		uploads:          make(map[string]*fakeUploadSession),
		expectedBlobs:    expectedBlobs,
		expectedRepo:     "demo/app",
		expectedTag:      "v1",
		expectedMT:       string(mediaType),
		expectedManifest: manifest,
	}
}

func (f *fakePushRegistry) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/v2/") && strings.Contains(r.URL.Path, "/blobs/"):
			f.handleBlobHead(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
			f.handleBlobStart(w, r)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			f.handleBlobPatch(w, r)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			f.handleBlobFinalize(w, r)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
			f.handleManifestPut(w, r)
		default:
			f.t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
}

func (f *fakePushRegistry) handleBlobHead(w http.ResponseWriter, r *http.Request) {
	digest := pathTail(r.URL.Path)
	f.headChecks = append(f.headChecks, digest)
	if f.existingBlobs[digest] {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (f *fakePushRegistry) handleBlobStart(w http.ResponseWriter, r *http.Request) {
	repo := pathSegmentBetween(r.URL.Path, "/v2/", "/blobs/uploads/")
	assert.Equal(f.t, f.expectedRepo, repo)
	f.nextUploadID++
	uploadPath := fmt.Sprintf("/v2/%s/blobs/uploads/upload-%d", repo, f.nextUploadID)
	f.uploads[uploadPath] = &fakeUploadSession{repo: repo}
	w.Header().Set("Location", uploadPath)
	w.WriteHeader(http.StatusAccepted)
}

func (f *fakePushRegistry) handleBlobPatch(w http.ResponseWriter, r *http.Request) {
	session := f.uploads[r.URL.Path]
	if session == nil {
		f.t.Fatalf("missing upload session for %s", r.URL.Path)
	}
	body, err := io.ReadAll(r.Body)
	require.NoError(f.t, err)
	session.data = append(session.data, body...)
	w.Header().Set("Location", r.URL.Path)
	w.WriteHeader(http.StatusAccepted)
}

func (f *fakePushRegistry) handleBlobFinalize(w http.ResponseWriter, r *http.Request) {
	session := f.uploads[r.URL.Path]
	if session == nil {
		f.t.Fatalf("missing upload session for %s", r.URL.Path)
	}
	digest := r.URL.Query().Get("digest")
	f.uploadedBlobs[digest] = append([]byte(nil), session.data...)
	delete(f.uploads, r.URL.Path)
	w.WriteHeader(http.StatusCreated)
}

func (f *fakePushRegistry) handleManifestPut(w http.ResponseWriter, r *http.Request) {
	repo := pathSegmentBetween(r.URL.Path, "/v2/", "/manifests/")
	tag := pathTail(r.URL.Path)
	assert.Equal(f.t, f.expectedRepo, repo)
	assert.Equal(f.t, f.expectedTag, tag)
	body, err := io.ReadAll(r.Body)
	require.NoError(f.t, err)
	f.manifestPuts[repo+":"+tag] = fakeManifestPut{contentType: r.Header.Get("Content-Type"), body: body}
	w.WriteHeader(http.StatusCreated)
}

func pathTail(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

func pathSegmentBetween(value, start, end string) string {
	startIdx := strings.Index(value, start)
	if startIdx == -1 {
		return ""
	}
	trimmed := value[startIdx+len(start):]
	endIdx := strings.Index(trimmed, end)
	if endIdx == -1 {
		return ""
	}
	return trimmed[:endIdx]
}

func typesOCIManifestSchema1() string {
	return "application/vnd.oci.image.manifest.v1+json"
}
