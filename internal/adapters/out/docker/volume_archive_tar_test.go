package docker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bnema.dev/zerowrap"
)

func TestClassifyVanishedTarFailure(t *testing.T) {
	tests := []struct {
		name   string
		code   int64
		stderr string
		want   bool
	}{
		{"busybox journal vanished", 1, "tar: ./x.db-journal: No such file or directory\ntar: error exit delayed from previous errors\n", true},
		{"busybox can't open", 1, "tar: can't open './data/x.db-journal': No such file or directory\ntar: error exit delayed from previous errors", true},
		{"gnu removed", 1, "tar: ./a: File removed before we read it\ntar: Exiting with failure status due to previous errors", true},
		{"gnu removed lowercase rejected", 1, "tar: ./a: file removed before we read it", false},
		{"gnu changed rejected", 1, "tar: ./a: file changed as we read it\n", false},
		{"non member enoent rejected", 1, "tar: /tmp/gordon-volume-backup.tar: No such file or directory", false},
		{"can't open non member rejected", 1, "tar: can't open '/tmp/fifo': No such file or directory", false},
		{"empty member path rejected", 1, "tar: ./: No such file or directory", false},
		{"exit zero", 0, "", false},
		{"exit two", 2, "tar: ./x: No such file or directory", false},
		{"zstd failure exit three", 3, "tar: ./x: No such file or directory\ngordon: zstd compression failed with status 137", false},
		{"empty stderr", 1, "", false},
		{"only summary line", 1, "tar: error exit delayed from previous errors", false},
		{"permission denied mixed", 1, "tar: ./x: No such file or directory\ntar: ./y: Permission denied\ntar: error exit delayed from previous errors", false},
		{"unknown error", 1, "tar: write error: No space left on device", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := classifyVanishedTarFailure(tt.code, tt.stderr)
			assert.Equal(t, tt.want, ok)
		})
	}
}

func TestCloseVolumeArchivePipe(t *testing.T) {
	vanished := "tar: ./x.db-journal: No such file or directory\ntar: error exit delayed from previous errors"
	copyErr := errors.New("copy failed")
	waitErr := errors.New("wait failed")
	tests := []struct {
		name      string
		copyErr   error
		waitErr   error
		code      int64
		stderr    string
		wantErr   error
		errSubstr string
		wantWarn  bool
	}{
		{name: "success", code: 0},
		{name: "copy error wins over tolerance", copyErr: copyErr, code: 1, stderr: vanished, wantErr: copyErr},
		{name: "wait error wins over tolerance", waitErr: waitErr, code: 1, stderr: vanished, wantErr: waitErr},
		{name: "vanished tolerated", code: 1, stderr: vanished, wantWarn: true},
		{name: "other exit 1 fails", code: 1, stderr: "tar: ./y: Permission denied", errSubstr: "exited with code 1"},
		{name: "zstd failure fails", code: 3, stderr: "gordon: zstd compression failed with status 137", errSubstr: "zstd compression failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &logs})
			pr, pw := io.Pipe()
			go closeVolumeArchivePipe(pw, tt.copyErr, tt.waitErr, tt.code, tt.stderr, log)

			_, err := io.ReadAll(pr)
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.errSubstr != "":
				require.ErrorContains(t, err, tt.errSubstr)
			default:
				require.NoError(t, err, "reader must see clean EOF")
			}
			assert.Equal(t, tt.wantWarn, strings.Contains(logs.String(), "files vanished"))
		})
	}
}

func TestCloseVolumeArchivePipeLogsAtMostFiveLines(t *testing.T) {
	var stderr strings.Builder
	for i := range 8 {
		fmt.Fprintf(&stderr, "tar: ./f%d: No such file or directory\n", i)
	}
	stderr.WriteString("tar: error exit delayed from previous errors\n")

	var logs bytes.Buffer
	log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &logs})
	pr, pw := io.Pipe()
	go closeVolumeArchivePipe(pw, nil, nil, 1, stderr.String(), log)
	_, err := io.ReadAll(pr)
	require.NoError(t, err)

	var entry struct {
		Count int      `json:"vanished_count"`
		Lines []string `json:"tar_stderr"`
	}
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	assert.Equal(t, 8, entry.Count)
	assert.Len(t, entry.Lines, 5)
}
