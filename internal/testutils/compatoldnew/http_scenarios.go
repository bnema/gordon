package compatoldnew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	adminAuthScenarioName  = "api/auth-missing-invalid"
	adminRouteListScenario = "api/route-list-detail"
	adminRouteCRUDScenario = "api/route-add-update-remove"
)

// HTTPScenarios returns Phase 5 HTTP/admin API compatibility scenario shells.
func HTTPScenarios() []Scenario {
	return []Scenario{
		implementedScenario(adminAuthScenarioName, SurfaceAPI, "6.3 Admin API compatibility", false),
		implementedScenario(adminRouteListScenario, SurfaceAPI, "6.3 Admin API compatibility", false),
		implementedScenario(adminRouteCRUDScenario, SurfaceAPI, "6.3 Admin API compatibility", false),
		httpScenario("api/reload"),
		httpScenario("api/health-status"),
		httpScenario("api/request-too-large"),
		httpScenario("api/forbidden-permission"),
	}
}

func httpScenario(name string) Scenario {
	return pendingScenario(name, SurfaceAPI, "6.3 Admin API compatibility", false, "old/new admin API compatibility scenario execution is not implemented yet")
}

// AdminAPIPreflight verifies that the local Docker daemon required to boot a
// real Gordon server is available. CI can call this directly to make an
// unavailable runtime a hard failure instead of a skipped integration test.
func AdminAPIPreflight(ctx context.Context) error {
	socket, err := adminDockerSocket()
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return fmt.Errorf("docker socket %q unavailable: %w", socket, err)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("close docker socket probe: %w", err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return fmt.Errorf("create docker ping request: %w", err)
	}
	response, err := (&http.Client{Transport: transport, Timeout: time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("ping docker daemon: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("docker daemon ping returned HTTP %d", response.StatusCode)
	}
	return nil
}

func adminDockerSocket() (string, error) {
	host := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if host == "" {
		return "/var/run/docker.sock", nil
	}
	if !strings.HasPrefix(host, "unix://") {
		return "", fmt.Errorf("DOCKER_HOST must be a unix socket for admin API compatibility, got %q", host)
	}
	socket := strings.TrimPrefix(host, "unix://")
	if socket == "" {
		return "", fmt.Errorf("DOCKER_HOST has an empty unix socket path")
	}
	return socket, nil
}

// RunCompatibilityAdminAuthAndRouteCRUD builds and runs baseline and candidate
// sequentially, capturing the exact authenticated route API operation order.
func RunCompatibilityAdminAuthAndRouteCRUD(ctx context.Context, repoRoot, artifactDir string) (Report, error) {
	if repoRoot == "" {
		return Report{}, fmt.Errorf("admin API compatibility: repository root is required")
	}
	if artifactDir == "" {
		return Report{}, fmt.Errorf("admin API compatibility: report artifact directory is required")
	}
	if err := AdminAPIPreflight(ctx); err != nil {
		return Report{}, err
	}

	binaries, err := BuildOldAndNew(ctx, nil, repoRoot, filepath.Join(artifactDir, "bin"))
	if err != nil {
		return Report{}, err
	}
	parent, err := os.MkdirTemp("", "gordon-compat-admin-api-*")
	if err != nil {
		return Report{}, fmt.Errorf("admin API compatibility: create fixture parent: %w", err)
	}
	defer os.RemoveAll(parent)

	old, oldErr := runAdminAPISide(ctx, SideOld, binaries.Old.BinaryPath, parent)
	if oldErr != nil {
		old = adminAPICaptureFailure(SideOld, oldErr)
	}
	new, newErr := runAdminAPISide(ctx, SideNew, binaries.New.BinaryPath, parent)
	if newErr != nil {
		new = adminAPICaptureFailure(SideNew, newErr)
	}
	// Do not allowlist API differences for custom baselines: a deliberately
	// pre-fix baseline such as 420070bc^ must report its delete behavior drift.
	// The designated origin/main baseline passes this exact comparison.
	return CompareSideResultsWithMetadata(old, new, nil, artifactDir, ReportMetadata{
		BaselineCommit:  binaries.Old.Commit,
		CandidateCommit: binaries.New.Commit,
		RerunCommand:    adminAPIRerunCommand(),
	})
}

func adminAPICaptureFailure(side string, validationErr error) SideResult {
	return SideResult{
		Side:            side,
		Artifact:        HTTPArtifact{baseArtifact{Raw: map[string]any{"captureError": "admin API capture failed"}, Normalized: map[string]any{"captureError": "admin API capture failed"}, SourceRef: "admin route auth and CRUD sequence", Compare: LevelExact}},
		ValidationError: validationErr,
	}
}

const adminAPIStartupAttempts = 2

type adminAPISide struct {
	fixture      SideFixture
	port         int
	proxyPort    int
	reservations *adminAPIPortReservations
	token        string
}

type adminAPIPortReservations struct {
	registry net.Listener
	proxy    net.Listener
}

func (r *adminAPIPortReservations) close() error {
	if r == nil {
		return nil
	}
	listeners := []net.Listener{r.registry, r.proxy}
	r.registry, r.proxy = nil, nil
	var errs []error
	for _, listener := range listeners {
		if listener != nil {
			errs = append(errs, listener.Close())
		}
	}
	return errors.Join(errs...)
}

func (setup *adminAPISide) releaseReservations() error {
	if setup == nil {
		return nil
	}
	return setup.reservations.close()
}

func runAdminAPISide(ctx context.Context, side, binaryPath, parent string) (SideResult, error) {
	var lastErr error
	for attempt := 1; attempt <= adminAPIStartupAttempts; attempt++ {
		result, err := runAdminAPISideAttempt(ctx, side, binaryPath, parent)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt == adminAPIStartupAttempts || !isAdminAPIBindRace(err) {
			return SideResult{}, err
		}
	}
	return SideResult{}, lastErr
}

// runAdminAPISideAttempt keeps both listener reservations while writing config
// and generating credentials, then releases them immediately before Start.
func runAdminAPISideAttempt(ctx context.Context, side, binaryPath, parent string) (_ SideResult, err error) {
	setup, err := stageAdminAPISide(parent)
	if err != nil {
		return SideResult{}, err
	}
	defer os.RemoveAll(setup.fixture.Root)
	defer func() {
		if releaseErr := setup.releaseReservations(); releaseErr != nil && err == nil {
			err = fmt.Errorf("release admin API port reservations: %w", releaseErr)
		}
	}()

	setup.token, err = generateAdminToken(ctx, binaryPath, setup.fixture, side)
	if err != nil {
		return SideResult{}, err
	}
	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(setup.port))
	instance := &GordonInstance{
		BinaryPath:     binaryPath,
		ConfigPath:     setup.fixture.ConfigPath,
		DataDir:        setup.fixture.DataDir,
		WorkingDir:     setup.fixture.Root,
		Env:            adminAPIEnvironment(setup.fixture),
		ReadinessProbe: ReadinessProbe{TCPAddress: address},
	}
	serveArgs := []string{"serve", "--config", setup.fixture.ConfigPath}
	if side == SideNew {
		serveArgs = []string{"serve", "--role", "monolith", "--config", setup.fixture.ConfigPath}
	}
	if err := setup.releaseReservations(); err != nil {
		return SideResult{}, fmt.Errorf("release admin API port reservations before start: %w", err)
	}
	if err := instance.Start(ctx, serveArgs...); err != nil {
		return SideResult{}, fmt.Errorf("admin API %s start: %w", side, err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if stopErr := instance.Stop(stopCtx); stopErr != nil && err == nil {
			err = fmt.Errorf("admin API %s stop: %w; logs: %s", side, stopErr, instance.Logs())
		}
	}()
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := instance.WaitReady(readyCtx); err != nil {
		return SideResult{}, fmt.Errorf("admin API %s ready: %w", side, err)
	}

	accessToken, err := exchangeAdminToken(ctx, "http://"+address, setup.token, side)
	if err != nil {
		return SideResult{}, fmt.Errorf("admin API %s exchange credential: %w; logs: %s", side, err, instance.Logs())
	}
	artifact, validationErr := captureAdminRouteCRUD(ctx, "http://"+address, accessToken)
	return SideResult{Side: side, Artifact: artifact, ValidationError: validationErr}, nil
}

func isAdminAPIBindRace(err error) bool {
	return err != nil && strings.Contains(err.Error(), "address already in use")
}

func stageAdminAPISide(parent string) (adminAPISide, error) {
	fixture, err := StageSideFixture(parent, filepath.Join(FixtureRoot(), "configs", "minimal.toml"))
	if err != nil {
		return adminAPISide{}, err
	}
	reservations, err := reserveAdminAPIPorts()
	if err != nil {
		_ = os.RemoveAll(fixture.Root)
		return adminAPISide{}, err
	}
	cleanup := func(stageErr error) (adminAPISide, error) {
		if closeErr := reservations.close(); closeErr != nil {
			stageErr = errors.Join(stageErr, fmt.Errorf("release admin API port reservations: %w", closeErr))
		}
		_ = os.RemoveAll(fixture.Root)
		return adminAPISide{}, stageErr
	}
	port := reservations.registry.Addr().(*net.TCPAddr).Port
	proxyPort := reservations.proxy.Addr().(*net.TCPAddr).Port
	// #nosec G101 -- this isolated unsafe-backend fixture deliberately needs a deterministic test secret.
	secret := "compat-admin-api-test-only-secret-0123456789"
	config := fmt.Sprintf(`[server]
registry_port = %d
registry_listen_address = "127.0.0.1"
data_dir = %q
gordon_domain = "localhost"

[entrypoints.edge]
address = "127.0.0.1:%d"
protocol = "smart_tcp"

[auth]
enabled = true
secrets_backend = "unsafe"
token_secret = %q
token_expiry = "30d"
access_token_ttl = "15m"

[api.rate_limit]
enabled = false

[network_isolation]
enabled = false

[logging]
level = "warn"
format = "console"
`, port, fixture.DataDir, proxyPort, secret)
	if err := os.WriteFile(fixture.ConfigPath, []byte(config), 0o600); err != nil {
		return cleanup(fmt.Errorf("write admin API config: %w", err))
	}
	secretPath := filepath.Join(fixture.DataDir, "secrets", secret)
	if err := os.MkdirAll(filepath.Dir(secretPath), 0o700); err != nil {
		return cleanup(fmt.Errorf("create unsafe test secret directory: %w", err))
	}
	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		return cleanup(fmt.Errorf("write unsafe test secret: %w", err))
	}
	return adminAPISide{fixture: fixture, port: port, proxyPort: proxyPort, reservations: reservations}, nil
}

// reserveAdminAPIPorts holds both listeners concurrently so the registry and
// proxy ports stay distinct until the server is ready to claim them.
func reserveAdminAPIPorts() (*adminAPIPortReservations, error) {
	registry, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve admin API registry port: %w", err)
	}
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		closeErr := registry.Close()
		if closeErr != nil {
			return nil, errors.Join(fmt.Errorf("reserve admin API proxy port: %w", err), fmt.Errorf("release admin API registry reservation: %w", closeErr))
		}
		return nil, fmt.Errorf("reserve admin API proxy port: %w", err)
	}
	return &adminAPIPortReservations{registry: registry, proxy: proxy}, nil
}

func adminAPIEnvironment(fixture SideFixture) []string {
	return append(append([]string{}, fixture.Env...),
		"XDG_CONFIG_HOME="+filepath.Join(fixture.Root, "xdg-config"),
		"XDG_RUNTIME_DIR="+filepath.Join(fixture.Root, "runtime"),
		"GORDON_AUTH_TOKEN_SECRET=",
		"GORDON_ROLE=monolith",
		"GORDON_REMOTE=",
		"GORDON_TOKEN=",
	)
}

var jwtLine = regexp.MustCompile(`(?m)^eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+$`)

func generateAdminToken(ctx context.Context, binaryPath string, fixture SideFixture, side string) (string, error) {
	capture, err := CaptureCommand(ctx, CommandCaptureRequest{
		BinaryPath: binaryPath,
		Args: []string{"auth", "token", "generate", "--config", fixture.ConfigPath, "--subject", "compat-" + side,
			"--scopes", "admin:*:*", "--expiry", "0"},
		Dir: fixture.Root, Env: adminAPIEnvironment(fixture), Source: "gordon auth token generate", Level: LevelExact,
	})
	if err != nil {
		return "", err
	}
	raw := capture.RawValue().(map[string]any)
	if raw["exitCode"] != 0 {
		return "", fmt.Errorf("token generation exited %v: %s", raw["exitCode"], raw["stderr"])
	}
	token := jwtLine.FindString(raw["stdout"].(string))
	if token == "" {
		return "", fmt.Errorf("token generation did not emit a JWT")
	}
	return token, nil
}

func exchangeAdminToken(ctx context.Context, baseURL, token, side string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/auth/token?scope="+url.QueryEscape("admin:*:*")+"&service=gordon-registry", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth("compat-"+side, token)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return "", fmt.Errorf("token exchange returned HTTP %d: %s", response.StatusCode, body)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode token exchange: %w", err)
	}
	if payload.Token == "" {
		return "", fmt.Errorf("token exchange returned an empty token")
	}
	return payload.Token, nil
}

type adminOperation struct {
	Name             string            `json:"name"`
	Status           int               `json:"status"`
	Headers          map[string]string `json:"headers"`
	Body             string            `json:"body"`
	JSON             any               `json:"json"`
	DecodeError      string            `json:"decodeError,omitempty"`
	RequestError     string            `json:"requestError,omitempty"`
	DTOFields        []string          `json:"dtoFields"`
	ValidationErrors []string          `json:"validationErrors,omitempty"`
}

func captureAdminRouteCRUD(ctx context.Context, baseURL, token string) (HTTPArtifact, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	domain := "compat-route.example.test"
	operations := make([]adminOperation, 0, 8)
	requests := []struct {
		name, method, path, body string
		auth                     string
		expectedStatus           int
		expectedFields           []string
	}{
		{"missing-auth", http.MethodGet, "/admin/routes", "", "", http.StatusUnauthorized, []string{"error"}},
		{"invalid-bearer", http.MethodGet, "/admin/routes", "", "Bearer invalid", http.StatusUnauthorized, []string{"error"}},
		{"empty-list", http.MethodGet, "/admin/routes", "", "Bearer " + token, http.StatusOK, []string{"routes"}},
		{"create", http.MethodPost, "/admin/routes", `{"domain":"compat-route.example.test","image":"busybox:1.36","https":false}`, "Bearer " + token, http.StatusCreated, []string{"domain", "image", "https"}},
		{"detail", http.MethodGet, "/admin/routes/" + domain, "", "Bearer " + token, http.StatusOK, []string{"domain", "image", "https"}},
		{"update", http.MethodPut, "/admin/routes/" + domain, `{"image":"busybox:1.37","https":true}`, "Bearer " + token, http.StatusOK, []string{"domain", "image", "https"}},
		{"delete", http.MethodDelete, "/admin/routes/" + domain, "", "Bearer " + token, http.StatusOK, []string{"status"}},
		{"missing-after-delete", http.MethodGet, "/admin/routes/" + domain, "", "Bearer " + token, http.StatusNotFound, []string{"error"}},
	}
	var contractFailures []string
	for _, spec := range requests {
		op := captureAdminOperation(ctx, client, baseURL, spec.name, spec.method, spec.path, spec.body, spec.auth, spec.expectedStatus, spec.expectedFields)
		operations = append(operations, op)
		contractFailures = append(contractFailures, op.ValidationErrors...)
	}
	artifact := HTTPArtifact{baseArtifact{
		Raw:        map[string]any{"operations": operations},
		Normalized: normalizeAdminOperations(operations),
		SourceRef:  "admin route auth and CRUD sequence",
		Compare:    LevelExact,
	}}
	if len(contractFailures) > 0 {
		return artifact, fmt.Errorf("%s", strings.Join(contractFailures, "; "))
	}
	return artifact, nil
}

func captureAdminOperation(ctx context.Context, client *http.Client, baseURL, name, method, path, body, authorization string, expectedStatus int, expectedFields []string) adminOperation {
	op := adminOperation{Name: name, Headers: map[string]string{}, DTOFields: expectedFields}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, strings.NewReader(body))
	if err != nil {
		op.RequestError = err.Error()
		op.ValidationErrors = []string{fmt.Sprintf("%s create request: %v", name, err)}
		return op
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	response, err := client.Do(req)
	if err != nil {
		op.RequestError = err.Error()
		op.ValidationErrors = []string{fmt.Sprintf("%s request: %v", name, err)}
		return op
	}
	defer response.Body.Close()
	op.Status = response.StatusCode
	op.Headers = adminSafeHeaders(response.Header)
	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		op.RequestError = fmt.Sprintf("read response: %v", err)
		op.ValidationErrors = []string{fmt.Sprintf("%s read response: %v", name, err)}
		return op
	}
	op.Body = string(bodyBytes)
	if err := json.Unmarshal(bodyBytes, &op.JSON); err != nil {
		op.DecodeError = "invalid JSON"
		op.ValidationErrors = append(op.ValidationErrors, fmt.Sprintf("%s decode JSON: %v", name, err))
	}
	if response.StatusCode != expectedStatus {
		op.ValidationErrors = append(op.ValidationErrors, fmt.Sprintf("%s: expected HTTP %d, got %d", name, expectedStatus, response.StatusCode))
	}
	if err := validateAdminHeaders(name, op.Headers, authorization != ""); err != nil {
		op.ValidationErrors = append(op.ValidationErrors, err.Error())
	}
	if op.DecodeError == "" {
		if err := validateDTOFields(name, op.JSON, expectedFields); err != nil {
			op.ValidationErrors = append(op.ValidationErrors, err.Error())
		}
	}
	return op
}

func adminSafeHeaders(headers http.Header) map[string]string {
	out := make(map[string]string, 4)
	for _, key := range []string{"Content-Type", "Cache-Control", "X-Content-Type-Options", "X-Frame-Options"} {
		out[key] = headers.Get(key)
	}
	return out
}

func validateAdminHeaders(name string, headers map[string]string, authenticated bool) error {
	if !strings.HasPrefix(headers["Content-Type"], "application/json") {
		return fmt.Errorf("%s: expected JSON content type, got %q", name, headers["Content-Type"])
	}
	if headers["X-Content-Type-Options"] != "nosniff" || headers["X-Frame-Options"] != "DENY" {
		return fmt.Errorf("%s: missing expected security headers", name)
	}
	if authenticated && headers["Cache-Control"] != "no-store" {
		return fmt.Errorf("%s: expected Cache-Control no-store, got %q", name, headers["Cache-Control"])
	}
	return nil
}

func validateDTOFields(name string, payload any, expected []string) error {
	object, ok := payload.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: expected JSON object, got %T", name, payload)
	}
	for _, field := range expected {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("%s: required DTO field %q is absent", name, field)
		}
	}
	if name == "empty-list" {
		if routes, ok := object["routes"].([]any); !ok || len(routes) != 0 {
			return fmt.Errorf("empty-list: expected empty routes array")
		}
	}
	return nil
}

func normalizeAdminOperations(operations []adminOperation) any {
	// This response set has no identifiers, timestamps, or listener addresses.
	// Keep every API-significant value intact rather than applying global text
	// normalizers that could conceal an auth or DTO regression.
	return operations
}

func adminAPIRerunCommand() string {
	return "GORDON_COMPAT_RUN_REAL=1 GORDON_COMPAT_REQUIRE_RUNTIME=1 GORDON_COMPAT_BASELINE_REF=" + BaselineRefFromEnv() + " go test ./internal/testutils/compatoldnew -run '^TestCompatibilityAdminAuthAndRouteCRUD$' -count=1"
}
