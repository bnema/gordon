package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseExecOutput_SplitsStdoutAndStderr(t *testing.T) {
	stream := append(frameDockerStream(1, []byte("hello\n")), frameDockerStream(2, []byte("warn\n"))...)

	stdout, stderr, err := parseExecOutput(bytes.NewReader(stream))

	require.NoError(t, err)
	assert.Equal(t, []byte("hello\n"), stdout)
	assert.Equal(t, []byte("warn\n"), stderr)
}

func TestRuntime_ExecInContainer_RejectsEmptyCommand(t *testing.T) {
	r := &Runtime{}

	tests := []struct {
		name string
		cmd  []string
	}{
		{name: "nil", cmd: nil},
		{name: "empty slice", cmd: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := r.ExecInContainer(context.Background(), "abc123", tt.cmd)
			require.Error(t, err)
			assert.Nil(t, result)
		})
	}
}

func frameDockerStream(streamID byte, payload []byte) []byte {
	frame := make([]byte, 8+len(payload))
	frame[0] = streamID
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

func TestExtractFileFromTarMatchesFullCleanPath(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, writeTarFile(tw, "other/.env", "wrong"))
	require.NoError(t, writeTarFile(tw, "app/.env", "right"))
	require.NoError(t, tw.Close())

	rc, err := extractFileFromTar(io.NopCloser(bytes.NewReader(buf.Bytes())), "/app/.env")
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "right", string(data))
}

func TestExtractFileFromTarMatchesBasenameForCopiedFile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, writeTarFile(tw, "gordon-backup-test.bak", "backup data"))
	require.NoError(t, tw.Close())

	rc, err := extractFileFromTar(io.NopCloser(bytes.NewReader(buf.Bytes())), "/tmp/gordon-backup-test.bak")
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "backup data", string(data))
}

func TestExtractFileFromTarDoesNotMatchBasenameInNestedDirectory(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, writeTarFile(tw, "other/gordon-backup-test.bak", "wrong"))
	require.NoError(t, tw.Close())

	rc, err := extractFileFromTar(io.NopCloser(bytes.NewReader(buf.Bytes())), "/tmp/gordon-backup-test.bak")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file not found")
	assert.Nil(t, rc)
}

func writeTarFile(tw *tar.Writer, name, content string) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(content))}); err != nil {
		return err
	}
	_, err := tw.Write([]byte(content))
	return err
}

func TestReadExtractedEnvFileRejectsOversizedContent(t *testing.T) {
	data, err := readExtractedEnvFile(io.NopCloser(strings.NewReader(strings.Repeat("a", maxExtractedEnvFileSize+1))))
	require.Error(t, err)
	assert.Nil(t, data)
	assert.Contains(t, err.Error(), "exceeds")
}
