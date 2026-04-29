// Package acmelego implements ACME certificate operations using the lego library.
package acmelego

// HTTPChallengeSink defines the storage interface for HTTP-01 challenge
// tokens. The usecase layer implements this to serve challenge responses
// via the proxy.
type HTTPChallengeSink interface {
	Present(token, keyAuth string)
	CleanUp(token string)
}

// HTTPProvider adapts an HTTPChallengeSink to the lego challenge.Provider
// interface for HTTP-01 challenge solving.
type HTTPProvider struct {
	sink HTTPChallengeSink
}

// NewHTTPProvider creates a new HTTPProvider.
func NewHTTPProvider(sink HTTPChallengeSink) *HTTPProvider {
	return &HTTPProvider{sink: sink}
}

// Present stores the key authorization for the given token via the sink.
// If the sink is nil, it returns nil (no-op) to allow graceful degradation
// in test or misconfigured scenarios.
func (p *HTTPProvider) Present(_domain, token, keyAuth string) error {
	if p.sink == nil {
		return nil
	}
	p.sink.Present(token, keyAuth)
	return nil
}

// CleanUp removes the key authorization for the given token via the sink.
func (p *HTTPProvider) CleanUp(_domain, token, _keyAuth string) error {
	if p.sink == nil {
		return nil
	}
	p.sink.CleanUp(token)
	return nil
}
