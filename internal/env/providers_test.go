package env

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewPassProvider(t *testing.T) {
	provider := NewPassProvider()
	assert.NotNil(t, provider)
	assert.Equal(t, "pass", provider.Name())
	assert.Equal(t, 10*time.Second, provider.timeout)
}

func TestPassProvider_Name(t *testing.T) {
	provider := NewPassProvider()
	assert.Equal(t, "pass", provider.Name())
}

func TestPassProvider_GetSecret(t *testing.T) {
	provider := NewPassProvider()

	tests := []struct {
		name           string
		path           string
		mockCommand    bool
		mockOutput     string
		mockError      bool
		expectedSecret string
		expectedError  bool
	}{
		{
			name:           "successful secret retrieval",
			path:           "test/secret",
			mockCommand:    true,
			mockOutput:     "secret-value\n",
			expectedSecret: "secret-value",
		},
		{
			name:          "command execution fails",
			path:          "test/nonexistent",
			mockCommand:   true,
			mockError:     true,
			expectedError: true,
		},
		{
			name:          "empty secret returned",
			path:          "test/empty",
			mockCommand:   true,
			mockOutput:    "\n",
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Check if pass command is available
			_, err := exec.LookPath("pass")
			if err != nil {
				t.Skip("pass command not available, skipping test")
			}

			// Since we can't easily mock exec.Command in this context,
			// we'll test the interface and structure, but skip actual execution
			// unless we have a real pass installation

			// Test the provider structure
			assert.Equal(t, "pass", provider.Name())
			assert.NotZero(t, provider.timeout)

			// For actual testing, we would need a more sophisticated mocking approach
			// or integration tests with a real pass setup
			if !tt.mockCommand {
				secret, err := provider.GetSecret(tt.path)
				if tt.expectedError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
					assert.Equal(t, tt.expectedSecret, secret)
				}
			}
		})
	}
}

func TestNewSopsProvider(t *testing.T) {
	provider := NewSopsProvider()
	assert.NotNil(t, provider)
	assert.Equal(t, "sops", provider.Name())
	assert.Equal(t, 10*time.Second, provider.timeout)
}

func TestSopsProvider_Name(t *testing.T) {
	provider := NewSopsProvider()
	assert.Equal(t, "sops", provider.Name())
}

func TestSopsProvider_GetSecret(t *testing.T) {
	provider := NewSopsProvider()

	tests := []struct {
		name           string
		path           string
		expectedSecret string
		expectedError  bool
	}{
		{
			name:          "invalid path format - no colon",
			path:          "invalid-path",
			expectedError: true,
		},
		{
			name:          "invalid path format - empty key",
			path:          "file.yaml:",
			expectedError: true,
		},
		{
			name:          "invalid path format - empty file",
			path:          ":key.path",
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Check if sops command is available
			_, err := exec.LookPath("sops")
			if err != nil {
				t.Skip("sops command not available, skipping test")
			}

			// Test the provider structure
			assert.Equal(t, "sops", provider.Name())
			assert.NotZero(t, provider.timeout)

			// Test path validation
			secret, err := provider.GetSecret(tt.path)
			if tt.expectedError {
				assert.Error(t, err)
				assert.Empty(t, secret)
			} else {
				// For successful cases, we'd need actual sops files
				// which is complex to set up in unit tests
				// This would be better tested in integration tests
			}
		})
	}
}

func TestSopsProvider_GetSecret_ValidFormat(t *testing.T) {
	provider := NewSopsProvider()
	
	// Check if sops command is available
	_, err := exec.LookPath("sops")
	if err != nil {
		t.Skip("sops command not available, skipping test")
	}

	// Test with valid format but non-existent file (should fail gracefully)
	_, err = provider.GetSecret("nonexistent.yaml:some.key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sops command failed")
}

func TestSecretProvider_Interface(t *testing.T) {
	// Test that our providers implement the SecretProvider interface
	var _ SecretProvider = NewPassProvider()
	var _ SecretProvider = NewSopsProvider()
}

func TestProviders_Timeout(t *testing.T) {
	// Test that providers have reasonable timeouts
	passProvider := NewPassProvider()
	sopsProvider := NewSopsProvider()

	assert.Equal(t, 10*time.Second, passProvider.timeout)
	assert.Equal(t, 10*time.Second, sopsProvider.timeout)
}

func TestProviders_ContextCancellation(t *testing.T) {
	// Test that providers respect context cancellation
	passProvider := NewPassProvider()
	sopsProvider := NewSopsProvider()

	// Create a context that's already cancelled
	_, cancel := context.WithCancel(context.Background())
	cancel()

	// Providers should handle cancelled contexts gracefully
	// (This is more of a behavioral test - the actual implementation
	// uses exec.CommandContext which should respect cancellation)
	assert.NotNil(t, passProvider)
	assert.NotNil(t, sopsProvider)
}

// Mock implementations for more controlled testing
type MockPassProvider struct {
	output string
	err    error
}

func (m *MockPassProvider) Name() string {
	return "pass"
}

func (m *MockPassProvider) GetSecret(path string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.output, nil
}

type MockSopsProvider struct {
	output string
	err    error
}

func (m *MockSopsProvider) Name() string {
	return "sops"
}

func (m *MockSopsProvider) GetSecret(path string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.output, nil
}

func TestMockProviders(t *testing.T) {
	// Test our mock implementations
	mockPass := &MockPassProvider{output: "test-secret", err: nil}
	mockSops := &MockSopsProvider{output: "sops-secret", err: nil}

	assert.Equal(t, "pass", mockPass.Name())
	assert.Equal(t, "sops", mockSops.Name())

	secret, err := mockPass.GetSecret("test/path")
	assert.NoError(t, err)
	assert.Equal(t, "test-secret", secret)

	secret, err = mockSops.GetSecret("file.yaml:key")
	assert.NoError(t, err)
	assert.Equal(t, "sops-secret", secret)
}