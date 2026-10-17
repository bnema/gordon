//go:build integration

package docker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

const volumeArchiveTestImage = "alpine:3.20"

// TestRuntime_ExportVolumeArchive_Integration_BinaryRoundTrip proves the
// exported stream is a byte-exact archive: binary content (random data
// that is not valid UTF-8) must survive export unchanged.
func TestRuntime_ExportVolumeArchive_Integration_BinaryRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	volume := fmt.Sprintf("gordon-archive-it-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })

	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "-v", volume+":/v", volumeArchiveTestImage, "sh", "-c",
		"head -c 5000000 /dev/urandom > /v/blob && sha256sum /v/blob").CombinedOutput()
	require.NoError(t, err, string(out))
	wantSum := strings.Fields(string(out))[0]

	runtime, err := NewRuntime()
	require.NoError(t, err)

	res, err := runtime.ExportVolumeArchive(ctx, domain.VolumeArchiveRequest{
		VolumeName:  volume,
		Compression: domain.VolumeBackupCompressionGzip,
		HelperImage: volumeArchiveTestImage,
	})
	require.NoError(t, err)
	defer res.Stream.Close()

	gz, err := gzip.NewReader(res.Stream)
	require.NoError(t, err)
	tr := tar.NewReader(gz)

	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if strings.TrimPrefix(header.Name, "./") != "blob" {
			continue
		}
		hash := sha256.New()
		_, err = io.Copy(hash, tr)
		require.NoError(t, err)
		assert.Equal(t, wantSum, hex.EncodeToString(hash.Sum(nil)))
		found = true
	}
	assert.True(t, found, "archive must contain the blob file")
}

// TestRuntime_ExportVolumeArchive_Integration_CancelUnblocksStream proves a
// stalled export cannot outlive its context: once the reader stops, the
// helper blocks on its output; cancellation must then fail the stream and
// remove the helper container.
func TestRuntime_ExportVolumeArchive_Integration_CancelUnblocksStream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	volume := fmt.Sprintf("gordon-archive-it-cancel-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })
	// Incompressible data far larger than the attach socket buffers.
	out, err := exec.Command("docker", "run", "--rm", "-v", volume+":/v", volumeArchiveTestImage, "sh", "-c",
		"head -c 30000000 /dev/urandom > /v/blob").CombinedOutput()
	require.NoError(t, err, string(out))

	runtime, err := NewRuntime()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := runtime.ExportVolumeArchive(ctx, domain.VolumeArchiveRequest{
		VolumeName:  volume,
		Compression: domain.VolumeBackupCompressionGzip,
		HelperImage: volumeArchiveTestImage,
	})
	require.NoError(t, err)
	defer res.Stream.Close()

	// Read a little, then stop consuming so the helper blocks on output.
	_, err = io.ReadFull(res.Stream, make([]byte, 1024))
	require.NoError(t, err)
	cancel()

	// Without reading further, cancellation alone must tear the export down.
	require.Eventually(t, func() bool {
		ps, psErr := exec.Command("docker", "ps", "-aq", "--filter", "volume="+volume).Output()
		return psErr == nil && strings.TrimSpace(string(ps)) == ""
	}, 30*time.Second, 200*time.Millisecond, "helper container must be removed after cancellation")

	_, err = io.ReadAll(res.Stream)
	require.Error(t, err, "a canceled export must not end as a complete archive")
}

// TestRuntime_ExportVolumeArchive_Integration_HelperFailure proves a helper
// that exits non-zero fails the stream instead of producing an artifact.
func TestRuntime_ExportVolumeArchive_Integration_HelperFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runtime, err := NewRuntime()
	require.NoError(t, err)

	// Docker creates the missing volume on mount; remove it afterwards.
	volume := fmt.Sprintf("gordon-archive-it-fail-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })

	// zstd is absent from plain alpine, so the helper exits non-zero.
	res, err := runtime.ExportVolumeArchive(ctx, domain.VolumeArchiveRequest{
		VolumeName:  volume,
		Compression: domain.VolumeBackupCompressionZstd,
		HelperImage: volumeArchiveTestImage,
	})
	require.NoError(t, err)
	defer res.Stream.Close()

	_, err = io.ReadAll(res.Stream)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "volume archive helper exited with code")
}
