package acmelego

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// challengeSink is a test sink that stores challenge tokens in a map.
type challengeSink struct {
	values map[string]string
}

func (s *challengeSink) Present(token, keyAuth string) {
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[token] = keyAuth
}

func (s *challengeSink) CleanUp(token string) {
	delete(s.values, token)
}

func TestHTTPProviderStoresAndCleansChallenge(t *testing.T) {
	sink := &challengeSink{}
	provider := NewHTTPProvider(sink)

	err := provider.Present("example.com", "token", "key-auth")
	assert.NoError(t, err)
	assert.Equal(t, "key-auth", sink.values["token"])

	err = provider.CleanUp("example.com", "token", "key-auth")
	assert.NoError(t, err)
	assert.Empty(t, sink.values["token"])
}

func TestHTTPProviderNilSink(t *testing.T) {
	provider := NewHTTPProvider(nil)

	err := provider.Present("example.com", "token", "key-auth")
	assert.NoError(t, err)

	err = provider.CleanUp("example.com", "token", "key-auth")
	assert.NoError(t, err)
}
