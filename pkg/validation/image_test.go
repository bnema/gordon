package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseImageReference(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		wantName string
		wantRef  string
	}{
		{
			name:     "simple image with latest tag",
			imageRef: "myapp:latest",
			wantName: "myapp",
			wantRef:  "latest",
		},
		{
			name:     "simple image with custom tag",
			imageRef: "myapp:v1.0.0",
			wantName: "myapp",
			wantRef:  "v1.0.0",
		},
		{
			name:     "simple image with digest",
			imageRef: "myapp@sha256:abc123def456",
			wantName: "myapp",
			wantRef:  "sha256:abc123def456",
		},
		{
			name:     "registry with image and tag",
			imageRef: "registry.example.com/myapp:latest",
			wantName: "registry.example.com/myapp",
			wantRef:  "latest",
		},
		{
			name:     "registry with port and image and tag",
			imageRef: "registry.example.com:5000/myapp:v1.0",
			wantName: "registry.example.com:5000/myapp",
			wantRef:  "v1.0",
		},
		{
			name:     "registry with port, image, and digest",
			imageRef: "registry.example.com:5000/myapp@sha256:abc123",
			wantName: "registry.example.com:5000/myapp",
			wantRef:  "sha256:abc123",
		},
		{
			name:     "simple image without tag (defaults to latest)",
			imageRef: "myapp",
			wantName: "myapp",
			wantRef:  "latest",
		},
		{
			name:     "registry with image without tag",
			imageRef: "registry.example.com/myapp",
			wantName: "registry.example.com/myapp",
			wantRef:  "latest",
		},
		{
			name:     "nested path image with tag",
			imageRef: "registry.example.com/path/to/myapp:latest",
			wantName: "registry.example.com/path/to/myapp",
			wantRef:  "latest",
		},
		{
			name:     "localhost registry with port",
			imageRef: "localhost:5000/myapp:latest",
			wantName: "localhost:5000/myapp",
			wantRef:  "latest",
		},
		{
			name:     "IP address registry with port",
			imageRef: "192.168.1.100:5000/myapp:latest",
			wantName: "192.168.1.100:5000/myapp",
			wantRef:  "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotRef := ParseImageReference(tt.imageRef)
			assert.Equal(t, tt.wantName, gotName, "name mismatch")
			assert.Equal(t, tt.wantRef, gotRef, "reference mismatch")
		})
	}
}
