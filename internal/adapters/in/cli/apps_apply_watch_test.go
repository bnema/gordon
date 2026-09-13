package cli

// Tests for the `apps apply --deploy` chain over a 202 accepted deploy:
// it must reuse the shared by-key watch (progress only on change, terminal
// journal, Ctrl-C resume) and must never reissue the deploy POST. JSON mode
// keeps stdout to exactly one final combined {apply,deploy} document while
// progress and resume guidance go to stderr.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

func acceptedApply() *dto.AppApplyResponse {
	return &dto.AppApplyResponse{App: "blog", ResultingRevision: "rev-b", Pending: true, Intent: "apply-1"}
}

func TestRunAppsApply_DeployAcceptedPollsToSuccess(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(acceptedApply(), nil).Once()
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(
		runningOp("op-1", dto.AppStepDTO{ID: "service.web.start", State: "pending"}), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out, &errOut,
		writeManifest(t, "[app]\nname = \"blog\"\n"), false, true, false))
	text := out.String()
	assert.Contains(t, text, "Applied blog (rev-b)", "the apply outcome stays visible")
	assert.Contains(t, text, "op op-1:", "the accepted deploy reports progress through the watch path")
	assert.Contains(t, text, "op-1 blog: success", "the terminal journal is rendered")
}

func TestRunAppsApply_DeployAcceptedNoSecondMutation(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(acceptedApply(), nil).Once()
	// .Once() proves the accepted deploy is observed by key, never re-POSTed.
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1"), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out, &errOut,
		writeManifest(t, "x"), false, true, false))
}

func TestRunAppsApply_DeployAcceptedUnchangedStateNotReprinted(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	step := dto.AppStepDTO{ID: "service.web.start", State: "pending"}
	advanced := runningOp("op-1",
		dto.AppStepDTO{ID: "service.web.start", State: "succeeded"},
		dto.AppStepDTO{ID: "service.web.health", State: "pending"})

	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(acceptedApply(), nil).Once()
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1", step), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(runningOp("op-1", step), nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(advanced, nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out, &errOut,
		writeManifest(t, "x"), false, true, false))
	assert.Equal(t, 2, strings.Count(out.String(), "op op-1:"),
		"initial and one advanced poll render progress; the unchanged poll is not reprinted")
}

func TestRunAppsApply_DeployAcceptedPartialFailsButKeepsApply(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(acceptedApply(), nil).Once()
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1"), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(&dto.AppDeployResponse{
		Op: "op-1", App: "blog", Status: "partial", Outcome: "partial",
		Services: map[string]dto.AppServiceResultDTO{
			"web": {Result: "failed", EffectiveRevision: "rev-b", Error: "boom"},
		},
	}, nil).Once()

	var out, errOut bytes.Buffer
	err := runAppsApply(context.Background(), plane, strings.NewReader(""), &out, &errOut,
		writeManifest(t, "x"), false, true, false)
	require.Error(t, err, "a terminal partial deploy must exit nonzero")
	assert.Contains(t, err.Error(), "apply of blog succeeded (rev-b)")
	assert.Contains(t, err.Error(), "partial")
	assert.Contains(t, out.String(), "Applied blog (rev-b)", "the apply outcome stays visible")
	assert.Contains(t, out.String(), "boom", "the terminal journal is rendered before failing")
}

func TestRunAppsApply_DeployAcceptedJSONKeepsStdoutSingleCombinedDocument(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(acceptedApply(), nil).Once()
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(
		runningOp("op-1", dto.AppStepDTO{ID: "service.web.start", State: "pending"}), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		runningOp("op-1", dto.AppStepDTO{ID: "service.web.start", State: "succeeded"}), nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "success", Outcome: "success"}, nil).Once()

	var out, errOut bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out, &errOut,
		writeManifest(t, "x"), false, true, true))

	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var got appApplyDeployDocument
	require.NoError(t, dec.Decode(&got))
	assert.Equal(t, "apply-1", got.Apply.Intent)
	assert.Equal(t, "op-1", got.Deploy.Op)
	assert.Equal(t, "success", got.Deploy.Outcome)
	var extra json.RawMessage
	assert.ErrorIs(t, dec.Decode(&extra), io.EOF, "stdout carries exactly one JSON document")
	assert.NotContains(t, out.String(), "op op-1:", "progress must not pollute JSON stdout")
	assert.NotContains(t, out.String(), dto.AppStatusRunning, "no initial running JSON is emitted")
	assert.Contains(t, errOut.String(), "op op-1:", "progress goes to stderr in JSON mode")
}

func TestRunAppsApply_DeployAcceptedJSONFailureKeepsApplyDocument(t *testing.T) {
	shortenOperationPoll(t)
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(acceptedApply(), nil).Once()
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1"), "key-1", nil).Once()
	plane.EXPECT().OperationByKey(mock.Anything, "blog", "key-1").Return(
		&dto.AppDeployResponse{Op: "op-1", App: "blog", Status: "failed", Outcome: "failed"}, nil).Once()

	var out, errOut bytes.Buffer
	err := runAppsApply(context.Background(), plane, strings.NewReader(""), &out, &errOut,
		writeManifest(t, "x"), false, true, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply of blog succeeded (rev-b)")

	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var got appApplyDeployDocument
	require.NoError(t, dec.Decode(&got))
	assert.Equal(t, "rev-b", got.Apply.ResultingRevision, "the failed deploy must not hide the successful apply")
	assert.Equal(t, "failed", got.Deploy.Outcome)
	var extra json.RawMessage
	assert.ErrorIs(t, dec.Decode(&extra), io.EOF, "stdout carries exactly one JSON document")
}

func TestRunAppsApply_DeployAcceptedCancelPrintsResume(t *testing.T) {
	plane := appPlane(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(acceptedApply(), nil).Once()
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1"), "key-1", nil).Once()

	var out, errOut bytes.Buffer
	err := runAppsApply(ctx, plane, strings.NewReader(""), &out, &errOut,
		writeManifest(t, "x"), false, true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply of blog succeeded (rev-b)")
	text := out.String()
	assert.Contains(t, text, "not cancelled", "Ctrl-C must not imply a server-side abort")
	assert.Contains(t, text, "gordon apps operations watch blog --key key-1")
}

func TestRunAppsApply_DeployAcceptedCancelKeepsJSONStdoutClean(t *testing.T) {
	plane := appPlane(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(acceptedApply(), nil).Once()
	plane.EXPECT().DeployApp(mock.Anything, "blog", mock.Anything).Return(runningOp("op-1"), "key-1", nil).Once()

	var out, errOut bytes.Buffer
	err := runAppsApply(ctx, plane, strings.NewReader(""), &out, &errOut,
		writeManifest(t, "x"), false, true, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply of blog succeeded (rev-b)")
	assert.Empty(t, out.String(), "JSON stdout stays empty when interrupted before terminal")
	assert.Contains(t, errOut.String(), "not cancelled")
	assert.Contains(t, errOut.String(), "gordon apps operations watch blog --key key-1")
}
