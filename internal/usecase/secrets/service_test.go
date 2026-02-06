package secrets

import (
	"context"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestValidateDomain(t *testing.T) {
	tests := []struct {
		name    string
		domain  string
		wantErr error
	}{
		// Valid domains
		{
			name:    "valid simple domain",
			domain:  "example.com",
			wantErr: nil,
		},
		{
			name:    "valid subdomain",
			domain:  "app.example.com",
			wantErr: nil,
		},
		{
			name:    "valid domain with port",
			domain:  "app.example.com:8080",
			wantErr: nil,
		},
		{
			name:    "valid domain with hyphens",
			domain:  "my-app.example-site.com",
			wantErr: nil,
		},
		{
			name:    "valid single label",
			domain:  "localhost",
			wantErr: nil,
		},
		// Empty domain
		{
			name:    "empty domain",
			domain:  "",
			wantErr: ErrDomainEmpty,
		},
		// Path traversal attempts
		{
			name:    "path traversal with double dots",
			domain:  "../etc/passwd",
			wantErr: ErrDomainPathTraversal,
		},
		{
			name:    "path traversal in middle",
			domain:  "app/../etc/passwd",
			wantErr: ErrDomainPathTraversal,
		},
		{
			name:    "path traversal at end",
			domain:  "app.example.com/..",
			wantErr: ErrDomainPathTraversal,
		},
		{
			name:    "multiple path traversal",
			domain:  "..../....//etc/passwd",
			wantErr: ErrDomainPathTraversal,
		},
		{
			name:    "encoded path traversal still blocked",
			domain:  "app..example.com",
			wantErr: ErrDomainPathTraversal,
		},
		// Domain too long
		{
			name:    "domain at max length (253)",
			domain:  strings.Repeat("a", 253),
			wantErr: nil,
		},
		{
			name:    "domain exceeds max length",
			domain:  strings.Repeat("a", 254),
			wantErr: ErrDomainTooLong,
		},
		{
			name:    "domain way too long",
			domain:  strings.Repeat("a", 1000),
			wantErr: ErrDomainTooLong,
		},
		// Invalid characters
		{
			name:    "null byte in domain",
			domain:  "app\x00.example.com",
			wantErr: ErrDomainInvalidChars,
		},
		{
			name:    "null byte at start",
			domain:  "\x00example.com",
			wantErr: ErrDomainInvalidChars,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDomain(tt.domain)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestService_ListKeys_ValidationErrors(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	tests := []struct {
		name    string
		domain  string
		wantErr error
	}{
		{
			name:    "empty domain",
			domain:  "",
			wantErr: ErrDomainEmpty,
		},
		{
			name:    "path traversal",
			domain:  "../etc/passwd",
			wantErr: ErrDomainPathTraversal,
		},
		{
			name:    "domain too long",
			domain:  strings.Repeat("a", 300),
			wantErr: ErrDomainTooLong,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := svc.ListKeys(context.Background(), tt.domain)
			assert.Nil(t, keys)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestService_ListKeys_Success(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	expectedKeys := []string{"API_KEY", "DB_PASSWORD"}
	store.EXPECT().ListKeys("app.example.com").Return(expectedKeys, nil)

	keys, err := svc.ListKeys(context.Background(), "app.example.com")
	assert.NoError(t, err)
	assert.Equal(t, expectedKeys, keys)
}

func TestService_ListKeysWithAttachments_ValidationErrors(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	tests := []struct {
		name    string
		domain  string
		wantErr error
	}{
		{
			name:    "empty domain",
			domain:  "",
			wantErr: ErrDomainEmpty,
		},
		{
			name:    "path traversal",
			domain:  "../etc/passwd",
			wantErr: ErrDomainPathTraversal,
		},
		{
			name:    "domain too long",
			domain:  strings.Repeat("a", 300),
			wantErr: ErrDomainTooLong,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, attachments, err := svc.ListKeysWithAttachments(context.Background(), tt.domain)
			assert.Nil(t, keys)
			assert.Nil(t, attachments)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestService_ListKeysWithAttachments_Success(t *testing.T) {
	t.Run("returns both domain and attachment secrets", func(t *testing.T) {
		store := outmocks.NewMockDomainSecretStore(t)
		svc := NewService(store, testLogger())

		expectedKeys := []string{"API_KEY", "DB_PASSWORD"}
		expectedAttachments := []out.AttachmentSecrets{
			{Service: "postgres", Keys: []string{"POSTGRES_PASSWORD"}},
			{Service: "redis", Keys: []string{"REDIS_PASSWORD", "REDIS_USER"}},
		}

		store.EXPECT().ListKeys("app.example.com").Return(expectedKeys, nil)
		store.EXPECT().ListAttachmentKeys("app.example.com").Return(expectedAttachments, nil)

		keys, attachments, err := svc.ListKeysWithAttachments(context.Background(), "app.example.com")
		assert.NoError(t, err)
		assert.Equal(t, expectedKeys, keys)
		assert.Equal(t, expectedAttachments, attachments)
	})

	t.Run("returns empty attachments when none exist", func(t *testing.T) {
		store := outmocks.NewMockDomainSecretStore(t)
		svc := NewService(store, testLogger())

		expectedKeys := []string{"API_KEY"}

		store.EXPECT().ListKeys("app.example.com").Return(expectedKeys, nil)
		store.EXPECT().ListAttachmentKeys("app.example.com").Return(nil, nil)

		keys, attachments, err := svc.ListKeysWithAttachments(context.Background(), "app.example.com")
		assert.NoError(t, err)
		assert.Equal(t, expectedKeys, keys)
		assert.Nil(t, attachments)
	})

	t.Run("continues without attachments on attachment error", func(t *testing.T) {
		store := outmocks.NewMockDomainSecretStore(t)
		svc := NewService(store, testLogger())

		expectedKeys := []string{"API_KEY"}
		attachmentErr := assert.AnError

		store.EXPECT().ListKeys("app.example.com").Return(expectedKeys, nil)
		store.EXPECT().ListAttachmentKeys("app.example.com").Return(nil, attachmentErr)

		// Should still succeed, just without attachments
		keys, attachments, err := svc.ListKeysWithAttachments(context.Background(), "app.example.com")
		assert.NoError(t, err)
		assert.Equal(t, expectedKeys, keys)
		assert.Nil(t, attachments)
	})
}

func TestService_ListKeysWithAttachments_StoreError(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	storeErr := assert.AnError
	store.EXPECT().ListKeys("app.example.com").Return(nil, storeErr)

	keys, attachments, err := svc.ListKeysWithAttachments(context.Background(), "app.example.com")
	assert.Nil(t, keys)
	assert.Nil(t, attachments)
	assert.ErrorIs(t, err, storeErr)
}

func TestService_GetAll_ValidationErrors(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	tests := []struct {
		name    string
		domain  string
		wantErr error
	}{
		{
			name:    "empty domain",
			domain:  "",
			wantErr: ErrDomainEmpty,
		},
		{
			name:    "path traversal",
			domain:  "app/../secret",
			wantErr: ErrDomainPathTraversal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secrets, err := svc.GetAll(context.Background(), tt.domain)
			assert.Nil(t, secrets)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestService_GetAll_Success(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	expectedSecrets := map[string]string{"API_KEY": "secret123", "DB_PASSWORD": "pass456"}
	store.EXPECT().GetAll("app.example.com").Return(expectedSecrets, nil)

	secrets, err := svc.GetAll(context.Background(), "app.example.com")
	assert.NoError(t, err)
	assert.Equal(t, expectedSecrets, secrets)
}

func TestService_Set_ValidationErrors(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	secrets := map[string]string{"API_KEY": "secret123"}

	tests := []struct {
		name    string
		domain  string
		wantErr error
	}{
		{
			name:    "empty domain",
			domain:  "",
			wantErr: ErrDomainEmpty,
		},
		{
			name:    "path traversal",
			domain:  "../../../etc/passwd",
			wantErr: ErrDomainPathTraversal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.Set(context.Background(), tt.domain, secrets)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestService_Set_Success(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	secrets := map[string]string{"API_KEY": "secret123"}
	store.EXPECT().Set("app.example.com", secrets).Return(nil)

	err := svc.Set(context.Background(), "app.example.com", secrets)
	assert.NoError(t, err)
}

func TestService_Delete_ValidationErrors(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	tests := []struct {
		name    string
		domain  string
		wantErr error
	}{
		{
			name:    "empty domain",
			domain:  "",
			wantErr: ErrDomainEmpty,
		},
		{
			name:    "path traversal",
			domain:  "..%2F..%2Fetc%2Fpasswd",
			wantErr: ErrDomainPathTraversal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.Delete(context.Background(), tt.domain, "API_KEY")
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestService_Delete_Success(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	store.EXPECT().Delete("app.example.com", "API_KEY").Return(nil)

	err := svc.Delete(context.Background(), "app.example.com", "API_KEY")
	assert.NoError(t, err)
}

func TestService_StoreError_PropagatedCorrectly(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	storeErr := assert.AnError
	store.EXPECT().ListKeys("app.example.com").Return(nil, storeErr)

	keys, err := svc.ListKeys(context.Background(), "app.example.com")
	assert.Nil(t, keys)
	assert.ErrorIs(t, err, storeErr)
}

// TestPathTraversalVariants tests various path traversal attack patterns.
func TestPathTraversalVariants(t *testing.T) {
	tests := []struct {
		name   string
		domain string
	}{
		{"simple parent dir", ".."},
		{"parent dir prefix", "../secret"},
		{"parent dir suffix", "secret/.."},
		{"parent dir in middle", "app/../secret"},
		{"multiple parent dirs", "../../etc/passwd"},
		{"windows style", "..\\..\\etc\\passwd"},
		{"mixed slashes with dots", "app/./../../etc"},
		{"double dots only", "...."},
		{"triple dots", "..."},
		{"dot dot in subdomain style", "app..evil.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDomain(tt.domain)
			// All these should be rejected (either path traversal or other validation)
			// The main goal is that ".." patterns are caught
			if strings.Contains(tt.domain, "..") {
				assert.ErrorIs(t, err, ErrDomainPathTraversal, "domain %q should be rejected for path traversal", tt.domain)
			}
		})
	}
}

// TestService_ValidationHappensBeforeStore ensures validation runs before any store calls.
func TestService_ValidationHappensBeforeStore(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	// With path traversal domain, store methods should never be called
	// We don't set up any expectations, so if store is called, the test will fail

	_, err := svc.ListKeys(context.Background(), "../etc/passwd")
	assert.ErrorIs(t, err, ErrDomainPathTraversal)

	_, err = svc.GetAll(context.Background(), "../etc/passwd")
	assert.ErrorIs(t, err, ErrDomainPathTraversal)

	err = svc.Set(context.Background(), "../etc/passwd", map[string]string{"key": "value"})
	assert.ErrorIs(t, err, ErrDomainPathTraversal)

	err = svc.Delete(context.Background(), "../etc/passwd", "key")
	assert.ErrorIs(t, err, ErrDomainPathTraversal)

	// Verify no store methods were called (mockery would fail if unexpected calls happened)
	mock.AssertExpectationsForObjects(t, store)
}

func TestService_SetAttachment(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	secrets := map[string]string{"POSTGRES_PASSWORD": "secret123"}
	store.EXPECT().SetAttachment("app__example__com-postgres", secrets).Return(nil)

	err := svc.SetAttachment(context.Background(), "app.example.com", "postgres", secrets)
	assert.NoError(t, err)
}

func TestService_DeleteAttachment(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	store.EXPECT().DeleteAttachment("app__example__com-postgres", "OLD_KEY").Return(nil)

	err := svc.DeleteAttachment(context.Background(), "app.example.com", "postgres", "OLD_KEY")
	assert.NoError(t, err)
}

func TestService_SetAttachment_InvalidDomain(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.SetAttachment(context.Background(), "", "postgres", map[string]string{"KEY": "val"})
	assert.ErrorIs(t, err, ErrDomainEmpty)
}

func TestService_SetAttachment_EmptyService(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.SetAttachment(context.Background(), "app.example.com", "", map[string]string{"KEY": "val"})
	assert.ErrorIs(t, err, ErrServiceEmpty)
}

func TestService_DeleteAttachment_InvalidDomain(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.DeleteAttachment(context.Background(), "", "postgres", "SOME_KEY")
	assert.ErrorIs(t, err, ErrDomainEmpty)
}

func TestService_DeleteAttachment_EmptyService(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.DeleteAttachment(context.Background(), "app.example.com", "", "SOME_KEY")
	assert.ErrorIs(t, err, ErrServiceEmpty)
}

func TestService_SetAttachment_InvalidServiceName(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.SetAttachment(context.Background(), "app.example.com", "../evil", map[string]string{"K": "V"})
	assert.ErrorIs(t, err, ErrInvalidServiceName)
}

func TestService_DeleteAttachment_InvalidServiceName(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.DeleteAttachment(context.Background(), "app.example.com", "foo/bar", "SOME_KEY")
	assert.ErrorIs(t, err, ErrInvalidServiceName)
}

func TestService_SetAttachment_ServiceNameTooLong(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	// Service name exceeds DNS label limit of 63 characters
	longService := strings.Repeat("a", 64)
	err := svc.SetAttachment(context.Background(), "app.example.com", longService, map[string]string{"K": "V"})
	assert.ErrorIs(t, err, ErrInvalidServiceName)
}

func TestService_SetAttachment_ServiceNameTrailingHyphen(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.SetAttachment(context.Background(), "app.example.com", "postgres-", map[string]string{"K": "V"})
	assert.ErrorIs(t, err, ErrInvalidServiceName)
}

func TestService_DeleteAttachment_ServiceNameTooLong(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	// Service name exceeds DNS label limit of 63 characters
	longService := strings.Repeat("a", 64)
	err := svc.DeleteAttachment(context.Background(), "app.example.com", longService, "SOME_KEY")
	assert.ErrorIs(t, err, ErrInvalidServiceName)
}

func TestService_DeleteAttachment_ServiceNameTrailingHyphen(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	err := svc.DeleteAttachment(context.Background(), "app.example.com", "redis-", "SOME_KEY")
	assert.ErrorIs(t, err, ErrInvalidServiceName)
}

func TestService_AttachmentValidationHappensBeforeStore(t *testing.T) {
	store := outmocks.NewMockDomainSecretStore(t)
	svc := NewService(store, testLogger())

	// With invalid service name, store methods should never be called
	err := svc.SetAttachment(context.Background(), "app.example.com", "../evil", map[string]string{"key": "value"})
	assert.ErrorIs(t, err, ErrInvalidServiceName)

	err = svc.DeleteAttachment(context.Background(), "app.example.com", "../evil", "key")
	assert.ErrorIs(t, err, ErrInvalidServiceName)

	// Verify no store methods were called
	mock.AssertExpectationsForObjects(t, store)
}
