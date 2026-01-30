package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeDomainForEnvFile(t *testing.T) {
	tests := []struct {
		name    string
		domain  string
		want    string
		wantErr bool
	}{
		{name: "simple domain", domain: "example.com", want: "example_com"},
		{name: "subdomain", domain: "app.example.com", want: "app_example_com"},
		{name: "domain with port", domain: "example.com:8080", want: "example_com_8080"},
		{name: "domain with path", domain: "example.com/path", want: "example_com_path"},
		{name: "single char", domain: "a", want: "a"},
		{name: "hyphenated", domain: "my-app.example.com", want: "my-app_example_com"},
		{name: "empty", domain: "", wantErr: true},
		{name: "path traversal", domain: "../etc/passwd", wantErr: true},
		{name: "double dots", domain: "foo..bar", wantErr: true},
		{name: "starts with dot", domain: ".hidden", wantErr: true},
		{name: "ends with dot", domain: "trailing.", wantErr: true},
		{name: "space", domain: "has space", wantErr: true},
		{name: "special chars", domain: "bad$domain", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SanitizeDomainForEnvFile(tt.domain)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseEnvData(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "simple key-value",
			data: "FOO=bar\nBAZ=qux",
			want: map[string]string{"FOO": "bar", "BAZ": "qux"},
		},
		{
			name: "double-quoted value",
			data: `KEY="hello world"`,
			want: map[string]string{"KEY": "hello world"},
		},
		{
			name: "single-quoted value",
			data: `KEY='hello world'`,
			want: map[string]string{"KEY": "hello world"},
		},
		{
			name: "comment lines",
			data: "# this is a comment\nKEY=val",
			want: map[string]string{"KEY": "val"},
		},
		{
			name: "empty lines",
			data: "\n\nKEY=val\n\n",
			want: map[string]string{"KEY": "val"},
		},
		{
			name: "value with equals",
			data: "URL=postgres://host:5432/db?opt=1",
			want: map[string]string{"URL": "postgres://host:5432/db?opt=1"},
		},
		{
			name: "no value",
			data: "NOVALUE",
			want: map[string]string{},
		},
		{
			name: "empty value",
			data: "KEY=",
			want: map[string]string{"KEY": ""},
		},
		{
			name: "empty input",
			data: "",
			want: map[string]string{},
		},
		{
			name: "large value within buffer",
			data: "BIG=" + strings.Repeat("x", 100000),
			want: map[string]string{"BIG": strings.Repeat("x", 100000)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEnvData([]byte(tt.data))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateEnvKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{name: "simple", key: "FOO", wantErr: false},
		{name: "with underscore", key: "FOO_BAR", wantErr: false},
		{name: "starts with underscore", key: "_PRIVATE", wantErr: false},
		{name: "lowercase", key: "foo", wantErr: false},
		{name: "mixed", key: "myApp_v2", wantErr: false},
		{name: "empty", key: "", wantErr: true},
		{name: "starts with number", key: "1BAD", wantErr: true},
		{name: "contains dot", key: "FOO.BAR", wantErr: true},
		{name: "contains slash", key: "FOO/BAR", wantErr: true},
		{name: "contains backslash", key: `FOO\BAR`, wantErr: true},
		{name: "path traversal", key: "..", wantErr: true},
		{name: "contains hyphen", key: "FOO-BAR", wantErr: true},
		{name: "contains space", key: "FOO BAR", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnvKey(tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
