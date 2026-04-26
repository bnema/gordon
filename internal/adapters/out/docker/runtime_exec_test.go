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
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "other/.env", Typeflag: tar.TypeReg, Size: int64(len("wrong"))}))
	_, err := tw.Write([]byte("wrong"))
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "app/.env", Typeflag: tar.TypeReg, Size: int64(len("right"))}))
	_, err = tw.Write([]byte("right"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	rc, err := extractFileFromTar(io.NopCloser(bytes.NewReader(buf.Bytes())), "/app/.env")
	require.NoError(t, err)
	defer rc.Close()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "right", string(data))
}

func TestReadExtractedEnvFileRejectsOversizedContent(t *testing.T) {
	data, err := readExtractedEnvFile(io.NopCloser(strings.NewReader(strings.Repeat("a", maxExtractedEnvFileSize+1))))
	require.Error(t, err)
	assert.Nil(t, data)
	assert.Contains(t, err.Error(), "exceeds")
}
