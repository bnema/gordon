package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"password env", "PASSWORD=hunter2", "PASSWORD=[REDACTED]"},
		{"token env", "TOKEN=abc123", "TOKEN=[REDACTED]"},
		{"secret env", "SECRET=shh", "SECRET=[REDACTED]"},
		{"api key env", "API_KEY=key-value", "API_KEY=[REDACTED]"},
		{"database url env", "DATABASE_URL=postgres://user:pass@db/app", "DATABASE_URL=[REDACTED]"},
		{"json password field", `{"PASSWORD":"hunter2","ok":"value"}`, `{"PASSWORD":"[REDACTED]","ok":"value"}`},
		{"json token field with spaces", `{"TOKEN": "abc123"}`, `{"TOKEN": "[REDACTED]"}`},
		{"case insensitive", "database_url=postgres://user:pass@db/app", "database_url=[REDACTED]"},
		{"prefixed password env", "POSTGRES_PASSWORD=hunter2", "POSTGRES_PASSWORD=[REDACTED]"},
		{"prefixed token env", "GORDON_TOKEN=abc123", "GORDON_TOKEN=[REDACTED]"},
		{"secret key env", "SECRET_KEY=shh", "SECRET_KEY=[REDACTED]"},
		{"api token env", "API_TOKEN=abc123", "API_TOKEN=[REDACTED]"},
		{"authorization bearer", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.secret", "Authorization: Bearer [REDACTED]"},
		{"json prefixed password field", `{"POSTGRES_PASSWORD":"hunter2"}`, `{"POSTGRES_PASSWORD":"[REDACTED]"}`},
		{"json secret key field", `{"secret_key": "shh"}`, `{"secret_key": "[REDACTED]"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RedactSecrets(tt.in))
		})
	}
}
