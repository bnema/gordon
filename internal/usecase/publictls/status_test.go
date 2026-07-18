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
	secret := t.Name()
	result := sanitizeError("token=" + secret + " provider said invalid")
	assert.NotContains(t, result, secret)
	assert.Contains(t, result, "token=redacted")
}

func TestSanitizeError_KeyEqQuotedValue(t *testing.T) {
	result := sanitizeError(`secret="my-secret-value" is invalid`)
	assert.NotContains(t, result, "my-secret-value")
	assert.Contains(t, result, `secret="redacted"`)
}

func TestSanitizeError_KeyEqSingleQuotedValue(t *testing.T) {
	secret := t.Name()
	result := sanitizeError("key='" + secret + "' not found")
	assert.NotContains(t, result, secret)
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
	apiKey := t.Name()
	password := "password-" + t.Name()
	result := sanitizeError("configuration error:\n  api_key: " + apiKey + "\n  password: " + password)
	assert.NotContains(t, result, apiKey)
	assert.NotContains(t, result, password)
	assert.Contains(t, result, "api_key: redacted")
	assert.Contains(t, result, "password: redacted")
	// Non-sensitive context preserved
	assert.Contains(t, result, "configuration error:")
}

func TestSanitizeError_CaseInsensitive(t *testing.T) {
	token := t.Name()
	secret := "secret-" + t.Name()
	result := sanitizeError("Token=" + token + " and SECRET=" + secret)
	assert.NotContains(t, result, token)
	assert.NotContains(t, result, secret)
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
