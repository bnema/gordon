package remote

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/bnema/gordon/internal/domain"
)

// ListPreviews returns all active preview environments.
func (c *Client) ListPreviews(ctx context.Context) ([]domain.PreviewRoute, error) {
	resp, err := c.request(ctx, http.MethodGet, "/previews", nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Previews []domain.PreviewRoute `json:"previews"`
	}
	if err := parseResponse(resp, &result); err != nil {
		return nil, err
	}

	return result.Previews, nil
}

// DeletePreview tears down a preview environment by name.
func (c *Client) DeletePreview(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("preview name cannot be empty")
	}
	resp, err := c.request(ctx, http.MethodDelete, "/preview/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}

// ExtendPreview extends the TTL of a preview environment.
func (c *Client) ExtendPreview(ctx context.Context, name string, ttl string) error {
	if name == "" {
		return fmt.Errorf("preview name cannot be empty")
	}
	if ttl == "" {
		return fmt.Errorf("ttl cannot be empty")
	}
	resp, err := c.request(ctx, http.MethodPatch, "/preview/"+url.PathEscape(name), struct {
		TTL string `json:"ttl"`
	}{TTL: ttl})
	if err != nil {
		return err
	}
	return parseResponse(resp, nil)
}
