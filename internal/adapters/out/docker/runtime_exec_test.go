package docker

import (
	"bytes"
	"context"
	"encoding/binary"
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
