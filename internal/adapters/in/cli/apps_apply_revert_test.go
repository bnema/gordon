package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

var cliRevert = dto.AppSetImageRevertDTO{
	Service: "web", ServerImage: "reg.example.com/web@sha256:abc", FileImage: "img:1",
	Revision: "rev-2", Principal: "oidc:https://ci#repo", ChangedAt: time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC),
}

// revertConflictErr is the error ApplyApp returns for a set-image revert 409.
func revertConflictErr() error {
	return fmt.Errorf("apply app: %w", &remote.SetImageRevertError{
		Status: "409 Conflict",
		Conflict: dto.AppSetImageRevertConflict{
			Error: dto.AppSetImageRevertCode, Message: "apply would revert a set-image change",
			DesiredRevision: "rev-2", SetImageReverts: []dto.AppSetImageRevertDTO{cliRevert},
		},
	})
}

func isRealApply(confirmed bool) any {
	return mock.MatchedBy(func(req dto.AppApplyRequest) bool {
		if confirmed {
			return req.AllowSetImageRevert && req.ExpectedRevision == "rev-2"
		}
		return !req.AllowSetImageRevert && req.ExpectedRevision == ""
	})
}

func appliedWithRevert() *dto.AppApplyResponse {
	return &dto.AppApplyResponse{
		App: "blog", ResultingRevision: "rev-3", Pending: true, Intent: "apply-3",
		SetImageReverts: []dto.AppSetImageRevertDTO{cliRevert},
	}
}

const pasteLines = "[services.web]\nimage = \"reg.example.com/web@sha256:abc\""

func TestRunAppsApply_YesAllowsRevertWithoutPrompt(t *testing.T) {
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.MatchedBy(func(req dto.AppApplyRequest) bool {
		return req.AllowSetImageRevert && req.ExpectedRevision == ""
	})).Return(appliedWithRevert(), nil).Once()
	var out bytes.Buffer

	err := runAppsApply(context.Background(), plane, strings.NewReader(""), &out, &bytes.Buffer{},
		appsApplyOptions{File: writeManifest(t, "x"), Yes: true})

	require.NoError(t, err)
	assert.Contains(t, out.String(), "Applied blog (rev-3)")
	assert.Contains(t, out.String(), "restored:")
	assert.NotContains(t, out.String(), "Continue?")
}

func TestRunAppsApply_SetImageRevertConflict(t *testing.T) {
	tests := []struct {
		name        string
		interactive bool
		answer      string
		wantRetry   bool
		wantErr     string
	}{
		{name: "terminal yes retries against the checked revision", interactive: true, answer: "y\n", wantRetry: true},
		{name: "terminal no keeps the server images", interactive: true, answer: "n\n", wantErr: "apply cancelled"},
		{name: "terminal empty answer defaults to no", interactive: true, answer: "\n", wantErr: "apply cancelled"},
		{name: "terminal end of input means no", interactive: true, answer: "", wantErr: "apply cancelled"},
		{name: "terminal unterminated yes at end of input means no", interactive: true, answer: "yes", wantErr: "apply cancelled"},
		{name: "no terminal refuses and names --yes", interactive: false, wantErr: "--yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plane := appPlane(t)
			plane.EXPECT().ApplyApp(mock.Anything, isRealApply(false)).Return(nil, revertConflictErr()).Once()
			if tt.wantRetry {
				plane.EXPECT().ApplyApp(mock.Anything, isRealApply(true)).Return(appliedWithRevert(), nil).Once()
			}
			var out, errOut bytes.Buffer

			err := runAppsApply(context.Background(), plane, strings.NewReader(tt.answer), &out, &errOut,
				appsApplyOptions{File: writeManifest(t, "x"), Interactive: tt.interactive})

			// On a terminal the warning, prompt and paste lines go to stderr so
			// a redirected stdout cannot hide the question.
			text := out.String()
			if tt.interactive {
				text = errOut.String()
				assert.NotContains(t, out.String(), "Continue?")
			}
			for _, want := range []string{"web", cliRevert.ServerImage, "img:1", "rev-2", "oidc:https://ci#repo", "2026-10-20T09:30:00Z", "restores the file value"} {
				assert.Contains(t, text, want)
			}
			assert.Equal(t, tt.interactive, strings.Contains(text, "Continue? [y/N]"), "prompt only on a terminal")
			if tt.wantRetry {
				require.NoError(t, err)
				assert.Contains(t, out.String(), "Applied blog (rev-3)")
				assert.NotContains(t, text, pasteLines)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, text, pasteLines, "the lines that keep the server image are printed")
		})
	}
}

func TestRunAppsApply_SetImageRevertJSONNeverPrompts(t *testing.T) {
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(nil, revertConflictErr()).Once()
	var out bytes.Buffer

	err := runAppsApply(context.Background(), plane, strings.NewReader("y\n"), &out, &bytes.Buffer{},
		appsApplyOptions{File: writeManifest(t, "x"), JSON: true, Interactive: true})

	require.Error(t, err)
	var got dto.AppSetImageRevertConflict
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), "stdout is exactly one JSON document")
	assert.Equal(t, "rev-2", got.DesiredRevision)
	assert.Equal(t, []dto.AppSetImageRevertDTO{cliRevert}, got.SetImageReverts)
}

func TestRunAppsApply_DryRunWarnsWithoutPrompt(t *testing.T) {
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.MatchedBy(func(req dto.AppApplyRequest) bool { return req.DryRun })).Return(&dto.AppApplyResponse{
		App:             "blog",
		Diff:            dto.AppDiffSection{Changed: []string{"service/web/image", "service/web/command"}},
		SetImageReverts: []dto.AppSetImageRevertDTO{cliRevert},
	}, nil).Once()
	var out bytes.Buffer

	err := runAppsApply(context.Background(), plane, strings.NewReader("y\n"), &out, &bytes.Buffer{},
		appsApplyOptions{File: writeManifest(t, "x"), DryRun: true, Interactive: true})

	require.NoError(t, err)
	text := out.String()
	assert.Contains(t, text, "Validated (dry-run) blog")
	assert.Contains(t, text, "service/web/image  (changed on the server (set-image, rev-2))")
	assert.Contains(t, text, "service/web/command  (changed in your file)")
	assert.Contains(t, text, "would revert 1 set-image change")
	assert.Contains(t, text, pasteLines)
	assert.NotContains(t, text, "Continue?")
}

func TestRunAppsDiffFile(t *testing.T) {
	preview := &dto.AppApplyResponse{
		App:             "blog",
		Diff:            dto.AppDiffSection{Changed: []string{"service/web/image", "env/MODE"}},
		SetImageReverts: []dto.AppSetImageRevertDTO{cliRevert},
	}
	tests := []struct {
		name    string
		app     string
		jsonOut bool
		wantErr string
	}{
		{name: "text marks where each path changed", app: "blog"},
		{name: "json includes set_image_reverts", app: "blog", jsonOut: true},
		{name: "manifest naming another app is refused", app: "shop", wantErr: `declares app "blog", not "shop"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plane := appPlane(t)
			plane.EXPECT().ApplyApp(mock.Anything, mock.MatchedBy(func(req dto.AppApplyRequest) bool {
				return req.DryRun && !req.AllowSetImageRevert
			})).Return(preview, nil).Once()
			var out bytes.Buffer

			err := runAppsDiffFile(context.Background(), plane, tt.app, writeManifest(t, "x"), &out, tt.jsonOut)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.jsonOut {
				var got dto.AppApplyResponse
				require.NoError(t, json.Unmarshal(out.Bytes(), &got))
				assert.Contains(t, out.String(), `"set_image_reverts"`)
				assert.Equal(t, []dto.AppSetImageRevertDTO{cliRevert}, got.SetImageReverts)
				return
			}
			text := out.String()
			assert.Contains(t, text, "service/web/image  (changed on the server (set-image, rev-2))")
			assert.Contains(t, text, "env/MODE  (changed in your file)")
			assert.Contains(t, text, pasteLines)
		})
	}
}

// The paste lines must decode back to the same service names, including
// names with dots that a bare TOML key would split into nested tables.
func TestRenderSetImageKeepLines_RoundTrip(t *testing.T) {
	reverts := []dto.AppSetImageRevertDTO{
		{Service: "web", ServerImage: "reg/web@sha256:a"},
		{Service: "web.api", ServerImage: "reg/api@sha256:b"},
	}
	var out bytes.Buffer
	require.NoError(t, renderSetImageKeepLines(&out, "", reverts))

	text := out.String()
	body := text[strings.Index(text, "\n["):]
	var doc struct {
		Services map[string]struct {
			Image string `toml:"image"`
		} `toml:"services"`
	}
	require.NoError(t, toml.Unmarshal([]byte(body), &doc))
	assert.Equal(t, "reg/web@sha256:a", doc.Services["web"].Image)
	assert.Equal(t, "reg/api@sha256:b", doc.Services["web.api"].Image)
}

// blockingReader never returns, standing in for a terminal nobody answers.
type blockingReader struct{ release chan struct{} }

func (r blockingReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

func TestPromptYesNo_ContextCancelled(t *testing.T) {
	in := blockingReader{release: make(chan struct{})}
	defer close(in.release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, err := promptYesNo(ctx, in, &bytes.Buffer{}, "Continue? [y/N] ")

	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, ok)
}

func TestNewAppsCmds_RevertFlags(t *testing.T) {
	assert.NotNil(t, newAppsApplyCmd().Flags().Lookup("yes"))
	assert.NotNil(t, newAppsDiffCmd().Flags().Lookup("file"))
}
