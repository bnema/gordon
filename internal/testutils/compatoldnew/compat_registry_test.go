package compatoldnew

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCompatibilityRegistryScenarios is deliberately a real-binary test.  It
// drives the OCI distribution HTTP protocol rather than calling registry
// handlers directly, and runs the same sequence against detached baseline and
// current binaries in isolated data directories.
func TestCompatibilityRegistryScenarios(t *testing.T) {
	requireRealCompatibilityRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := AdminAPIPreflight(ctx); err != nil {
		if os.Getenv("GORDON_COMPAT_REQUIRE_RUNTIME") == "1" {
			t.Fatal(err)
		}
		t.Skipf("registry compatibility requires Docker: %v", err)
	}
	dir := compatibilityArtifactDir(t, "registry")
	report, err := RunCompatibilityRegistryScenarios(ctx, projectRoot(t), dir)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	require.FileExists(t, filepath.Join(dir, "compat-report.json"))
	require.FileExists(t, filepath.Join(dir, "normalized.diff"))
}

type registryOperation struct {
	Name    string            `json:"name"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	JSON    any               `json:"json,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// RunCompatibilityRegistryScenarios covers the eight registry scenarios as a
// single ordered OCI workflow: ping, challenge, push, pull/tag/digest,
// listing, oversized upload, invalid names/references, and the manifest PUT
// that emits image.pushed. Event delivery and restart replay are separately
// asserted by the exact production outbox/control tests in the gate target.
func RunCompatibilityRegistryScenarios(ctx context.Context, repoRoot, artifactDir string) (Report, error) {
	if repoRoot == "" || artifactDir == "" {
		return Report{}, fmt.Errorf("registry compatibility requires repository and artifact directories")
	}
	binaries, err := BuildOldAndNew(ctx, nil, repoRoot, filepath.Join(artifactDir, "bin"))
	if err != nil {
		return Report{}, err
	}
	parent, err := os.MkdirTemp("", "gordon-compat-registry-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(parent)
	old, oldErr := runRegistrySide(ctx, SideOld, binaries.Old.BinaryPath, parent)
	if oldErr != nil {
		old = registryFailure(SideOld, oldErr)
	}
	new, newErr := runRegistrySide(ctx, SideNew, binaries.New.BinaryPath, parent)
	if newErr != nil {
		new = registryFailure(SideNew, newErr)
	}
	return CompareSideResultsWithMetadata(old, new, nil, artifactDir, ReportMetadata{
		BaselineCommit: binaries.Old.Commit, CandidateCommit: binaries.New.Commit,
		RerunCommand: "GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityRegistryScenarios$' -count=1",
	})
}

func registryFailure(side string, err error) SideResult {
	artifact := HTTPArtifact{baseArtifact{Raw: map[string]any{"captureError": "registry capture failed"}, Normalized: map[string]any{"captureError": "registry capture failed"}, SourceRef: "OCI distribution registry sequence", Compare: LevelExact}}
	return SideResult{Side: side, Artifact: artifact, ValidationError: err}
}

func runRegistrySide(ctx context.Context, side, binary, parent string) (_ SideResult, err error) {
	setup, err := stageAdminAPISide(parent)
	if err != nil {
		return SideResult{}, err
	}
	defer os.RemoveAll(setup.fixture.Root)
	defer func() {
		if closeErr := setup.releaseReservations(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	setup.token, err = generateAdminToken(ctx, binary, setup.fixture, side)
	if err != nil {
		return SideResult{}, err
	}
	address := fmt.Sprintf("127.0.0.1:%d", setup.port)
	instance := &GordonInstance{BinaryPath: binary, ConfigPath: setup.fixture.ConfigPath, DataDir: setup.fixture.DataDir, WorkingDir: setup.fixture.Root, Env: adminAPIEnvironment(setup.fixture), ReadinessProbe: ReadinessProbe{TCPAddress: address}}
	args := []string{"serve", "--config", setup.fixture.ConfigPath}
	if side == SideNew {
		args = []string{"serve", "--role", "monolith", "--config", setup.fixture.ConfigPath}
	}
	if err := setup.releaseReservations(); err != nil {
		return SideResult{}, err
	}
	if err := instance.Start(ctx, args...); err != nil {
		return SideResult{}, fmt.Errorf("start %s: %w", side, err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if stopErr := instance.Stop(stopCtx); stopErr != nil && err == nil {
			err = fmt.Errorf("stop %s: %w; logs: %s", side, stopErr, instance.Logs())
		}
	}()
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := instance.WaitReady(readyCtx); err != nil {
		return SideResult{}, fmt.Errorf("ready %s: %w", side, err)
	}
	token, err := exchangeRegistryCompatibilityToken(ctx, "http://"+address, setup.token, side)
	if err != nil {
		return SideResult{}, err
	}
	artifact, validationErr := captureRegistrySequence(ctx, "http://"+address, token)
	return SideResult{Side: side, Artifact: artifact, ValidationError: validationErr}, nil
}

func exchangeRegistryCompatibilityToken(ctx context.Context, baseURL, longLivedToken, side string) (string, error) {
	query := url.Values{"scope": {"repository:*:push,pull"}, "service": {"gordon-registry"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/auth/token?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth("compat-"+side, longLivedToken)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token exchange returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Token == "" {
		return "", fmt.Errorf("registry token exchange returned an empty token")
	}
	return payload.Token, nil
}

func captureRegistrySequence(ctx context.Context, baseURL, token string) (HTTPArtifact, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	var ops []registryOperation
	failures := []string{}
	request := func(name, method, path, authorization string, body []byte, contentType string, want int) registryOperation {
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(body))
		if err != nil {
			failures = append(failures, name+": create request: "+err.Error())
			return registryOperation{Name: name}
		}
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err != nil {
			failures = append(failures, name+": "+err.Error())
			return registryOperation{Name: name}
		}
		defer resp.Body.Close()
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
		if readErr != nil {
			failures = append(failures, name+": read response: "+readErr.Error())
		}
		op := registryOperation{Name: name, Status: resp.StatusCode, Headers: registryHeaders(resp.Header), Body: string(data)}
		if strings.Contains(resp.Header.Get("Content-Type"), "json") && len(data) > 0 {
			_ = json.Unmarshal(data, &op.JSON)
		}
		if resp.StatusCode != want {
			failures = append(failures, fmt.Sprintf("%s: expected HTTP %d, got %d", name, want, resp.StatusCode))
		}
		ops = append(ops, op)
		return op
	}

	challenge := request("registry/auth-challenge", http.MethodGet, "/v2/", "", nil, "", http.StatusUnauthorized)
	if !strings.HasPrefix(challenge.Headers["WWW-Authenticate"], "Bearer ") {
		failures = append(failures, "registry/auth-challenge: missing Bearer challenge")
	}
	auth := "Bearer " + token
	ping := request("registry/v2-ping", http.MethodGet, "/v2/", auth, nil, "", http.StatusOK)
	if ping.Headers["Docker-Distribution-API-Version"] != "registry/2.0" {
		failures = append(failures, "registry/v2-ping: missing distribution API version")
	}

	const repository, tag = "compat/app", "v1"
	layer := []byte("gordon-compat-oci-layer")
	layerDigest := "sha256:" + fmt.Sprintf("%x", sha256.Sum256(layer))
	start := request("registry/push-image/start", http.MethodPost, "/v2/"+repository+"/blobs/uploads/", auth, nil, "", http.StatusAccepted)
	location := start.Headers["Location"]
	if !strings.HasPrefix(location, "/v2/"+repository+"/blobs/uploads/") || start.Headers["Docker-Upload-UUID"] == "" {
		failures = append(failures, "registry/push-image: invalid upload Location or UUID")
	}
	request("registry/push-image/blob", http.MethodPut, location+"?digest="+url.QueryEscape(layerDigest), auth, layer, "application/octet-stream", http.StatusCreated)
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":0,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`)
	manifestDigest := "sha256:" + fmt.Sprintf("%x", sha256.Sum256(manifest))
	put := request("registry/image-push-event", http.MethodPut, "/v2/"+repository+"/manifests/"+tag, auth, manifest, "application/vnd.docker.distribution.manifest.v2+json", http.StatusCreated)
	if put.Headers["Docker-Content-Digest"] != manifestDigest || put.Headers["Location"] != "/v2/"+repository+"/manifests/"+tag {
		failures = append(failures, "registry/image-push-event: manifest response digest or Location mismatch")
	}
	pull := request("registry/pull-image/tag", http.MethodGet, "/v2/"+repository+"/manifests/"+tag, auth, nil, "", http.StatusOK)
	if pull.Headers["Content-Type"] != "application/vnd.docker.distribution.manifest.v2+json" || pull.Body != string(manifest) {
		failures = append(failures, "registry/pull-image: content type or body mismatch")
	}
	// The distribution API permits (and Docker clients perform) a digest PUT as
	// part of a push. Store it explicitly before asserting digest retrieval.
	request("registry/push-image/manifest-digest", http.MethodPut, "/v2/"+repository+"/manifests/"+manifestDigest, auth, manifest, "application/vnd.docker.distribution.manifest.v2+json", http.StatusCreated)
	request("registry/pull-image/digest", http.MethodGet, "/v2/"+repository+"/manifests/"+manifestDigest, auth, nil, "", http.StatusOK)
	tags := request("registry/tag-list", http.MethodGet, "/v2/"+repository+"/tags/list", auth, nil, "", http.StatusOK)
	if !registryTagsContain(tags.JSON, tag) {
		failures = append(failures, "registry/tag-list: pushed tag absent")
	}
	tooLargeStart := request("registry/upload-too-large/start", http.MethodPost, "/v2/"+repository+"/blobs/uploads/", auth, nil, "", http.StatusAccepted)
	overse := request("registry/upload-too-large", http.MethodPatch, tooLargeStart.Headers["Location"], auth, bytes.Repeat([]byte("x"), 2048), "application/octet-stream", http.StatusRequestEntityTooLarge)
	if registryErrorCode(overse.JSON) != "SIZE_INVALID" {
		failures = append(failures, "registry/upload-too-large: expected SIZE_INVALID")
	}
	badName := request("registry/invalid-name-reference/name", http.MethodGet, "/v2/UPPER/manifests/v1", auth, nil, "", http.StatusBadRequest)
	badRef := request("registry/invalid-name-reference/reference", http.MethodGet, "/v2/"+repository+"/manifests/bad@tag", auth, nil, "", http.StatusBadRequest)
	if registryErrorCode(badName.JSON) != "NAME_INVALID" || registryErrorCode(badRef.JSON) != "TAG_INVALID" {
		failures = append(failures, "registry/invalid-name-reference: wrong OCI error code")
	}
	artifact := HTTPArtifact{baseArtifact{Raw: map[string]any{"operations": ops}, Normalized: registryNormalizedOperations(ops), SourceRef: "OCI distribution registry sequence", Compare: LevelExact}}
	if len(failures) != 0 {
		return artifact, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return artifact, nil
}

func registryHeaders(h http.Header) map[string]string {
	out := make(map[string]string, 5)
	for _, name := range []string{"Docker-Distribution-API-Version", "WWW-Authenticate", "Location", "Docker-Content-Digest", "Content-Type", "Docker-Upload-UUID", "Range"} {
		out[name] = h.Get(name)
	}
	return out
}
func registryNormalizedOperations(ops []registryOperation) any {
	for i := range ops {
		if strings.Contains(ops[i].Name, "start") {
			ops[i].Headers["Location"] = normalizeUploadLocation(ops[i].Headers["Location"])
			ops[i].Headers["Docker-Upload-UUID"] = "<dynamic>"
		}
		if ops[i].Name == "registry/auth-challenge" {
			ops[i].Headers["WWW-Authenticate"] = normalizeRegistryChallenge(ops[i].Headers["WWW-Authenticate"])
		}
	}
	return ops
}

func normalizeRegistryChallenge(value string) string {
	const prefix = `Bearer realm="http://127.0.0.1:`
	if !strings.HasPrefix(value, prefix) {
		return value
	}
	if end := strings.Index(value[len(prefix):], `"`); end >= 0 {
		return `Bearer realm="<dynamic>",` + value[len(prefix)+end+1:]
	}
	return value
}
func normalizeUploadLocation(v string) string {
	if i := strings.LastIndex(v, "/"); i >= 0 {
		return v[:i+1] + "<dynamic>"
	}
	return v
}
func registryTagsContain(value any, tag string) bool {
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	tags, ok := m["tags"].([]any)
	if !ok {
		return false
	}
	for _, v := range tags {
		if v == tag {
			return true
		}
	}
	return false
}
func registryErrorCode(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	es, ok := m["errors"].([]any)
	if !ok || len(es) == 0 {
		return ""
	}
	first, ok := es[0].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := first["code"].(string)
	return code
}
