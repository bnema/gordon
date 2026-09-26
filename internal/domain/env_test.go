package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateEnvKey(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		wantErr     bool
		wantErrType error
	}{
		{name: "simple", key: "FOO", wantErr: false},
		{name: "with underscore", key: "FOO_BAR", wantErr: false},
		{name: "starts with underscore", key: "_PRIVATE", wantErr: false},
		{name: "lowercase", key: "foo", wantErr: false},
		{name: "mixed", key: "myApp_v2", wantErr: false},
		{name: "empty", key: "", wantErr: true, wantErrType: ErrInvalidEnvKey},
		{name: "starts with number", key: "1BAD", wantErr: true, wantErrType: ErrInvalidEnvKey},
		{name: "contains dot", key: "FOO.BAR", wantErr: true, wantErrType: ErrInvalidEnvKey},
		{name: "contains slash", key: "FOO/BAR", wantErr: true, wantErrType: ErrPathTraversal},
		{name: "contains backslash", key: `FOO\BAR`, wantErr: true, wantErrType: ErrPathTraversal},
		{name: "path traversal", key: "..", wantErr: true, wantErrType: ErrPathTraversal},
		{name: "contains hyphen", key: "FOO-BAR", wantErr: true, wantErrType: ErrInvalidEnvKey},
		{name: "contains space", key: "FOO BAR", wantErr: true, wantErrType: ErrInvalidEnvKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnvKey(tt.key)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrType != nil {
					assert.ErrorIs(t, err, tt.wantErrType)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestContainsSecretReference(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{name: "simple pass reference", value: "${pass:myapp/db}", expected: true},
		{name: "simple sops reference", value: "${sops:secrets.yaml#/key}", expected: true},
		{name: "pass reference in text", value: "prefix-${pass:path}-suffix", expected: true},
		{name: "sops reference in text", value: "${sops:file.yaml}", expected: true},
		{name: "no reference - plain text", value: "hello world", expected: false},
		{name: "no reference - dollar sign only", value: "$FOO", expected: false},
		{name: "no reference - empty braces", value: "${}", expected: false},
		{name: "no reference - missing colon", value: "${pass}", expected: false},
		{name: "no reference - extra braces", value: "${{pass:path}}", expected: false},
		{name: "normal env var", value: "${FOO}", expected: false},
		{name: "value with equals and reference", value: "key=${pass:secret}", expected: true},
		{name: "quoted reference", value: `"${pass:secret}"`, expected: true},
		{name: "multiple references", value: "${pass:a}${sops:b}", expected: true},
		{name: "case sensitive - uppercase", value: "${PASS:secret}", expected: false},
		{name: "case sensitive - mixed", value: "${Pass:secret}", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsSecretReference(tt.value)
			assert.Equal(t, tt.expected, result)
		})
	}
}
