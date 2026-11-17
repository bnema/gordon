package docker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// The zstd command is exercised for real in an alpine helper with zstd
// installed; this guards the shell structure that makes it correct.
func TestVolumeArchiveCommandZstdStructure(t *testing.T) {
	cmd, err := volumeArchiveCommand(domain.VolumeBackupCompressionZstd)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(cmd, "cd /volume || exit 1; "), "cd must run in the parent shell")
	assert.Contains(t, cmd, `zstd -T0 -c "$fifo" & zstd_pid=$!;`, "only zstd may be backgrounded")
	assert.Equal(t, 1, strings.Count(cmd, "&")-strings.Count(cmd, "&&")-strings.Count(cmd, ">&"), "single background job")
	assert.NotContains(t, cmd, "&&")
	assert.Contains(t, cmd, "exit 3; fi; exit $tar_status", "zstd failure must win over tar status")
}

func TestVolumeArchiveCommandGzip(t *testing.T) {
	cmd, err := volumeArchiveCommand(domain.VolumeBackupCompressionGzip)
	require.NoError(t, err)
	assert.Equal(t, "cd /volume && tar -czf - .", cmd)
}
