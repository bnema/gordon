package remote

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
	gordon "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

const (
	defaultDialTimeout = 15 * time.Second
	maxLogLines        = 10000
)

// Client is a gRPC client for the Gordon admin API.
type Client struct {
	addr    string
	token   string
	timeout time.Duration

	mu    sync.Mutex
	conn  *grpc.ClientConn
	admin gordon.AdminServiceClient
}

// ClientOption configures the Client.
type ClientOption func(*Client)

// NewClient creates a new remote Gordon client.
func NewClient(baseURL string, opts ...ClientOption) *Client {
	addr, err := normalizeAddr(baseURL)
	if err != nil {
		addr = baseURL
	}

	c := &Client{
		addr:    addr,
		timeout: defaultDialTimeout,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// WithToken sets the authentication token.
func WithToken(token string) ClientOption {
	return func(c *Client) {
		c.token = token
	}
}

// WithTimeout sets the dial timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Routes API

// Type aliases for API types using shared DTO package.
type RouteInfo = dto.RouteInfo
type Attachment = dto.Attachment

// ListRoutes returns all configured routes.
func (c *Client) ListRoutes(ctx context.Context) ([]domain.Route, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.ListRoutes(c.ctxWithAuth(ctx), &gordon.ListRoutesRequest{Detailed: false})
	if err != nil {
		return nil, err
	}

	routes := make([]domain.Route, 0, len(resp.Routes))
	for _, route := range resp.Routes {
		if route == nil {
			continue
		}
		routes = append(routes, domain.Route{Domain: route.Domain, Image: route.Image, HTTPS: route.Https})
	}

	return routes, nil
}

// ListRoutesWithDetails returns routes with network and attachment info.
func (c *Client) ListRoutesWithDetails(ctx context.Context) ([]RouteInfo, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.ListRoutes(c.ctxWithAuth(ctx), &gordon.ListRoutesRequest{Detailed: true})
	if err != nil {
		return nil, err
	}

	results := make([]RouteInfo, 0, len(resp.RouteInfos))
	for _, info := range resp.RouteInfos {
		if info == nil {
			continue
		}
		results = append(results, toRouteInfo(info))
	}

	return results, nil
}

// ListNetworks returns Gordon-managed networks.
func (c *Client) ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.ListNetworks(c.ctxWithAuth(ctx), &gordon.ListNetworksRequest{})
	if err != nil {
		return nil, err
	}

	networks := make([]*domain.NetworkInfo, 0, len(resp.Networks))
	for _, network := range resp.Networks {
		if network == nil {
			continue
		}
		networks = append(networks, &domain.NetworkInfo{
			Name:   network.Name,
			Driver: network.Driver,
		})
	}

	return networks, nil
}

// ListAttachments returns attachments for a domain.
func (c *Client) ListAttachments(ctx context.Context, routeDomain string) ([]Attachment, error) {
	if routeDomain == "" {
		return nil, fmt.Errorf("domain is required")
	}

	infos, err := c.ListRoutesWithDetails(ctx)
	if err != nil {
		return nil, err
	}

	for _, info := range infos {
		if info.Domain == routeDomain {
			return info.Attachments, nil
		}
	}

	return nil, fmt.Errorf("route not found")
}

// GetRoute returns a specific route by domain.
func (c *Client) GetRoute(ctx context.Context, routeDomain string) (*domain.Route, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.GetRoute(c.ctxWithAuth(ctx), &gordon.GetRouteRequest{Domain: routeDomain})
	if err != nil {
		return nil, err
	}

	if resp.Route == nil {
		return nil, fmt.Errorf("route not found")
	}

	return &domain.Route{Domain: resp.Route.Domain, Image: resp.Route.Image, HTTPS: resp.Route.Https}, nil
}

// AddRoute adds a new route.
func (c *Client) AddRoute(ctx context.Context, route domain.Route) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	_, err := c.admin.AddRoute(c.ctxWithAuth(ctx), &gordon.AddRouteRequest{
		Route: &gordon.AdminRoute{Domain: route.Domain, Image: route.Image, Https: route.HTTPS},
	})
	return err
}

// UpdateRoute updates an existing route.
func (c *Client) UpdateRoute(ctx context.Context, route domain.Route) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	_, err := c.admin.UpdateRoute(c.ctxWithAuth(ctx), &gordon.UpdateRouteRequest{
		Domain: route.Domain,
		Route:  &gordon.AdminRoute{Domain: route.Domain, Image: route.Image, Https: route.HTTPS},
	})
	return err
}

// RemoveRoute removes a route.
func (c *Client) RemoveRoute(ctx context.Context, routeDomain string) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	_, err := c.admin.RemoveRoute(c.ctxWithAuth(ctx), &gordon.RemoveRouteRequest{Domain: routeDomain})
	return err
}

// Secrets API

// AttachmentSecrets represents secrets for an attachment container.
type AttachmentSecrets struct {
	Service string   `json:"service"`
	Keys    []string `json:"keys"`
}

// SecretsListResult contains domain secrets and any attachment secrets.
type SecretsListResult struct {
	Domain      string              `json:"domain"`
	Keys        []string            `json:"keys"`
	Attachments []AttachmentSecrets `json:"attachments,omitempty"`
}

// ListSecrets returns the list of secret keys for a domain.
func (c *Client) ListSecrets(ctx context.Context, secretDomain string) ([]string, error) {
	result, err := c.ListSecretsWithAttachments(ctx, secretDomain)
	if err != nil {
		return nil, err
	}
	return result.Keys, nil
}

// ListSecretsWithAttachments returns domain secrets and attachment secrets.
func (c *Client) ListSecretsWithAttachments(ctx context.Context, secretDomain string) (*SecretsListResult, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.ListSecrets(c.ctxWithAuth(ctx), &gordon.ListSecretsRequest{Domain: secretDomain})
	if err != nil {
		return nil, err
	}

	result := &SecretsListResult{
		Domain: resp.Domain,
		Keys:   append([]string{}, resp.Keys...),
	}

	for _, attachment := range resp.Attachments {
		if attachment == nil {
			continue
		}
		result.Attachments = append(result.Attachments, AttachmentSecrets{
			Service: attachment.Service,
			Keys:    append([]string{}, attachment.Keys...),
		})
	}

	return result, nil
}

// SetSecrets sets secrets for a domain.
func (c *Client) SetSecrets(ctx context.Context, secretDomain string, secrets map[string]string) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	_, err := c.admin.SetSecrets(c.ctxWithAuth(ctx), &gordon.SetSecretsRequest{
		Domain:  secretDomain,
		Secrets: secrets,
	})
	return err
}

// DeleteSecret removes a secret key for a domain.
func (c *Client) DeleteSecret(ctx context.Context, secretDomain, key string) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	_, err := c.admin.DeleteSecret(c.ctxWithAuth(ctx), &gordon.DeleteSecretRequest{Domain: secretDomain, Key: key})
	return err
}

// Status API

// Status represents the Gordon server status.
type Status struct {
	Routes           int               `json:"routes"`
	RegistryDomain   string            `json:"registry_domain"`
	RegistryPort     int               `json:"registry_port"`
	ServerPort       int               `json:"server_port"`
	AutoRoute        bool              `json:"auto_route"`
	NetworkIsolation bool              `json:"network_isolation"`
	ContainerStatus  map[string]string `json:"container_status"`
}

// GetStatus returns the Gordon server status.
func (c *Client) GetStatus(ctx context.Context) (*Status, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.GetStatus(c.ctxWithAuth(ctx), &gordon.GetStatusRequest{})
	if err != nil {
		return nil, err
	}

	return &Status{
		Routes:           int(resp.RouteCount),
		RegistryDomain:   resp.RegistryDomain,
		RegistryPort:     int(resp.RegistryPort),
		ServerPort:       int(resp.ServerPort),
		AutoRoute:        resp.AutoRoute,
		NetworkIsolation: resp.NetworkIsolation,
		ContainerStatus:  resp.ContainerStatuses,
	}, nil
}

// RouteHealth represents the health status of a route.
type RouteHealth struct {
	ContainerStatus string `json:"container_status"`
	HTTPStatus      int    `json:"http_status"`
	ResponseTimeMs  int64  `json:"response_time_ms"`
	Healthy         bool   `json:"healthy"`
	Error           string `json:"error"`
}

// GetHealth returns health status for all routes.
func (c *Client) GetHealth(ctx context.Context) (map[string]*RouteHealth, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.GetHealth(c.ctxWithAuth(ctx), &gordon.GetHealthRequest{})
	if err != nil {
		return nil, err
	}

	result := make(map[string]*RouteHealth, len(resp.Routes))
	for domainName, status := range resp.Routes {
		if status == nil {
			continue
		}
		result[domainName] = &RouteHealth{
			Healthy:        status.Healthy,
			ResponseTimeMs: int64(status.ResponseTimeMs),
			Error:          status.Error,
		}
	}

	return result, nil
}

// Reload triggers a configuration reload.
func (c *Client) Reload(ctx context.Context) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	_, err := c.admin.Reload(c.ctxWithAuth(ctx), &gordon.ReloadRequest{})
	return err
}

// Config API

// Config represents the Gordon configuration.
type Config struct {
	Server struct {
		Port           int    `json:"port"`
		RegistryPort   int    `json:"registry_port"`
		RegistryDomain string `json:"registry_domain"`
		DataDir        string `json:"data_dir"`
	} `json:"server"`
	AutoRoute struct {
		Enabled bool `json:"enabled"`
	} `json:"auto_route"`
	NetworkIsolation struct {
		Enabled bool   `json:"enabled"`
		Prefix  string `json:"prefix"`
	} `json:"network_isolation"`
	Routes         []domain.Route    `json:"routes"`
	ExternalRoutes map[string]string `json:"external_routes"`
}

// GetConfig returns the Gordon configuration.
func (c *Client) GetConfig(ctx context.Context) (*Config, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.GetConfig(c.ctxWithAuth(ctx), &gordon.GetConfigRequest{})
	if err != nil {
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(resp.ConfigJson, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &config, nil
}

// Ping checks if the remote Gordon instance is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.GetStatus(ctx)
	return err
}

// Deploy API

// DeployResult contains the result of a deployment.
type DeployResult struct {
	Status      string `json:"status"`
	ContainerID string `json:"container_id"`
	Domain      string `json:"domain"`
}

// Deploy triggers a deployment for the specified domain.
func (c *Client) Deploy(ctx context.Context, deployDomain string) (*DeployResult, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.Deploy(c.ctxWithAuth(ctx), &gordon.DeployRequest{Domain: deployDomain})
	if err != nil {
		return nil, err
	}

	status := "failed"
	if resp.Success {
		status = "deployed"
	}

	return &DeployResult{Status: status, ContainerID: resp.ContainerId, Domain: deployDomain}, nil
}

// Logs API

// GetProcessLogs returns the last N process log lines.
func (c *Client) GetProcessLogs(ctx context.Context, lines int) ([]string, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	stream, err := c.admin.GetProcessLogs(c.ctxWithAuth(ctx), &gordon.GetProcessLogsRequest{Lines: toLogLines(lines), Follow: false})
	if err != nil {
		return nil, err
	}

	return readLogStream(stream)
}

// GetContainerLogs returns the last N container log lines for a domain.
func (c *Client) GetContainerLogs(ctx context.Context, logDomain string, lines int) ([]string, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	stream, err := c.admin.GetContainerLogs(c.ctxWithAuth(ctx), &gordon.GetContainerLogsRequest{Domain: logDomain, Lines: toLogLines(lines), Follow: false})
	if err != nil {
		return nil, err
	}

	return readContainerLogStream(stream)
}

// StreamProcessLogs streams process log lines.
func (c *Client) StreamProcessLogs(ctx context.Context, lines int) (<-chan string, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	stream, err := c.admin.GetProcessLogs(c.ctxWithAuth(ctx), &gordon.GetProcessLogsRequest{Lines: toLogLines(lines), Follow: true})
	if err != nil {
		return nil, err
	}

	return streamLogChannel(ctx, stream)
}

// StreamContainerLogs streams container log lines.
func (c *Client) StreamContainerLogs(ctx context.Context, logDomain string, lines int) (<-chan string, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	stream, err := c.admin.GetContainerLogs(c.ctxWithAuth(ctx), &gordon.GetContainerLogsRequest{Domain: logDomain, Lines: toLogLines(lines), Follow: true})
	if err != nil {
		return nil, err
	}

	return streamContainerLogChannel(ctx, stream)
}

// Attachments config API

// GetAllAttachmentsConfig returns all configured attachments.
func (c *Client) GetAllAttachmentsConfig(ctx context.Context) (map[string][]string, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.GetAttachments(c.ctxWithAuth(ctx), &gordon.GetAttachmentsRequest{})
	if err != nil {
		return nil, err
	}

	result := make(map[string][]string)
	for _, attachment := range resp.Attachments {
		if attachment == nil {
			continue
		}
		result[attachment.Name] = append(result[attachment.Name], attachment.Image)
	}

	return result, nil
}

// GetAttachmentsConfig returns attachments for a target.
func (c *Client) GetAttachmentsConfig(ctx context.Context, domainOrGroup string) ([]string, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.GetAttachments(c.ctxWithAuth(ctx), &gordon.GetAttachmentsRequest{Target: domainOrGroup})
	if err != nil {
		return nil, err
	}

	images := make([]string, 0, len(resp.Attachments))
	for _, attachment := range resp.Attachments {
		if attachment == nil {
			continue
		}
		images = append(images, attachment.Image)
	}

	return images, nil
}

// AddAttachment adds an attachment to a target.
func (c *Client) AddAttachment(ctx context.Context, domainOrGroup, image string) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	_, err := c.admin.AddAttachment(c.ctxWithAuth(ctx), &gordon.AddAttachmentRequest{Target: domainOrGroup, Image: image})
	return err
}

// RemoveAttachment removes an attachment from a target.
func (c *Client) RemoveAttachment(ctx context.Context, domainOrGroup, image string) error {
	if err := c.ensureConn(ctx); err != nil {
		return err
	}

	_, err := c.admin.RemoveAttachment(c.ctxWithAuth(ctx), &gordon.RemoveAttachmentRequest{Target: domainOrGroup, Image: image})
	return err
}

// VerifyAuth checks if authentication is valid.
func (c *Client) VerifyAuth(ctx context.Context) (*dto.AuthVerifyResponse, error) {
	if err := c.ensureConn(ctx); err != nil {
		return nil, err
	}

	resp, err := c.admin.VerifyAuth(c.ctxWithAuth(ctx), &gordon.VerifyAuthRequest{})
	if err != nil {
		return nil, err
	}

	result := &dto.AuthVerifyResponse{
		Valid:   resp.Valid,
		Subject: resp.Subject,
		Scopes:  append([]string{}, resp.Scopes...),
	}
	if resp.ExpiresAt != nil {
		result.ExpiresAt = resp.ExpiresAt.AsTime().Unix()
	}

	return result, nil
}

func (c *Client) ctxWithAuth(ctx context.Context) context.Context {
	if c.token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token)
}

func (c *Client) ensureConn(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil && c.admin != nil {
		return nil
	}

	dialCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", c.addr, err)
	}

	if c.timeout > 0 {
		state := conn.GetState()
		if state == connectivity.Idle {
			if !conn.WaitForStateChange(dialCtx, state) {
				_ = conn.Close()
				return fmt.Errorf("connection timeout: %w", dialCtx.Err())
			}
		}
	}

	c.conn = conn
	c.admin = gordon.NewAdminServiceClient(conn)
	return nil
}

func normalizeAddr(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("missing address")
	}

	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		if parsed.Scheme != "" && parsed.Scheme != "https" && parsed.Scheme != "grpcs" {
			return "", fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
		}
		host := parsed.Host
		if host == "" {
			return "", errors.New("invalid address")
		}
		return ensurePort(host, parsed.Port())
	}

	parsed, err := url.Parse("https://" + raw)
	if err != nil {
		return "", err
	}

	return ensurePort(parsed.Host, parsed.Port())
}

func ensurePort(host, port string) (string, error) {
	if host == "" {
		return "", errors.New("invalid address")
	}

	if port != "" {
		return host, nil
	}

	if _, _, err := net.SplitHostPort(host); err == nil {
		return host, nil
	}

	if strings.Contains(host, ":") {
		return net.JoinHostPort(host, "443"), nil
	}

	return host + ":443", nil
}

func toRouteInfo(info *gordon.RouteInfo) RouteInfo {
	attachments := make([]Attachment, 0, len(info.Attachments))
	for _, attachment := range info.Attachments {
		if attachment == nil {
			continue
		}
		attachments = append(attachments, Attachment{
			Name:        attachment.Name,
			Image:       attachment.Image,
			ContainerID: attachment.ContainerId,
			Status:      attachment.Status,
			Network:     attachment.Network,
		})
	}

	return RouteInfo{
		Domain:          info.Domain,
		Image:           info.Image,
		ContainerID:     info.ContainerId,
		ContainerStatus: info.ContainerStatus,
		Network:         info.Network,
		Attachments:     attachments,
	}
}

func readLogStream(stream interface {
	Recv() (*gordon.GetProcessLogsResponse, error)
}) ([]string, error) {
	lines := make([]string, 0)
	for {
		resp, err := stream.Recv()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				return lines, nil
			}
			return nil, err
		}
		if resp != nil && resp.Entry != nil {
			lines = append(lines, resp.Entry.Line)
		}
	}
}

func streamLogChannel(ctx context.Context, stream interface {
	Recv() (*gordon.GetProcessLogsResponse, error)
}) (<-chan string, error) {
	out := make(chan string)

	go func() {
		defer close(out)
		for {
			resp, err := stream.Recv()
			if err != nil {
				return
			}
			if resp == nil || resp.Entry == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- resp.Entry.Line:
			}
		}
	}()
	return out, nil
}

func readContainerLogStream(stream interface {
	Recv() (*gordon.GetContainerLogsResponse, error)
}) ([]string, error) {
	lines := make([]string, 0)
	for {
		resp, err := stream.Recv()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				return lines, nil
			}
			return nil, err
		}
		if resp != nil && resp.Entry != nil {
			lines = append(lines, resp.Entry.Line)
		}
	}
}

func streamContainerLogChannel(ctx context.Context, stream interface {
	Recv() (*gordon.GetContainerLogsResponse, error)
}) (<-chan string, error) {
	out := make(chan string)

	go func() {
		defer close(out)
		for {
			resp, err := stream.Recv()
			if err != nil {
				return
			}
			if resp == nil || resp.Entry == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- resp.Entry.Line:
			}
		}
	}()
	return out, nil
}

func toLogLines(lines int) int32 {
	if lines <= 0 {
		return 50
	}
	if lines > maxLogLines {
		return maxLogLines
	}
	if lines > 2147483647 {
		return 2147483647
	}
	return int32(lines)
}
