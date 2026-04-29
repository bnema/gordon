package acmelego

import (
	"testing"

	"github.com/stretchr/testify/assert"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
)

func TestHTTPProviderStoresAndCleansChallenge(t *testing.T) {
	mockSink := outmocks.NewMockHTTPChallengeSink(t)
	mockSink.EXPECT().Present("token", "key-auth").Return()
	mockSink.EXPECT().CleanUp("token").Return()

	provider := NewHTTPProvider(mockSink)

	err := provider.Present("example.com", "token", "key-auth")
	assert.NoError(t, err)

	err = provider.CleanUp("example.com", "token", "key-auth")
	assert.NoError(t, err)
}

func TestHTTPProviderNilSink(t *testing.T) {
	provider := NewHTTPProvider(nil)

	err := provider.Present("example.com", "token", "key-auth")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sink is nil")

	err = provider.CleanUp("example.com", "token", "key-auth")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sink is nil")
}
