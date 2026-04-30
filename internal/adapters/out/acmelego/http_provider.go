// Package acmelego implements ACME certificate operations using the lego library.
package acmelego

import (
	"fmt"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// HTTPProvider adapts an out.HTTPChallengeSink to the lego challenge.Provider
// interface for HTTP-01 challenge solving.
type HTTPProvider struct {
	sink out.HTTPChallengeSink
}

// NewHTTPProvider creates a new HTTPProvider.
func NewHTTPProvider(sink out.HTTPChallengeSink) *HTTPProvider {
	return &HTTPProvider{sink: sink}
}

// Present stores the key authorization for the given token via the sink.
func (p *HTTPProvider) Present(_domain, token, keyAuth string) error {
	if p.sink == nil {
		return fmt.Errorf("http-01 sink is nil: cannot Present")
	}
	if err := p.sink.Present(token, keyAuth); err != nil {
		return fmt.Errorf("present http-01 challenge: %w", err)
	}
	return nil
}

// CleanUp removes the key authorization for the given token via the sink.
func (p *HTTPProvider) CleanUp(_domain, token, _keyAuth string) error {
	if p.sink == nil {
		return fmt.Errorf("http-01 sink is nil: cannot CleanUp")
	}
	if err := p.sink.CleanUp(token); err != nil {
		return fmt.Errorf("cleanup http-01 challenge: %w", err)
	}
	return nil
}
