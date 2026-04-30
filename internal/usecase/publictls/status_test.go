package publictls

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeError_Empty(t *testing.T) {
	assert.Equal(t, "", sanitizeError(""))
}

func TestSanitizeError_NoSensitiveData(t *testing.T) {
	msg := "failed to obtain certificate: acme server returned 429"
	assert.Equal(t, msg, sanitizeError(msg))
}

func TestSanitizeError_KeyEqValue(t *testing.T) {
	result := sanitizeError("token=sk-secret-goes-here provider said invalid")
	assert.NotContains(t, result, "sk-secret-goes-here")
	assert.Contains(t, result, "token=redacted")
}

func TestSanitizeError_KeyEqQuotedValue(t *testing.T) {
	result := sanitizeError(`secret="my-secret-value" is invalid`)
	assert.NotContains(t, result, "my-secret-value")
	assert.Contains(t, result, `secret="redacted"`)
}

func TestSanitizeError_KeyEqSingleQuotedValue(t *testing.T) {
	result := sanitizeError(`key='my-api-key-12345' not found`)
	assert.NotContains(t, result, "my-api-key-12345")
	assert.Contains(t, result, "key='redacted'")
}

func TestSanitizeError_JSONKeyValue(t *testing.T) {
	result := sanitizeError(`{"token":"s3kr1t","domain":"example.com"}`)
	assert.NotContains(t, result, "s3kr1t")
	assert.Contains(t, result, `"token":"redacted"`)
	// Non-sensitive fields should be preserved
	assert.Contains(t, result, `"domain":"example.com"`)
}

func TestSanitizeError_YAMLKeyValue(t *testing.T) {
	result := sanitizeError("configuration error:\n  api_key: abcdef123456\n  password: hunter2")
	assert.NotContains(t, result, "abcdef123456")
	assert.NotContains(t, result, "hunter2")
	assert.Contains(t, result, "api_key: redacted")
	assert.Contains(t, result, "password: redacted")
	// Non-sensitive context preserved
	assert.Contains(t, result, "configuration error:")
}

func TestSanitizeError_CaseInsensitive(t *testing.T) {
	result := sanitizeError("Token=sk-live-abc123 and SECRET=top-secret-value")
	assert.NotContains(t, result, "sk-live-abc123")
	assert.NotContains(t, result, "top-secret-value")
	assert.Contains(t, result, "Token=redacted")
	assert.Contains(t, result, "SECRET=redacted")
}

func TestSanitizeError_MixedFormats(t *testing.T) {
	result := sanitizeError(`token=plain, secret="quoted", auth='single-quoted', and json {"password":"p4ss"}`)
	assert.NotContains(t, result, "plain")
	assert.NotContains(t, result, "quoted")
	assert.NotContains(t, result, "single-quoted")
	assert.NotContains(t, result, "p4ss")
	assert.Contains(t, result, "token=redacted")
	assert.Contains(t, result, `secret="redacted"`)
	assert.Contains(t, result, "auth='redacted'")
	assert.Contains(t, result, `"password":"redacted"`)
}

func TestSanitizeError_PreservesSafeContext(t *testing.T) {
	msg := "certificate for example.com failed: acme error 403"
	assert.Equal(t, msg, sanitizeError(msg))
}
