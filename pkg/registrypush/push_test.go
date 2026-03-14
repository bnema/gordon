package registrypush_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

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
