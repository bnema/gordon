package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsVanishedFileOnlyTarFailure(t *testing.T) {
	tests := []struct {
		name   string
		code   int64
		stderr string
		want   bool
	}{
		{"busybox journal vanished", 1, "tar: ./x.db-journal: No such file or directory\ntar: error exit delayed from previous errors\n", true},
		{"gnu removed", 1, "tar: ./a: file removed before we read it\ntar: Exiting with failure status due to previous errors", true},
		{"gnu removed capitalized", 1, "tar: ./a: File removed before we read it", true},
		{"gnu changed", 1, "tar: ./a: file changed as we read it\n", true},
		{"exit zero", 0, "", false},
		{"exit two", 2, "tar: ./x: No such file or directory", false},
		{"empty stderr", 1, "", false},
		{"only summary line", 1, "tar: error exit delayed from previous errors", false},
		{"permission denied mixed", 1, "tar: ./x: No such file or directory\ntar: ./y: Permission denied\ntar: error exit delayed from previous errors", false},
		{"unknown error", 1, "tar: write error: No space left on device", false},
		{"non tar line", 1, "zstd: error 70\n./x: No such file or directory", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isVanishedFileOnlyTarFailure(tt.code, tt.stderr))
		})
	}
}
