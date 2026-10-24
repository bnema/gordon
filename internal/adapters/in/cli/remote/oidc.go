package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// OIDC credential source environment.
const (
	envOIDCToken           = "GORDON_OIDC_TOKEN" //nolint:gosec // G101: an environment variable name
	envOIDCAudience        = "GORDON_OIDC_AUDIENCE"
	envActionsRequestURL   = "ACTIONS_ID_TOKEN_REQUEST_URL"
	envActionsRequestToken = "ACTIONS_ID_TOKEN_REQUEST_TOKEN" //nolint:gosec // G101: an environment variable name
)

const (
	idTokenFetchTimeout = 30 * time.Second
	maxIDTokenResponse  = 64 << 10
)

// ErrOIDCAdminUnavailable reports an admin API call in OIDC mode: a workload
// identity only reaches the CI host (registry push, set-image and its
// operation lookup).
var ErrOIDCAdminUnavailable = errors.New("not available with an OIDC identity: the CI host serves only image push, app set-image and operation lookup")

// ErrCIInsecureTransport reports a CI credential (the workload identity or
// a ci-access token) that would leave the client over plaintext or follow a
// redirect. CI credentials only travel on a direct https request to the CI
// host.
var ErrCIInsecureTransport = errors.New("refusing to send CI credentials")

// CheckCIRemote reports whether rawURL may receive CI credentials: it must
// be an absolute https URL. The error never includes credentials.
func CheckCIRemote(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: the CI host URL is not a valid absolute URL", ErrCIInsecureTransport)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w to %s over %s: the CI host must be an https:// URL", ErrCIInsecureTransport, u.Host, u.Scheme)
	}
	return nil
}

// isRedirectStatus reports the statuses net/http would follow.
func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

// ciDo sends a request carrying a CI credential. It refuses any non-https
// destination before dialing and never follows a redirect: a redirect
// answer is an error, so the credential reaches only the CI host itself.
func (c *Client) ciDo(req *http.Request) (*http.Response, error) {
	if err := CheckCIRemote(req.URL.String()); err != nil {
		return nil, err
	}
	noRedirect := *c.httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return nil, err
	}
	if isRedirectStatus(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodySize))
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s %s answered with a redirect (%s); redirects are not followed", ErrCIInsecureTransport, req.Method, req.URL.Path, resp.Status)
	}
	return resp, nil
}

// do sends an admin or CI request: CI credentials go through ciDo.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	if c.oidc != nil {
		return c.ciDo(req)
	}
	return c.httpClient.Do(req)
}

// OIDCSource yields the workload identity token (an OIDC JWT) the client
// exchanges at the CI host. It holds either a static token or the CI
// runner's ID token request endpoint.
type OIDCSource struct {
	static       string
	requestURL   string
	requestToken string
	audience     string
}

// ResolveOIDCSource selects the credential source from the environment:
// GORDON_OIDC_TOKEN, else the runner's ACTIONS_ID_TOKEN_REQUEST_URL and
// ACTIONS_ID_TOKEN_REQUEST_TOKEN with an audience from flagAudience or
// GORDON_OIDC_AUDIENCE (required then). It returns nil when neither is
// set, so the client keeps the stored-token mode.
func ResolveOIDCSource(flagAudience string) (*OIDCSource, error) {
	if token := strings.TrimSpace(os.Getenv(envOIDCToken)); token != "" {
		return &OIDCSource{static: token}, nil
	}
	requestURL := strings.TrimSpace(os.Getenv(envActionsRequestURL))
	requestToken := strings.TrimSpace(os.Getenv(envActionsRequestToken))
	if requestURL == "" || requestToken == "" {
		return nil, nil
	}
	audience := strings.TrimSpace(flagAudience)
	if audience == "" {
		audience = strings.TrimSpace(os.Getenv(envOIDCAudience))
	}
	if audience == "" {
		return nil, fmt.Errorf("the CI runner offers an OIDC ID token (%s is set) but no audience is configured: "+
			"pass --oidc-audience or set %s to the audience of the Gordon trusted issuer", envActionsRequestURL, envOIDCAudience)
	}
	return &OIDCSource{requestURL: requestURL, requestToken: requestToken, audience: audience}, nil
}

// IDToken returns a workload identity token, fetching a fresh one from the
// runner when the source is not static. Errors never include a token.
func (s *OIDCSource) IDToken(ctx context.Context) (string, error) {
	if s.static != "" {
		return s.static, nil
	}
	target, err := url.Parse(s.requestURL)
	if err != nil {
		return "", fmt.Errorf("fetch OIDC ID token: invalid %s", envActionsRequestURL)
	}
	query := target.Query()
	query.Set("audience", s.audience)
	target.RawQuery = query.Encode()

	ctx, cancel := context.WithTimeout(ctx, idTokenFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", fmt.Errorf("fetch OIDC ID token: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+s.requestToken)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		// The transport error embeds the request URL; report only its kind.
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("fetch OIDC ID token: the CI runner did not answer: timed out")
		}
		return "", fmt.Errorf("fetch OIDC ID token: request to the CI runner failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch OIDC ID token: runner answered %s", resp.Status)
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxIDTokenResponse)).Decode(&body); err != nil || body.Value == "" {
		return "", fmt.Errorf("fetch OIDC ID token: runner returned no token")
	}
	return body.Value, nil
}

// WithOIDC switches the client to OIDC mode: it exchanges the workload
// identity at <remote>/auth/token and uses the resulting ci-access token for
// the CI host routes. Admin API calls fail with ErrOIDCAdminUnavailable.
func WithOIDC(source *OIDCSource) ClientOption {
	return func(c *Client) {
		c.oidc = source
	}
}

// IsOIDC reports whether the client authenticates with a workload identity.
func (c *Client) IsOIDC() bool { return c.oidc != nil }

// CIRegistryHost returns the registry host to push to in OIDC mode: the CI
// host itself.
func (c *Client) CIRegistryHost() (string, bool) {
	if c.oidc == nil {
		return "", false
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || u.Host == "" {
		return "", false
	}
	return u.Host, true
}

// cachedCIToken is one ci-access token for one requested scope set.
type cachedCIToken struct {
	token   string
	expires time.Time
}

// ExchangeCIToken returns a ci-access token for scopes (none for set-image
// and lookup), exchanging the workload identity at the CI token endpoint
// when no valid cached token exists.
func (c *Client) ExchangeCIToken(ctx context.Context, scopes ...string) (string, error) {
	if c.oidc == nil {
		return "", errors.New("ci-access token exchange requires an OIDC identity")
	}
	// Refuse a plaintext CI host before fetching the identity token.
	if err := CheckCIRemote(c.baseURL); err != nil {
		return "", err
	}
	key := strings.Join(scopes, " ")
	c.ciMu.Lock()
	defer c.ciMu.Unlock()
	if cached, ok := c.ciTokens[key]; ok && time.Now().Before(cached.expires.Add(-ephemeralMargin)) {
		return cached.token, nil
	}
	token, ttl, err := c.exchangeCIToken(ctx, scopes)
	if err != nil {
		return "", err
	}
	if c.ciTokens == nil {
		c.ciTokens = map[string]cachedCIToken{}
	}
	c.ciTokens[key] = cachedCIToken{token: token, expires: time.Now().Add(ttl)}
	return token, nil
}

// ResetCIToken drops the cached ci-access tokens, forcing a new exchange.
func (c *Client) ResetCIToken() {
	c.ciMu.Lock()
	c.ciTokens = nil
	c.ciMu.Unlock()
}

func (c *Client) exchangeCIToken(ctx context.Context, scopes []string) (string, time.Duration, error) {
	jwt, err := c.oidc.IDToken(ctx)
	if err != nil {
		return "", 0, err
	}
	target, err := url.Parse(c.baseURL + "/auth/token")
	if err != nil {
		return "", 0, fmt.Errorf("ci-access token exchange: %w", err)
	}
	query := url.Values{"service": {target.Host}}
	for _, scope := range scopes {
		query.Add("scope", scope)
	}
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", 0, fmt.Errorf("ci-access token exchange: %w", err)
	}
	req.SetBasicAuth("oidc", jwt)
	req.Header.Set("Accept", "application/json")
	resp, err := c.ciDo(req)
	if err != nil {
		return "", 0, fmt.Errorf("ci-access token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		return "", 0, fmt.Errorf("ci-access token exchange: %w", parseErrorResponse(resp, body))
	}
	var result dto.TokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxIDTokenResponse)).Decode(&result); err != nil {
		return "", 0, fmt.Errorf("ci-access token exchange: decode response")
	}
	token := result.Token
	if token == "" {
		token = result.AccessToken
	}
	if token == "" || result.ExpiresIn <= 0 {
		return "", 0, errors.New("ci-access token exchange returned no usable token")
	}
	return token, time.Duration(result.ExpiresIn) * time.Second, nil
}

// adminURL maps an admin API path to its endpoint. In OIDC mode only the
// set-image and its operation lookup exist, on the CI host routes; every
// other admin call fails without any network request.
func (c *Client) adminURL(method, path string) (string, error) {
	if c.oidc == nil {
		return c.baseURL + "/admin" + path, nil
	}
	if ciPath, ok := ciRoute(method, path); ok {
		return c.baseURL + ciPath, nil
	}
	return "", fmt.Errorf("%s %s: %w", method, path, ErrOIDCAdminUnavailable)
}

// ciRoute maps the admin set-image and by-key routes to the CI host routes.
func ciRoute(method, path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/apps/")
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, "/")
	switch {
	case method == http.MethodPost && len(parts) == 2 && parts[1] == "set-image":
		return "/-/ci" + path, true
	case method == http.MethodGet && len(parts) == 4 && parts[1] == "operations" && parts[2] == "by-key":
		return "/-/ci" + path, true
	default:
		return "", false
	}
}
