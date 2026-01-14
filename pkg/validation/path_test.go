package validation

import (
	"strings"
	"testing"
)

func TestValidateRepositoryName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{"simple name", "myapp", false, ""},
		{"with hyphen", "my-app", false, ""},
		{"with underscore", "my_app", false, ""},
		{"with dot", "my.app", false, ""},
		{"nested path", "myorg/myapp", false, ""},
		{"deeply nested", "myorg/team/myapp", false, ""},
		{"with numbers", "app123", false, ""},
		{"complex valid", "my-org/my_app.v2", false, ""},

		// Invalid cases - path traversal
		{"path traversal simple", "../etc/passwd", true, "path traversal"},
		{"path traversal nested", "myorg/../../../etc", true, "path traversal"},
		{"path traversal in middle", "myorg/..hidden/app", true, "path traversal"},
		{"double dot only", "..", true, "path traversal"},

		// Invalid cases - format
		{"empty", "", true, "cannot be empty"},
		{"starts with hyphen", "-myapp", true, "invalid repository name format"},
		{"starts with dot", ".myapp", true, "invalid repository name format"},
		{"ends with hyphen", "myapp-", true, "invalid repository name format"},
		{"uppercase", "MyApp", true, "invalid repository name format"},
		{"special chars", "my@app", true, "invalid repository name format"},
		{"spaces", "my app", true, "invalid repository name format"},
		{"adjacent separators", "my--app", true, "invalid repository name format"},
		{"starts with slash", "/myapp", true, "invalid repository name format"},
		{"ends with slash", "myapp/", true, "invalid repository name format"},
		{"too long", strings.Repeat("a", 257), true, "too long"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRepositoryName(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateRepositoryName(%q) expected error containing %q, got nil", tt.input, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateRepositoryName(%q) error = %q, want error containing %q", tt.input, err.Error(), tt.errMsg)
				}
			} else if err != nil {
				t.Errorf("ValidateRepositoryName(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestValidateReference(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// Valid tags
		{"simple tag", "latest", false, ""},
		{"version tag", "v1.0.0", false, ""},
		{"with hyphen", "my-tag", false, ""},
		{"with underscore", "my_tag", false, ""},
		{"with dot", "my.tag", false, ""},
		{"numeric", "123", false, ""},
		{"alphanumeric", "abc123", false, ""},

		// Valid digests as references
		{"sha256 digest", "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", false, ""},
		{"sha512 digest", "sha512:" + strings.Repeat("a", 128), false, ""},

		// Invalid cases - path traversal
		{"path traversal", "../etc", true, "path traversal"},
		{"hidden traversal", "tag/../other", true, "path traversal"},

		// Invalid cases - format
		{"empty", "", true, "cannot be empty"},
		{"starts with hyphen", "-tag", true, "invalid reference format"},
		{"starts with dot", ".tag", true, "invalid reference format"},
		{"too long tag", strings.Repeat("a", 129), true, "invalid reference format"},
		{"special chars", "tag@latest", true, "invalid reference format"},
		{"spaces", "my tag", true, "invalid reference format"},

		// Invalid digests
		{"wrong algorithm", "md5:abc123", true, "invalid reference format"},
		{"short sha256", "sha256:abc", true, "invalid reference format"},
		{"uppercase hex", "sha256:ABC123" + strings.Repeat("0", 58), true, "invalid reference format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateReference(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateReference(%q) expected error containing %q, got nil", tt.input, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateReference(%q) error = %q, want error containing %q", tt.input, err.Error(), tt.errMsg)
				}
			} else if err != nil {
				t.Errorf("ValidateReference(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestValidateDigest(t *testing.T) {
	validSHA256 := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	validSHA512 := "sha512:" + strings.Repeat("a", 128)

	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{"valid sha256", validSHA256, false, ""},
		{"valid sha512", validSHA512, false, ""},

		// Invalid cases - path traversal
		{"path traversal", "sha256:../../../etc/passwd", true, "path traversal"},

		// Invalid cases - format
		{"empty", "", true, "cannot be empty"},
		{"no algorithm", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", true, "invalid digest format"},
		{"wrong algorithm", "md5:abc123", true, "invalid digest format"},
		{"sha1 not allowed", "sha1:" + strings.Repeat("a", 40), true, "invalid digest format"},
		{"short hash", "sha256:abc", true, "invalid digest format"},
		{"uppercase hex", "sha256:" + strings.Repeat("A", 64), true, "invalid digest format"},
		{"invalid chars", "sha256:" + strings.Repeat("g", 64), true, "invalid digest format"},
		{"missing colon", "sha256" + strings.Repeat("a", 64), true, "invalid digest format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDigest(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateDigest(%q) expected error containing %q, got nil", tt.input, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateDigest(%q) error = %q, want error containing %q", tt.input, err.Error(), tt.errMsg)
				}
			} else if err != nil {
				t.Errorf("ValidateDigest(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestValidateUUID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// Valid cases
		{"valid uuid", "1234567890-myapp", false, ""},
		{"with underscore", "1234567890-my_app", false, ""},
		{"with hyphen in name", "1234567890-my-app", false, ""},
		{"nested repo uuid", "1234567890-myorg_myapp", false, ""},

		// Invalid cases - path traversal
		{"path traversal", "1234567890-../etc/passwd", true, "path traversal"},
		{"traversal in middle", "1234567890-app/../etc", true, "path traversal"},

		// Invalid cases - format
		{"empty", "", true, "cannot be empty"},
		{"no timestamp", "myapp", true, "invalid UUID format"},
		{"no hyphen", "1234567890myapp", true, "invalid UUID format"},
		{"special chars", "1234567890-my@app", true, "invalid UUID format"},
		{"spaces", "1234567890-my app", true, "invalid UUID format"},
		{"slash not replaced", "1234567890-myorg/myapp", true, "invalid UUID format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUUID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateUUID(%q) expected error containing %q, got nil", tt.input, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateUUID(%q) error = %q, want error containing %q", tt.input, err.Error(), tt.errMsg)
				}
			} else if err != nil {
				t.Errorf("ValidateUUID(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		errMsg   string
		wantPath string
	}{
		// Valid cases
		{"simple", "myfile", false, "", "myfile"},
		{"nested", "dir/file", false, "", "dir/file"},
		{"with dots in name", "file.txt", false, "", "file.txt"},

		// Invalid cases
		{"empty", "", true, "cannot be empty", ""},
		{"path traversal", "../etc/passwd", true, "path traversal", ""},
		{"hidden traversal", "dir/../../../etc", true, "path traversal", ""},
		{"absolute path", "/etc/passwd", true, "absolute paths not allowed", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidatePath(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidatePath(%q) expected error containing %q, got nil", tt.input, tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidatePath(%q) error = %q, want error containing %q", tt.input, err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidatePath(%q) unexpected error: %v", tt.input, err)
				}
				if got != tt.wantPath {
					t.Errorf("ValidatePath(%q) = %q, want %q", tt.input, got, tt.wantPath)
				}
			}
		})
	}
}

func TestValidatePathWithinRoot(t *testing.T) {
	tests := []struct {
		name     string
		rootDir  string
		fullPath string
		wantErr  bool
	}{
		{"within root", "/data", "/data/file.txt", false},
		{"nested within root", "/data", "/data/subdir/file.txt", false},
		{"exact root", "/data", "/data", false},
		{"escapes root", "/data", "/etc/passwd", true},
		{"traversal escape", "/data", "/data/../etc/passwd", true},
		{"sibling dir", "/data/registry", "/data/secrets/file", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePathWithinRoot(tt.rootDir, tt.fullPath)
			if tt.wantErr && err == nil {
				t.Errorf("ValidatePathWithinRoot(%q, %q) expected error, got nil", tt.rootDir, tt.fullPath)
			} else if !tt.wantErr && err != nil {
				t.Errorf("ValidatePathWithinRoot(%q, %q) unexpected error: %v", tt.rootDir, tt.fullPath, err)
			}
		})
	}
}
