package remote

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateRemoteName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "myserver", false},
		{"valid with dots", "my.server", false},
		{"valid with dashes", "my-server", false},
		{"valid with underscore", "my_server", false},
		{"invalid slash", "my/server", true},
		{"invalid dotdot", "my..server", true},
		{"invalid space", "my server", true},
		{"empty", "", true},
		{"leading dot", ".server", false},
		{"trailing dot", "server.", false},
		{"only dots", "...", true},
		{"only dashes", "---", false},
		{"very long name", strings.Repeat("a", 256), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRemoteName(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
